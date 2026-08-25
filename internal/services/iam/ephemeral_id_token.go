package iam

import (
	"context"
	"fmt"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ ephemeral.EphemeralResource              = &idTokenEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &idTokenEphemeralResource{}
)

// NewIDTokenEphemeralResource creates an ephemeral resource for namespace_id_token.
func NewIDTokenEphemeralResource() ephemeral.EphemeralResource {
	return &idTokenEphemeralResource{}
}

// NewIDTokenEphemeralResourceWithClient creates an ephemeral resource with a specified ServiceClient (for testing).
func NewIDTokenEphemeralResourceWithClient(cli ServiceClient) ephemeral.EphemeralResource {
	return &idTokenEphemeralResource{client: cli}
}

type idTokenEphemeralResource struct {
	client ServiceClient
}

type IDTokenEphemeralModel struct {
	Audience types.String `tfsdk:"audience"`
	IDToken  types.String `tfsdk:"id_token"`
}

func (e *idTokenEphemeralResource) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = "namespace_id_token"
}

func (e *idTokenEphemeralResource) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Issues a short-lived signed OIDC ID token for workload identity federation using Namespace Cloud tenant credentials. The token is available only in-memory during Terraform operations and is not persisted to state.",
		Attributes: map[string]schema.Attribute{
			"audience": schema.StringAttribute{
				Description: "Intended recipient audience URI or identifier for the issued OIDC ID token.",
				Required:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"id_token": schema.StringAttribute{
				Description: "Issued short-lived signed OIDC ID token.",
				Computed:    true,
				Sensitive:   true,
			},
		},
	}
}

func (e *idTokenEphemeralResource) Configure(ctx context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
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

func (e *idTokenEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data IDTokenEphemeralModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Deferral check: if audience is unknown and deferral is allowed, postpone to apply time
	if req.ClientCapabilities.DeferralAllowed {
		if data.Audience.IsUnknown() {
			resp.Deferred = &ephemeral.Deferred{
				Reason: ephemeral.DeferredReasonEphemeralResourceConfigUnknown,
			}
			return
		}
	} else if data.Audience.IsUnknown() {
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

	pbReq := &iamv1beta.IssueIdTokenRequest{
		Audience: data.Audience.ValueString(),
	}

	tokenResp, err := e.client.IssueIdToken(ctx, pbReq)
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "open", "namespace_id_token")
		return
	}

	data.IDToken = types.StringValue(tokenResp.IdToken)

	diags = resp.Result.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}
