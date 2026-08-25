package iam

import (
	"context"
	"fmt"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ ephemeral.EphemeralResource              = &clientCertificateEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &clientCertificateEphemeralResource{}
)

// NewClientCertificateEphemeralResource creates an ephemeral resource for namespace_client_certificate.
func NewClientCertificateEphemeralResource() ephemeral.EphemeralResource {
	return &clientCertificateEphemeralResource{}
}

// NewClientCertificateEphemeralResourceWithClient creates an ephemeral resource with a specified ServiceClient (for testing).
func NewClientCertificateEphemeralResourceWithClient(cli ServiceClient) ephemeral.EphemeralResource {
	return &clientCertificateEphemeralResource{client: cli}
}

type clientCertificateEphemeralResource struct {
	client ServiceClient
}

type ClientCertificateEphemeralModel struct {
	TenantID             types.String `tfsdk:"tenant_id"`
	ActorID              types.String `tfsdk:"actor_id"`
	DurationSecs         types.Int64  `tfsdk:"duration_secs"`
	PublicKeyPEM         types.String `tfsdk:"public_key_pem"`
	Policies             types.List   `tfsdk:"policies"`
	Access               types.Object `tfsdk:"access"`
	ClientCertificatePEM types.String `tfsdk:"client_certificate_pem"`
	PrivateKeyPEM        types.String `tfsdk:"private_key_pem"`
}

func (e *clientCertificateEphemeralResource) Metadata(ctx context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = "namespace_client_certificate"
}

func (e *clientCertificateEphemeralResource) Schema(ctx context.Context, req ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Issues an X.509 client certificate and private key using trusted authority JWT credentials. If a public key PEM is supplied by the user, only the signed certificate is returned and the private key output remains null. The issued certificate and key are available only in-memory during Terraform operations and are not persisted to state.",
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{
				Description: "Namespace tenant identifier for which the client certificate is issued.",
				Required:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"actor_id": schema.StringAttribute{
				Description: "Identity of the actor requesting the client certificate.",
				Required:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"duration_secs": schema.Int64Attribute{
				Description: "Validity duration of the issued certificate in seconds. If omitted, the Namespace API default is used.",
				Optional:    true,
				Validators: []validator.Int64{
					tfutil.Int64AtLeast(1),
				},
			},
			"public_key_pem": schema.StringAttribute{
				Description: "Optional PEM-encoded public key. If provided, the server issues a certificate matching this key and does not return a private key.",
				Optional:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"policies": schema.ListNestedAttribute{
				Description: "Optional list of token policies scoping permissions for the issued certificate.",
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
				Description: "Optional granular access grants scoping the certificate.",
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
			"client_certificate_pem": schema.StringAttribute{
				Description: "Issued PEM-encoded X.509 client certificate.",
				Computed:    true,
				Sensitive:   true,
			},
			"private_key_pem": schema.StringAttribute{
				Description: "Issued PEM-encoded private key. Null if public_key_pem was provided.",
				Computed:    true,
				Sensitive:   true,
			},
		},
	}
}

func (e *clientCertificateEphemeralResource) Configure(ctx context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
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

func (e *clientCertificateEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data ClientCertificateEphemeralModel
	diags := req.Config.Get(ctx, &data)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Deferral check: if any top-level or container input is unknown, postpone to apply time if deferral is allowed,
	// or return an actionable error diagnostic if deferral is unsupported.
	if data.TenantID.IsUnknown() || data.ActorID.IsUnknown() || data.DurationSecs.IsUnknown() || data.PublicKeyPEM.IsUnknown() || data.Policies.IsUnknown() || data.Access.IsUnknown() {
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

	pbReq := &iamv1beta.IssueTenantClientCertificateRequest{
		TenantId: data.TenantID.ValueString(),
		ActorId:  data.ActorID.ValueString(),
	}

	if !data.DurationSecs.IsNull() && !data.DurationSecs.IsUnknown() {
		pbReq.DurationSecs = data.DurationSecs.ValueInt64()
	}

	if !data.PublicKeyPEM.IsNull() && !data.PublicKeyPEM.IsUnknown() {
		pbReq.PublicKeyPem = data.PublicKeyPEM.ValueString()
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

	certResp, err := e.client.IssueTenantClientCertificate(ctx, pbReq)
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "open", "namespace_client_certificate")
		return
	}

	data.ClientCertificatePEM = types.StringValue(certResp.ClientCertificatePem)
	if certResp.PrivateKeyPem != "" {
		data.PrivateKeyPEM = types.StringValue(certResp.PrivateKeyPem)
	} else {
		data.PrivateKeyPEM = types.StringNull()
	}

	diags = resp.Result.Set(ctx, &data)
	resp.Diagnostics.Append(diags...)
}
