package integrations

import (
	"context"
	"fmt"

	httpcachev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/httpcache/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ ephemeral.EphemeralResource              = &httpCacheEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &httpCacheEphemeralResource{}
)

// NewHTTPCacheEphemeralResource creates an ephemeral resource for namespace_http_cache.
func NewHTTPCacheEphemeralResource() ephemeral.EphemeralResource {
	return &httpCacheEphemeralResource{}
}

// NewHTTPCacheEphemeralResourceWithClient creates an ephemeral resource with a specified ServiceClient (for testing).
func NewHTTPCacheEphemeralResourceWithClient(cli ServiceClient) ephemeral.EphemeralResource {
	return &httpCacheEphemeralResource{client: cli}
}

type httpCacheEphemeralResource struct {
	client ServiceClient
}

type HTTPCacheEphemeralModel struct {
	Name             types.String `tfsdk:"name"`
	Site             types.String `tfsdk:"site"`
	CacheEndpointURL types.String `tfsdk:"cache_endpoint_url"`
	Username         types.String `tfsdk:"username"`
	Password         types.String `tfsdk:"password"`
	ExpiresAt        types.String `tfsdk:"expires_at"`
}

func (e *httpCacheEphemeralResource) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = "namespace_http_cache"
}

func (e *httpCacheEphemeralResource) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Ensures generic HTTP remote build cache infrastructure and obtains short-lived basic-auth credentials and endpoint URL. Warning: Opening this ephemeral resource invokes an Ensure RPC that may create or reuse durable server-side cache infrastructure. Terraform does not manage the lifecycle of the underlying cache and will not delete it when destroyed.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Name identifier of the HTTP cache.",
				Required:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"site": schema.StringAttribute{
				Description: "Optional site/region hint for cache placement. Computed from API response if omitted.",
				Optional:    true,
				Computed:    true,
			},
			"cache_endpoint_url": schema.StringAttribute{
				Description: "HTTP(S) URL for remote HTTP build cache.",
				Computed:    true,
			},
			"username": schema.StringAttribute{
				Description: "HTTP Basic Auth username for cache access.",
				Computed:    true,
				Sensitive:   true,
			},
			"password": schema.StringAttribute{
				Description: "HTTP Basic Auth password for cache access.",
				Computed:    true,
				Sensitive:   true,
			},
			"expires_at": schema.StringAttribute{
				Description: "RFC3339 timestamp when the issued credentials expire.",
				Computed:    true,
			},
		},
	}
}

func (e *httpCacheEphemeralResource) Configure(ctx context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cl, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Ephemeral Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	e.client = NewDomainClient(cl)
}

func (e *httpCacheEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data HTTPCacheEphemeralModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Deferral check: if any input is unknown and deferral is allowed, postpone to apply time
	if req.ClientCapabilities.DeferralAllowed {
		if data.Name.IsUnknown() || data.Site.IsUnknown() {
			resp.Deferred = &ephemeral.Deferred{
				Reason: ephemeral.DeferredReasonEphemeralResourceConfigUnknown,
			}
			return
		}
	} else if data.Name.IsUnknown() || data.Site.IsUnknown() {
		resp.Diagnostics.AddError(
			"Unknown Ephemeral Resource Configuration",
			"Configuration contains unknown values and cannot be resolved.",
		)
		return
	}

	if e.client == nil {
		resp.Diagnostics.AddError(
			"Unconfigured Client",
			"The provider client was not configured before opening the ephemeral resource.",
		)
		return
	}

	pbReq := &httpcachev1beta.EnsureHttpCacheRequest{
		Name: data.Name.ValueString(),
	}

	if !data.Site.IsNull() && !data.Site.IsUnknown() {
		pbReq.Site = data.Site.ValueString()
	}

	cacheResp, err := e.client.EnsureHttpCache(ctx, pbReq)
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "open", "namespace_http_cache")
		return
	}

	data.CacheEndpointURL = types.StringValue(cacheResp.CacheEndpointUrl)
	data.Username = types.StringValue(cacheResp.Username)
	data.Password = types.StringValue(cacheResp.Password)
	data.ExpiresAt = tfutil.TimestampToTFString(cacheResp.ExpiresAt)
	data.Site = types.StringValue(cacheResp.Site)

	diags = resp.Result.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}
