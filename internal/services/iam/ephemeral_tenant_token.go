package iam

import (
	"context"
	"fmt"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ ephemeral.EphemeralResource              = &tenantTokenEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &tenantTokenEphemeralResource{}

	TenantTokenPolicyAttrTypes = map[string]attr.Type{
		"policy_type": types.StringType,
		"value":       types.StringType,
	}
	TenantTokenPolicyObjectType = types.ObjectType{AttrTypes: TenantTokenPolicyAttrTypes}

	AccessGrantPermissionAttrTypes = map[string]attr.Type{
		"resource_type": types.StringType,
		"resource_id":   types.StringType,
		"actions":       types.ListType{ElemType: types.StringType},
	}
	AccessGrantPermissionObjectType = types.ObjectType{AttrTypes: AccessGrantPermissionAttrTypes}

	AccessPolicyAttrTypes = map[string]attr.Type{
		"grants": types.ListType{ElemType: AccessGrantPermissionObjectType},
	}
	AccessPolicyObjectType = types.ObjectType{AttrTypes: AccessPolicyAttrTypes}
)

// NewTenantTokenEphemeralResource creates an ephemeral resource for namespace_tenant_token.
func NewTenantTokenEphemeralResource() ephemeral.EphemeralResource {
	return &tenantTokenEphemeralResource{}
}

// NewTenantTokenEphemeralResourceWithClient creates an ephemeral resource with a specified ServiceClient (for testing).
func NewTenantTokenEphemeralResourceWithClient(cli ServiceClient) ephemeral.EphemeralResource {
	return &tenantTokenEphemeralResource{client: cli}
}

type tenantTokenEphemeralResource struct {
	client ServiceClient
}

type TenantTokenEphemeralModel struct {
	TenantID     types.String `tfsdk:"tenant_id"`
	ActorID      types.String `tfsdk:"actor_id"`
	DurationSecs types.Int64  `tfsdk:"duration_secs"`
	Policies     types.List   `tfsdk:"policies"`
	Access       types.Object `tfsdk:"access"`
	BearerToken  types.String `tfsdk:"bearer_token"`
}

type TenantTokenPolicyModel struct {
	PolicyType types.String `tfsdk:"policy_type"`
	Value      types.String `tfsdk:"value"`
}

type AccessPolicyModel struct {
	Grants types.List `tfsdk:"grants"`
}

type PermissionModel struct {
	ResourceType types.String `tfsdk:"resource_type"`
	ResourceID   types.String `tfsdk:"resource_id"`
	Actions      types.List   `tfsdk:"actions"`
}

func (e *tenantTokenEphemeralResource) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = "namespace_tenant_token"
}

func (e *tenantTokenEphemeralResource) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Issues a short-lived Namespace Cloud tenant bearer token using partner credentials. The token is available only in-memory during Terraform operations and is not persisted to state.",
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{
				Description: "Namespace tenant identifier for which the token is issued.",
				Required:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"actor_id": schema.StringAttribute{
				Description: "Identity of the actor requesting the token.",
				Required:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"duration_secs": schema.Int64Attribute{
				Description: "Validity duration of the issued token in seconds. If omitted, the Namespace API default (e.g. 15 minutes) is used.",
				Optional:    true,
				Validators: []validator.Int64{
					tfutil.Int64AtLeast(1),
				},
			},
			"policies": schema.ListNestedAttribute{
				Description: "Optional list of token policies scoping permissions for the issued token.",
				Optional:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"policy_type": schema.StringAttribute{
							Description: "Policy type identifier (e.g. 'namespace.cloud.iam.v1beta.AdminPolicy').",
							Required:    true,
							Validators: []validator.String{
								tfutil.NonEmptyString(),
							},
						},
						"value": schema.StringAttribute{
							Description: "JSON or protobuf serialized policy value.",
							Required:    true,
						},
					},
				},
			},
			"access": schema.SingleNestedAttribute{
				Description: "Optional granular access grants scoping the token.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"grants": schema.ListNestedAttribute{
						Description: "List of permission grants.",
						Required:    true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"resource_type": schema.StringAttribute{
									Description: "Resource type identifier.",
									Required:    true,
									Validators: []validator.String{
										tfutil.NonEmptyString(),
									},
								},
								"resource_id": schema.StringAttribute{
									Description: "Target resource identifier.",
									Required:    true,
								},
								"actions": schema.ListAttribute{
									Description: "List of allowed action strings.",
									ElementType: types.StringType,
									Required:    true,
								},
							},
						},
					},
				},
			},
			"bearer_token": schema.StringAttribute{
				Description: "Issued short-lived tenant bearer token.",
				Computed:    true,
				Sensitive:   true,
			},
		},
	}
}

func (e *tenantTokenEphemeralResource) Configure(ctx context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
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

func (e *tenantTokenEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data TenantTokenEphemeralModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Deferral check: if any top-level or container input is unknown, postpone to apply time if deferral is allowed,
	// or return an actionable error diagnostic if deferral is unsupported.
	if data.TenantID.IsUnknown() || data.ActorID.IsUnknown() || data.DurationSecs.IsUnknown() || data.Policies.IsUnknown() || data.Access.IsUnknown() {
		if req.ClientCapabilities.DeferralAllowed {
			resp.Deferred = &ephemeral.Deferred{
				Reason: ephemeral.DeferredReasonEphemeralResourceConfigUnknown,
			}
			return
		}
		resp.Diagnostics.AddError("Unknown Ephemeral Resource Configuration", "Configuration contains unknown values and cannot be resolved.")
		return
	}

	if e.client == nil {
		resp.Diagnostics.AddError(
			"Unconfigured Client",
			"The provider client was not configured before opening the ephemeral resource.",
		)
		return
	}

	pbReq := &iamv1beta.IssueTenantTokenRequest{
		TenantId: data.TenantID.ValueString(),
		ActorId:  data.ActorID.ValueString(),
	}

	if !data.DurationSecs.IsNull() && !data.DurationSecs.IsUnknown() {
		pbReq.DurationSecs = data.DurationSecs.ValueInt64()
	}

	if !data.Policies.IsNull() && !data.Policies.IsUnknown() {
		var policies []TenantTokenPolicyModel
		d := data.Policies.ElementsAs(ctx, &policies, false)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		for _, p := range policies {
			if p.PolicyType.IsUnknown() || p.Value.IsUnknown() {
				if req.ClientCapabilities.DeferralAllowed {
					resp.Deferred = &ephemeral.Deferred{
						Reason: ephemeral.DeferredReasonEphemeralResourceConfigUnknown,
					}
					return
				}
				resp.Diagnostics.AddError("Unknown Policy Configuration", "Policy configuration contains unknown values and cannot be resolved.")
				return
			}
			pbReq.Policies = append(pbReq.Policies, &iamv1beta.TenantTokenPolicy{
				PolicyType: p.PolicyType.ValueString(),
				Value:      p.Value.ValueString(),
			})
		}
	}

	if !data.Access.IsNull() && !data.Access.IsUnknown() {
		var accessModel AccessPolicyModel
		d := data.Access.As(ctx, &accessModel, basetypes.ObjectAsOptions{})
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}

		if accessModel.Grants.IsUnknown() {
			if req.ClientCapabilities.DeferralAllowed {
				resp.Deferred = &ephemeral.Deferred{
					Reason: ephemeral.DeferredReasonEphemeralResourceConfigUnknown,
				}
				return
			}
			resp.Diagnostics.AddError("Unknown Access Grant Configuration", "Access grant contains unknown values and cannot be resolved.")
			return
		}

		if !accessModel.Grants.IsNull() {
			var grants []PermissionModel
			d := accessModel.Grants.ElementsAs(ctx, &grants, false)
			resp.Diagnostics.Append(d...)
			if resp.Diagnostics.HasError() {
				return
			}

			accessPolicy := &iamv1beta.AccessPolicy{}
			for _, g := range grants {
				if g.ResourceType.IsUnknown() || g.ResourceID.IsUnknown() || g.Actions.IsUnknown() {
					if req.ClientCapabilities.DeferralAllowed {
						resp.Deferred = &ephemeral.Deferred{
							Reason: ephemeral.DeferredReasonEphemeralResourceConfigUnknown,
						}
						return
					}
					resp.Diagnostics.AddError("Unknown Access Grant Configuration", "Access grant contains unknown values and cannot be resolved.")
					return
				}

				var actions []string
				if !g.Actions.IsNull() {
					var actionVals []types.String
					d := g.Actions.ElementsAs(ctx, &actionVals, false)
					resp.Diagnostics.Append(d...)
					if resp.Diagnostics.HasError() {
						return
					}
					for _, act := range actionVals {
						if act.IsUnknown() {
							if req.ClientCapabilities.DeferralAllowed {
								resp.Deferred = &ephemeral.Deferred{
									Reason: ephemeral.DeferredReasonEphemeralResourceConfigUnknown,
								}
								return
							}
							resp.Diagnostics.AddError("Unknown Access Grant Configuration", "Access grant contains unknown values and cannot be resolved.")
							return
						}
						actions = append(actions, act.ValueString())
					}
				}

				accessPolicy.Grants = append(accessPolicy.Grants, &iamv1beta.Permission{
					ResourceType: g.ResourceType.ValueString(),
					ResourceId:   g.ResourceID.ValueString(),
					Actions:      actions,
				})
			}
			pbReq.Access = accessPolicy
		}
	}

	tokenResp, err := e.client.IssueTenantToken(ctx, pbReq)
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "open", "namespace_tenant_token")
		return
	}

	data.BearerToken = types.StringValue(tokenResp.BearerToken)

	diags = resp.Result.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}
