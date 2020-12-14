package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	"github.com/hashicorp/terraform-provider-hcs/internal/clients"
)

// helmConfigTemplate is the template used to generate a helm
// config for an AKS cluster based on given inputs.
//
// see generateHelmConfig for details on the inputs passed in
const helmConfigTemplate = `global:
  enabled: false
  name: consul
  datacenter: %s
  acls:
    manageSystemACLs: true
    bootstrapToken:
      secretName: %s-bootstrap-token
      secretKey: token
  gossipEncryption:
    secretName: %s-hcs
    secretKey: gossipEncryptionKey
  tls:
    enabled: true
    enableAutoEncrypt: true
    caCert:
      secretName: %s-hcs
      secretKey: caCert
externalServers:
  enabled: true
  hosts: %s
  httpsPort: 443
  useSystemRoots: true
  k8sAuthMethodHost: https://%s:443
client:
  enabled: true
  # If you are using Kubenet in your AKS cluster (the default network),
  # uncomment the line below.
  # exposeGossipPorts: true
  join: %s
connectInject:
  enabled: true`

// consulConfig represents the Consul config returned on the GetConfig response.
type ConsulConfig struct {
	Datacenter string   `json:"datacenter"`
	RetryJoin  []string `json:"retry_join"`
}

// dataSourceAgentHelmConfig is the data source for the agent Helm
// config for an HCS cluster.
func dataSourceAgentHelmConfig() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceAgentHelmConfigRead,
		Schema: map[string]*schema.Schema{
			// Required inputs
			"resource_group_name": {
				Type:             schema.TypeString,
				Required:         true,
				ValidateDiagFunc: validateResourceGroupName,
			},
			"managed_application_name": {
				Type:             schema.TypeString,
				Required:         true,
				ValidateDiagFunc: validateSlugID,
			},
			"aks_cluster_name": {
				Type:             schema.TypeString,
				Required:         true,
				ValidateDiagFunc: validateStringNotEmpty,
			},
			// Optional
			"aks_resource_group": {
				Type:             schema.TypeString,
				Optional:         true,
				ValidateDiagFunc: validateStringNotEmpty,
			},
			// Computed outputs
			"config": {
				Type:     schema.TypeString,
				Computed: true,
			},
		},
	}
}

// dataSourceAgentHelmConfigRead is the func to implement reading of the
// agent Helm config for an HCS cluster.
func dataSourceAgentHelmConfigRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	resourceGroupName := d.Get("resource_group_name").(string)
	managedAppName := d.Get("managed_application_name").(string)

	managedAppClient := meta.(*clients.Client).ManagedApplication
	app, err := managedAppClient.Get(ctx, resourceGroupName, managedAppName)
	if err != nil {
		return diag.Errorf("failed to check for presence of existing HCS Cluster (Managed Application %q) (Resource Group %q): %+v", managedAppName, resourceGroupName, err)
	}
	if app.Response.StatusCode == 404 {
		// No managed application exists, so returning an error stating as such
		return diag.Errorf("[ERROR] no HCS Cluster found for (Managed Application %q) (Resource Group %q).", managedAppName, resourceGroupName)
	}

	managedAppManagedResourceGroupID := *app.ManagedResourceGroupID

	crpClient := meta.(*clients.Client).CustomResourceProvider

	resp, err := crpClient.Config(ctx, managedAppManagedResourceGroupID)
	if err != nil {
		return diag.Errorf("failed to get config for managed app: %+v", err)
	}

	var consulConfig ConsulConfig
	err = json.Unmarshal([]byte(resp.ClientConfig), &consulConfig)
	if err != nil {
		return diag.Errorf("failed to json unmarshal Consul config %v", err)
	}

	// default to resourceGroupName if aks_resource_group is not provided
	aksResourceGroup := d.Get("aks_resource_group").(string)
	if aksResourceGroup == "" {
		aksResourceGroup = resourceGroupName
	}

	aksClusterName := d.Get("aks_cluster_name").(string)

	mcClient := meta.(*clients.Client).ManagedClusters

	mcResp, err := mcClient.Get(ctx, aksResourceGroup, aksClusterName)
	if err != nil {
		return diag.Errorf("failed to check for presence of existing AKS Cluster (Cluster name %q) (Resource Group %q): %+v", aksClusterName, aksResourceGroup, err)
	}
	if mcResp.Response.StatusCode == 404 {
		// No AKS cluster exists, so returning an error stating as such
		return diag.Errorf("[ERROR] no AKS Cluster found for (Cluster name %q) (Resource Group %q).", aksClusterName, aksResourceGroup)
	}

	if err := d.Set("config", generateHelmConfig(
		managedAppName, consulConfig.Datacenter, *mcResp.Fqdn, consulConfig.RetryJoin)); err != nil {
		return diag.FromErr(err)
	}

	d.SetId(*app.ID + "/agent-helm-config")

	return nil
}

// generateHelmConfig will generate a helm config based on the passed in
// name, data center, retry join, and fqdn.
func generateHelmConfig(name, datacenter, fqdn string, retryJoin []string) string {
	// lowercase the name
	lower := strings.ToLower(name)

	// print retryJoin a double-quoted string safely escaped with Go syntax
	rj := fmt.Sprintf("%q", retryJoin)

	// replace any escaped double-quotes with single quotes
	// this is to match the format the the HCS CLI is outputting
	rj = strings.Replace(rj, "\"", "'", -1)

	return fmt.Sprintf(helmConfigTemplate,
		datacenter,
		lower, lower, lower,
		rj,
		fqdn,
		rj,
	)
}
