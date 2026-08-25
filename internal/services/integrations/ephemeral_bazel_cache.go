package integrations

import (
	"context"
	"fmt"

	bazelv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/bazel/v1beta"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ ephemeral.EphemeralResource              = &bazelCacheEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &bazelCacheEphemeralResource{}
)

// NewBazelCacheEphemeralResource creates an ephemeral resource for namespace_bazel_cache.
func NewBazelCacheEphemeralResource() ephemeral.EphemeralResource {
	return &bazelCacheEphemeralResource{}
}

// NewBazelCacheEphemeralResourceWithClient creates an ephemeral resource with a specified ServiceClient (for testing).
func NewBazelCacheEphemeralResourceWithClient(cli ServiceClient) ephemeral.EphemeralResource {
	return &bazelCacheEphemeralResource{client: cli}
}

type bazelCacheEphemeralResource struct {
	client ServiceClient
}

type BazelCacheEphemeralModel struct {
	Key                     types.String `tfsdk:"key"`
	Version                 types.Int64  `tfsdk:"version"`
	Site                    types.String `tfsdk:"site"`
	EnableRemoteAssetAPI    types.Bool   `tfsdk:"enable_remote_asset_api"`
	ExperimentalDirectMTLS  types.Bool   `tfsdk:"experimental_direct_mtls"`
	ExperimentalCacheName   types.String `tfsdk:"experimental_cache_name"`
	CacheEndpoint           types.String `tfsdk:"cache_endpoint"`
	HttpsMtlsCacheEndpoint  types.String `tfsdk:"https_mtls_cache_endpoint"`
	HttpsCacheEndpoint      types.String `tfsdk:"https_cache_endpoint"`
	ServerCAPEM             types.String `tfsdk:"server_ca_pem"`
	ClientCertPEM           types.String `tfsdk:"client_cert_pem"`
	ClientKeyPEM            types.String `tfsdk:"client_key_pem"`
	ExpiresAt               types.String `tfsdk:"expires_at"`
	CredentialHelperDomains types.List   `tfsdk:"credential_helper_domains"`
	BuildEventEndpoint      types.String `tfsdk:"build_event_endpoint"`
	RemoteAssetEndpoint     types.String `tfsdk:"remote_asset_endpoint"`
	UseWorkloadMTLS         types.Bool   `tfsdk:"use_workload_mtls"`
}

func (e *bazelCacheEphemeralResource) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = "namespace_bazel_cache"
}

func (e *bazelCacheEphemeralResource) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Ensures Bazel remote cache infrastructure and obtains short-lived mTLS credentials and endpoints. Warning: Opening this ephemeral resource invokes an Ensure RPC that may create or reuse durable server-side cache infrastructure. Terraform does not manage the lifecycle of the underlying cache and will not delete it when destroyed.",
		Attributes: map[string]schema.Attribute{
			"key": schema.StringAttribute{
				Description: "Cache access scope key: 'ci' or 'workstation'.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("ci", "workstation"),
				},
			},
			"version": schema.Int64Attribute{
				Description: "Optional cache protocol version.",
				Optional:    true,
			},
			"site": schema.StringAttribute{
				Description: "Optional site/region hint for cache placement.",
				Optional:    true,
			},
			"enable_remote_asset_api": schema.BoolAttribute{
				Description: "Enable remote asset API support on the cache endpoint.",
				Optional:    true,
			},
			"experimental_direct_mtls": schema.BoolAttribute{
				Description: "Enable direct mTLS connection mode.",
				Optional:    true,
			},
			"experimental_cache_name": schema.StringAttribute{
				Description: "Explicit cache namespace identifier.",
				Optional:    true,
			},
			"cache_endpoint": schema.StringAttribute{
				Description: "Primary Bazel remote cache endpoint (gRPC/mTLS).",
				Computed:    true,
			},
			"https_mtls_cache_endpoint": schema.StringAttribute{
				Description: "HTTPS mTLS cache endpoint.",
				Computed:    true,
			},
			"https_cache_endpoint": schema.StringAttribute{
				Description: "HTTPS cache endpoint.",
				Computed:    true,
			},
			"server_ca_pem": schema.StringAttribute{
				Description: "PEM-encoded server CA certificate.",
				Computed:    true,
			},
			"client_cert_pem": schema.StringAttribute{
				Description: "PEM-encoded client certificate for mTLS authentication.",
				Computed:    true,
				Sensitive:   true,
			},
			"client_key_pem": schema.StringAttribute{
				Description: "PEM-encoded private key for mTLS authentication.",
				Computed:    true,
				Sensitive:   true,
			},
			"expires_at": schema.StringAttribute{
				Description: "RFC3339 timestamp when the issued mTLS credentials expire.",
				Computed:    true,
			},
			"credential_helper_domains": schema.ListAttribute{
				Description: "Domains supported by the credential helper.",
				ElementType: types.StringType,
				Computed:    true,
			},
			"build_event_endpoint": schema.StringAttribute{
				Description: "Build Event Protocol (BES) streaming endpoint.",
				Computed:    true,
			},
			"remote_asset_endpoint": schema.StringAttribute{
				Description: "Remote Asset API endpoint.",
				Computed:    true,
			},
			"use_workload_mtls": schema.BoolAttribute{
				Description: "Whether workload identity mTLS is used.",
				Computed:    true,
			},
		},
	}
}

func (e *bazelCacheEphemeralResource) Configure(ctx context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
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

func (e *bazelCacheEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data BazelCacheEphemeralModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Deferral check: if any input is unknown and deferral is allowed, postpone to apply time
	if req.ClientCapabilities.DeferralAllowed {
		if data.Key.IsUnknown() || data.Version.IsUnknown() || data.Site.IsUnknown() ||
			data.EnableRemoteAssetAPI.IsUnknown() || data.ExperimentalDirectMTLS.IsUnknown() ||
			data.ExperimentalCacheName.IsUnknown() {
			resp.Deferred = &ephemeral.Deferred{
				Reason: ephemeral.DeferredReasonEphemeralResourceConfigUnknown,
			}
			return
		}
	} else if data.Key.IsUnknown() || data.Version.IsUnknown() || data.Site.IsUnknown() ||
		data.EnableRemoteAssetAPI.IsUnknown() || data.ExperimentalDirectMTLS.IsUnknown() ||
		data.ExperimentalCacheName.IsUnknown() {
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

	pbReq := &bazelv1beta.EnsureBazelCacheRequest{
		Key: data.Key.ValueString(),
	}

	if !data.Version.IsNull() && !data.Version.IsUnknown() {
		pbReq.Version = data.Version.ValueInt64()
	}
	if !data.Site.IsNull() && !data.Site.IsUnknown() {
		pbReq.Site = data.Site.ValueString()
	}
	if !data.EnableRemoteAssetAPI.IsNull() && !data.EnableRemoteAssetAPI.IsUnknown() {
		pbReq.EnableRemoteAssetApi = data.EnableRemoteAssetAPI.ValueBool()
	}
	if !data.ExperimentalDirectMTLS.IsNull() && !data.ExperimentalDirectMTLS.IsUnknown() {
		pbReq.ExperimentalDirectMtls = data.ExperimentalDirectMTLS.ValueBool()
	}
	if !data.ExperimentalCacheName.IsNull() && !data.ExperimentalCacheName.IsUnknown() {
		pbReq.ExperimentalCacheName = data.ExperimentalCacheName.ValueString()
	}

	cacheResp, err := e.client.EnsureBazelCache(ctx, pbReq)
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "open", "namespace_bazel_cache")
		return
	}

	data.CacheEndpoint = types.StringValue(cacheResp.CacheEndpoint)
	data.HttpsMtlsCacheEndpoint = types.StringValue(cacheResp.HttpsMtlsCacheEndpoint)
	data.HttpsCacheEndpoint = types.StringValue(cacheResp.HttpsCacheEndpoint)
	data.ServerCAPEM = types.StringValue(cacheResp.ServerCaPem)
	data.ClientCertPEM = types.StringValue(cacheResp.ClientCertPem)
	data.ClientKeyPEM = types.StringValue(cacheResp.ClientKeyPem)
	data.ExpiresAt = tfutil.TimestampToTFString(cacheResp.ExpiresAt)
	data.BuildEventEndpoint = types.StringValue(cacheResp.BuildEventEndpoint)
	data.RemoteAssetEndpoint = types.StringValue(cacheResp.RemoteAssetEndpoint)
	data.UseWorkloadMTLS = types.BoolValue(cacheResp.UseWorkloadMtls)

	if len(cacheResp.CredentialHelperDomains) > 0 {
		domainsList, d := types.ListValueFrom(ctx, types.StringType, cacheResp.CredentialHelperDomains)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		data.CredentialHelperDomains = domainsList
	} else {
		data.CredentialHelperDomains = types.ListNull(types.StringType)
	}

	diags = resp.Result.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}
