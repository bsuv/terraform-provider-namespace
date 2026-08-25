package provider

import (
	"context"
	"strings"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ provider.Provider                       = &NamespaceProvider{}
	_ provider.ProviderWithEphemeralResources = &NamespaceProvider{}
)

// NamespaceProvider represents the Terraform provider instance.
type NamespaceProvider struct {
	version    string
	testClient *client.Client
}

// NamespaceProviderModel maps provider configuration schema to Go types.
type NamespaceProviderModel struct {
	Token           types.String `tfsdk:"token"`
	TokenFile       types.String `tfsdk:"token_file"`
	GlobalEndpoint  types.String `tfsdk:"global_endpoint"`
	IAMEndpoint     types.String `tfsdk:"iam_endpoint"`
	ComputeEndpoint types.String `tfsdk:"compute_endpoint"`
}

// New returns a factory function for the Namespace provider.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &NamespaceProvider{
			version: version,
		}
	}
}

// NewWithClient returns a factory function with a pre-configured client (for testing).
func NewWithClient(version string, cl *client.Client) func() provider.Provider {
	return func() provider.Provider {
		return &NamespaceProvider{
			version:    version,
			testClient: cl,
		}
	}
}

func (p *NamespaceProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "namespace"
	resp.Version = p.version
}

func (p *NamespaceProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Namespace provider allows managing Namespace Cloud resources, including GitHub Actions runner profiles, IAM tenant groups, container registry policies, and shared container images.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				Description: "Namespace Cloud Bearer authentication token. Conflicts with `token_file`. Can also be specified via the `NSC_TOKEN` environment variable.",
				Optional:    true,
				Sensitive:   true,
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.MatchRelative().AtParent().AtName("token_file")),
					tfutil.NonEmptyString(),
				},
			},
			"token_file": schema.StringAttribute{
				Description: "Path to a JSON file containing Namespace Cloud credentials. Conflicts with `token`. Can also be specified via the `NSC_TOKEN_FILE` environment variable.",
				Optional:    true,
				Validators: []validator.String{
					stringvalidator.ConflictsWith(path.MatchRelative().AtParent().AtName("token")),
					tfutil.NonEmptyString(),
				},
			},
			"global_endpoint": schema.StringAttribute{
				Description: "Override URL for global services endpoint (default: `https://global.namespaceapis.com:443`).",
				Optional:    true,
				Validators: []validator.String{
					tfutil.ValidEndpoint(),
				},
			},
			"iam_endpoint": schema.StringAttribute{
				Description: "Override URL for IAM services endpoint (default: `https://iam.namespaceapis.com:443`).",
				Optional:    true,
				Validators: []validator.String{
					tfutil.ValidEndpoint(),
				},
			},
			"compute_endpoint": schema.StringAttribute{
				Description: "Regional compute endpoint URL (e.g. `https://eu.compute.namespaceapis.com:443`).",
				Optional:    true,
				Validators: []validator.String{
					tfutil.ValidEndpoint(),
				},
			},
		},
	}
}

func (p *NamespaceProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config NamespaceProviderModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	unknownConfiguration := false
	var unknownAttributes []string
	for _, attr := range []struct {
		name  string
		value types.String
	}{
		{name: "token", value: config.Token},
		{name: "token_file", value: config.TokenFile},
		{name: "global_endpoint", value: config.GlobalEndpoint},
		{name: "iam_endpoint", value: config.IAMEndpoint},
		{name: "compute_endpoint", value: config.ComputeEndpoint},
	} {
		if attr.value.IsUnknown() {
			unknownConfiguration = true
			unknownAttributes = append(unknownAttributes, attr.name)
		}
	}
	if unknownConfiguration {
		if req.ClientCapabilities.DeferralAllowed {
			resp.Deferred = &provider.Deferred{Reason: provider.DeferredReasonProviderConfigUnknown}
			return
		}
		for _, attrName := range unknownAttributes {
			resp.Diagnostics.AddAttributeError(
				path.Root(attrName),
				"Provider Configuration Is Unknown",
				"Namespace cannot initialize its API client until all provider configuration is known. Unknown attributes: "+strings.Join(unknownAttributes, ", ")+".",
			)
		}
		return
	}

	if p.testClient != nil {
		resp.DataSourceData = p.testClient
		resp.ResourceData = p.testClient
		resp.EphemeralResourceData = p.testClient
		return
	}

	cli, err := client.New(ctx, client.Config{
		Token:           config.Token.ValueString(),
		TokenFile:       config.TokenFile.ValueString(),
		GlobalEndpoint:  config.GlobalEndpoint.ValueString(),
		IAMEndpoint:     config.IAMEndpoint.ValueString(),
		ComputeEndpoint: config.ComputeEndpoint.ValueString(),
		Version:         p.version,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to Initialize Namespace Client", client.RedactSecrets(err.Error()))
		return
	}

	resp.DataSourceData = cli
	resp.ResourceData = cli
	resp.EphemeralResourceData = cli
}

func (p *NamespaceProvider) Resources(ctx context.Context) []func() resource.Resource {
	return getResources()
}

func (p *NamespaceProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return getDataSources()
}

func (p *NamespaceProvider) EphemeralResources(ctx context.Context) []func() ephemeral.EphemeralResource {
	return getEphemeralResources()
}
