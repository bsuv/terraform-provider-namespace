package registry

import (
	"context"
	"fmt"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ resource.Resource                     = &defaultPolicyResource{}
	_ resource.ResourceWithConfigure        = &defaultPolicyResource{}
	_ resource.ResourceWithImportState      = &defaultPolicyResource{}
	_ resource.ResourceWithConfigValidators = &defaultPolicyResource{}
)

// NewDefaultPolicyResource creates a new registry default policy singleton resource instance.
func NewDefaultPolicyResource() resource.Resource {
	return &defaultPolicyResource{}
}

type defaultPolicyResource struct {
	client ServiceClient
}

type DefaultPolicyResourceModel struct {
	ID                    types.String `tfsdk:"id"`
	Revision              types.Int64  `tfsdk:"revision"`
	DefaultExpiration     types.String `tfsdk:"default_expiration"`
	ExpirationEnforcement types.String `tfsdk:"expiration_enforcement"`
}

func (r *defaultPolicyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "namespace_registry_default_policy"
}

func (r *defaultPolicyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages the singleton workspace-wide container registry image expiration and retention policy. Exclusive ownership semantics apply with last-writer-wins behavior across configurations managing the singleton workspace default policy. Destroying this resource does not restore any previous policy state; instead, it performs a neutral reset on the server by setting default_expiration = nil (disabled) and expiration_enforcement = EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Fixed singleton identifier for the default policy ('default').",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"revision": schema.Int64Attribute{
				Description: "Monotonic revision number used for optimistic concurrency locking.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"default_expiration": schema.StringAttribute{
				Description: "Workspace-wide default container image expiration duration (e.g. '24h', '720h'). Must be at least 4h.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					tfutil.DurationAtLeast(4 * time.Hour),
				},
			},
			"expiration_enforcement": schema.StringAttribute{
				Description: "Image expiration enforcement mode (`EXPIRATION_ENFORCEMENT_ENFORCE` or `EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE`).",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("EXPIRATION_ENFORCEMENT_ENFORCE", "EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE"),
				},
			},
		},
	}
}

func (r *defaultPolicyResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		defaultPolicyConfigValidator{},
	}
}

type defaultPolicyConfigValidator struct{}

func (v defaultPolicyConfigValidator) Description(ctx context.Context) string {
	return "validates cross-field rules for registry default policy"
}

func (v defaultPolicyConfigValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v defaultPolicyConfigValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config DefaultPolicyResourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.ExpirationEnforcement.IsNull() || config.ExpirationEnforcement.IsUnknown() {
		return
	}

	enf := config.ExpirationEnforcement.ValueString()
	if enf == "EXPIRATION_ENFORCEMENT_ENFORCE" {
		if config.DefaultExpiration.IsNull() || config.DefaultExpiration.IsUnknown() || config.DefaultExpiration.ValueString() == "" {
			resp.Diagnostics.AddAttributeError(
				path.Root("default_expiration"),
				"Missing Required Expiration Duration",
				"Attribute 'default_expiration' is required when expiration_enforcement is 'EXPIRATION_ENFORCEMENT_ENFORCE'.",
			)
		}
	}
}

func (r *defaultPolicyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cli, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	r.client = NewDomainClient(cli)
}

func (r *defaultPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan DefaultPolicyResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Read current singleton policy to acquire current revision
	currentPolicy, err := r.client.GetDefaultPolicy(ctx, &emptypb.Empty{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_registry_default_policy")
		return
	}

	var durPb *durationpb.Duration
	if !plan.DefaultExpiration.IsNull() && !plan.DefaultExpiration.IsUnknown() && plan.DefaultExpiration.ValueString() != "" {
		durPb, err = tfutil.ParseDuration(plan.DefaultExpiration.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid Duration", fmt.Sprintf("Failed to parse default_expiration: %v", err))
			return
		}
	}

	var enfEnum registryv1beta.ExpirationEnforcement
	switch plan.ExpirationEnforcement.ValueString() {
	case "EXPIRATION_ENFORCEMENT_ENFORCE":
		enfEnum = registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE
	case "EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE":
		enfEnum = registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE
	default:
		resp.Diagnostics.AddError("Invalid Enforcement", fmt.Sprintf("Unsupported expiration_enforcement %q", plan.ExpirationEnforcement.ValueString()))
		return
	}

	_, err = r.client.UpdateDefaultPolicy(ctx, &registryv1beta.UpdateDefaultPolicyRequest{
		Revision:              currentPolicy.Revision,
		DefaultExpiration:     durPb,
		ExpirationEnforcement: enfEnum,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_registry_default_policy")
		return
	}

	updatedPolicy, err := r.client.GetDefaultPolicy(ctx, &emptypb.Empty{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_registry_default_policy")
		return
	}

	state := mapDefaultPolicyToModel(updatedPolicy, plan)
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

func (r *defaultPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state DefaultPolicyResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	policy, err := r.client.GetDefaultPolicy(ctx, &emptypb.Empty{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_registry_default_policy")
		return
	}

	newState := mapDefaultPolicyToModel(policy, state)
	diags = resp.State.Set(ctx, &newState)
	resp.Diagnostics.Append(diags...)
}

func (r *defaultPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan DefaultPolicyResourceModel
	var state DefaultPolicyResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	diags = req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var durPb *durationpb.Duration
	var err error
	if !plan.DefaultExpiration.IsNull() && !plan.DefaultExpiration.IsUnknown() && plan.DefaultExpiration.ValueString() != "" {
		durPb, err = tfutil.ParseDuration(plan.DefaultExpiration.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid Duration", fmt.Sprintf("Failed to parse default_expiration: %v", err))
			return
		}
	}

	var enfEnum registryv1beta.ExpirationEnforcement
	switch plan.ExpirationEnforcement.ValueString() {
	case "EXPIRATION_ENFORCEMENT_ENFORCE":
		enfEnum = registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE
	case "EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE":
		enfEnum = registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE
	default:
		resp.Diagnostics.AddError("Invalid Enforcement", fmt.Sprintf("Unsupported expiration_enforcement %q", plan.ExpirationEnforcement.ValueString()))
		return
	}

	// Use observed revision from state for optimistic concurrency
	_, err = r.client.UpdateDefaultPolicy(ctx, &registryv1beta.UpdateDefaultPolicyRequest{
		Revision:              state.Revision.ValueInt64(),
		DefaultExpiration:     durPb,
		ExpirationEnforcement: enfEnum,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "update", "namespace_registry_default_policy")
		return
	}

	updatedPolicy, err := r.client.GetDefaultPolicy(ctx, &emptypb.Empty{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "update", "namespace_registry_default_policy")
		return
	}

	newState := mapDefaultPolicyToModel(updatedPolicy, plan)
	diags = resp.State.Set(ctx, &newState)
	resp.Diagnostics.Append(diags...)
}

func (r *defaultPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state DefaultPolicyResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Neutral reset: read current revision, reset expiration to nil and DO_NOT_ENFORCE
	currentPolicy, err := r.client.GetDefaultPolicy(ctx, &emptypb.Empty{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "delete", "namespace_registry_default_policy")
		return
	}

	_, err = r.client.UpdateDefaultPolicy(ctx, &registryv1beta.UpdateDefaultPolicyRequest{
		Revision:              currentPolicy.Revision,
		DefaultExpiration:     nil,
		ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "delete", "namespace_registry_default_policy")
		return
	}

	// Post-update verification read: MUST verify that default policy has been neutral reset
	postPolicy, err := r.client.GetDefaultPolicy(ctx, &emptypb.Empty{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "delete", "namespace_registry_default_policy")
		return
	}

	if postPolicy.DefaultExpiration != nil ||
		postPolicy.ExpirationEnforcement != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE {
		resp.Diagnostics.AddError(
			"Registry Default Policy Reset Verification Failed",
			fmt.Sprintf("Expected default policy to be reset to no expiration and DO_NOT_ENFORCE, but server returned expiration=%v enforcement=%v",
				postPolicy.DefaultExpiration, postPolicy.ExpirationEnforcement),
		)
		return
	}
}

func (r *defaultPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != "default" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Default policy singleton only supports import with ID 'default', got %q", req.ID),
		)
		return
	}

	policy, err := r.client.GetDefaultPolicy(ctx, &emptypb.Empty{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_registry_default_policy")
		return
	}

	state := mapDefaultPolicyToModel(policy, DefaultPolicyResourceModel{})
	diags := resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

func mapDefaultPolicyToModel(pb *registryv1beta.GetDefaultPolicyResponse, current DefaultPolicyResourceModel) DefaultPolicyResourceModel {
	var enfStr string
	switch pb.ExpirationEnforcement {
	case registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE:
		enfStr = "EXPIRATION_ENFORCEMENT_ENFORCE"
	case registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE:
		enfStr = "EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE"
	default:
		enfStr = pb.ExpirationEnforcement.String()
	}

	durStr := tfutil.DurationToTFStringPreserve(pb.DefaultExpiration, current.DefaultExpiration)

	return DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Value(pb.Revision),
		DefaultExpiration:     durStr,
		ExpirationEnforcement: types.StringValue(enfStr),
	}
}
