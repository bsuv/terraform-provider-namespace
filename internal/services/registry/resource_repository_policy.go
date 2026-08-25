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

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ resource.Resource                     = &repositoryPolicyResource{}
	_ resource.ResourceWithConfigure        = &repositoryPolicyResource{}
	_ resource.ResourceWithImportState      = &repositoryPolicyResource{}
	_ resource.ResourceWithConfigValidators = &repositoryPolicyResource{}
)

// NewRepositoryPolicyResource creates a new repository policy resource instance.
func NewRepositoryPolicyResource() resource.Resource {
	return &repositoryPolicyResource{}
}

type repositoryPolicyResource struct {
	client ServiceClient
}

type RepositoryPolicyResourceModel struct {
	ID                    types.String `tfsdk:"id"`
	Repository            types.String `tfsdk:"repository"`
	Revision              types.Int64  `tfsdk:"revision"`
	Expiration            types.String `tfsdk:"expiration"`
	ExpirationEnforcement types.String `tfsdk:"expiration_enforcement"`
}

func (r *repositoryPolicyResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "namespace_registry_repository_policy"
}

func (r *repositoryPolicyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a repository-specific container registry image retention and expiration policy override. Exclusive repository-level ownership semantics apply with last-writer-wins behavior for each repository. Destroying this resource does not restore any previous repository policy state; instead, it eliminates the repository override by resetting expiration = nil and expiration_enforcement = EXPIRATION_ENFORCEMENT_UNKNOWN on the server, verified by GetRepositoryPolicy returning NotFound, causing the repository to fall back to inheriting the workspace-wide default policy.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Matches the repository name.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"repository": schema.StringAttribute{
				Description: "Container repository name to apply this retention policy override to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"revision": schema.Int64Attribute{
				Description: "Monotonic revision number used for optimistic concurrency locking.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"expiration": schema.StringAttribute{
				Description: "Repository-level container image expiration duration (e.g. '24h', '720h'). Must be at least 4h.",
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

func (r *repositoryPolicyResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		repositoryPolicyConfigValidator{},
	}
}

type repositoryPolicyConfigValidator struct{}

func (v repositoryPolicyConfigValidator) Description(ctx context.Context) string {
	return "validates cross-field rules for repository policy override"
}

func (v repositoryPolicyConfigValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v repositoryPolicyConfigValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config RepositoryPolicyResourceModel
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
		if config.Expiration.IsNull() || config.Expiration.IsUnknown() || config.Expiration.ValueString() == "" {
			resp.Diagnostics.AddAttributeError(
				path.Root("expiration"),
				"Missing Required Expiration Duration",
				"Attribute 'expiration' is required when expiration_enforcement is 'EXPIRATION_ENFORCEMENT_ENFORCE'.",
			)
		}
	}
}

func (r *repositoryPolicyResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *repositoryPolicyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RepositoryPolicyResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	repoName := plan.Repository.ValueString()

	// Acquire current revision (if repository already has policy, otherwise starts at 0)
	var currentRevision int64
	currentPolicy, err := r.client.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{
		Repository: repoName,
	})
	if err != nil {
		if !client.IsNotFound(err) {
			client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_registry_repository_policy")
			return
		}
	} else if currentPolicy != nil {
		currentRevision = currentPolicy.Revision
	}

	var durPb *durationpb.Duration
	if !plan.Expiration.IsNull() && !plan.Expiration.IsUnknown() && plan.Expiration.ValueString() != "" {
		durPb, err = tfutil.ParseDuration(plan.Expiration.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid Duration", fmt.Sprintf("Failed to parse expiration: %v", err))
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

	_, err = r.client.UpdateRepositoryPolicy(ctx, &registryv1beta.UpdateRepositoryPolicyRequest{
		Repository:            repoName,
		Revision:              currentRevision,
		Expiration:            durPb,
		ExpirationEnforcement: enfEnum,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_registry_repository_policy")
		return
	}

	updatedPolicy, err := r.client.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{
		Repository: repoName,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_registry_repository_policy")
		return
	}

	state := mapRepositoryPolicyToModel(updatedPolicy, repoName, plan)
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

func (r *repositoryPolicyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RepositoryPolicyResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	repoName := state.Repository.ValueString()
	if repoName == "" {
		repoName = state.ID.ValueString()
	}

	policy, err := r.client.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{
		Repository: repoName,
	})
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_registry_repository_policy")
		return
	}

	newState := mapRepositoryPolicyToModel(policy, repoName, state)
	diags = resp.State.Set(ctx, &newState)
	resp.Diagnostics.Append(diags...)
}

func (r *repositoryPolicyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan RepositoryPolicyResourceModel
	var state RepositoryPolicyResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	diags = req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	repoName := state.Repository.ValueString()
	if repoName == "" {
		repoName = state.ID.ValueString()
	}

	var durPb *durationpb.Duration
	var err error
	if !plan.Expiration.IsNull() && !plan.Expiration.IsUnknown() && plan.Expiration.ValueString() != "" {
		durPb, err = tfutil.ParseDuration(plan.Expiration.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid Duration", fmt.Sprintf("Failed to parse expiration: %v", err))
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

	_, err = r.client.UpdateRepositoryPolicy(ctx, &registryv1beta.UpdateRepositoryPolicyRequest{
		Repository:            repoName,
		Revision:              state.Revision.ValueInt64(),
		Expiration:            durPb,
		ExpirationEnforcement: enfEnum,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "update", "namespace_registry_repository_policy")
		return
	}

	updatedPolicy, err := r.client.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{
		Repository: repoName,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "update", "namespace_registry_repository_policy")
		return
	}

	newState := mapRepositoryPolicyToModel(updatedPolicy, repoName, plan)
	diags = resp.State.Set(ctx, &newState)
	resp.Diagnostics.Append(diags...)
}

func (r *repositoryPolicyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RepositoryPolicyResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	repoName := state.Repository.ValueString()
	if repoName == "" {
		repoName = state.ID.ValueString()
	}

	currentPolicy, err := r.client.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{
		Repository: repoName,
	})
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "delete", "namespace_registry_repository_policy")
		return
	}

	_, err = r.client.UpdateRepositoryPolicy(ctx, &registryv1beta.UpdateRepositoryPolicyRequest{
		Repository:            repoName,
		Revision:              currentPolicy.Revision,
		Expiration:            nil,
		ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "delete", "namespace_registry_repository_policy")
		return
	}

	// Post-update verification read: MUST verify that repository policy override has been eliminated (returning NotFound)
	postPolicy, err := r.client.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{
		Repository: repoName,
	})
	if err == nil {
		resp.Diagnostics.AddError(
			"Registry Repository Policy Reset Verification Failed",
			fmt.Sprintf("Expected repository policy override to be deleted (returning NotFound), but policy still exists (repository=%q, revision=%d, expiration=%v, enforcement=%v)",
				repoName, postPolicy.Revision, postPolicy.Expiration, postPolicy.ExpirationEnforcement),
		)
		return
	}
	if !client.IsNotFound(err) {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "delete", "namespace_registry_repository_policy")
		return
	}
}

func (r *repositoryPolicyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importID := req.ID
	if importID == "" {
		resp.Diagnostics.AddError("Invalid Import ID", "Repository name must be provided to import a repository policy")
		return
	}

	policy, err := r.client.GetRepositoryPolicy(ctx, &registryv1beta.GetRepositoryPolicyRequest{
		Repository: importID,
	})
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("Resource Not Found", fmt.Sprintf("No repository policy found for repository %q", importID))
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_registry_repository_policy")
		return
	}

	state := mapRepositoryPolicyToModel(policy, importID, RepositoryPolicyResourceModel{})
	diags := resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

func mapRepositoryPolicyToModel(pb *registryv1beta.GetRepositoryPolicyResponse, repoName string, current RepositoryPolicyResourceModel) RepositoryPolicyResourceModel {
	var enfStr string
	switch pb.ExpirationEnforcement {
	case registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE:
		enfStr = "EXPIRATION_ENFORCEMENT_ENFORCE"
	case registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE:
		enfStr = "EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE"
	default:
		enfStr = pb.ExpirationEnforcement.String()
	}

	durStr := tfutil.DurationToTFStringPreserve(pb.Expiration, current.Expiration)

	return RepositoryPolicyResourceModel{
		ID:                    types.StringValue(repoName),
		Repository:            types.StringValue(repoName),
		Revision:              types.Int64Value(pb.Revision),
		Expiration:            durStr,
		ExpirationEnforcement: types.StringValue(enfStr),
	}
}
