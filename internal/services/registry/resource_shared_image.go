package registry

import (
	"context"
	"fmt"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ resource.Resource              = &sharedImageResource{}
	_ resource.ResourceWithConfigure = &sharedImageResource{}
)

// NewSharedImageResource creates a new container registry shared image resource instance.
func NewSharedImageResource() resource.Resource {
	return &sharedImageResource{}
}

type sharedImageResource struct {
	client ServiceClient
}

type SharedImageResourceModel struct {
	ID               types.String `tfsdk:"id"`
	Repository       types.String `tfsdk:"repository"`
	Digest           types.String `tfsdk:"digest"`
	Visibility       types.String `tfsdk:"visibility"`
	ExpiresAt        types.String `tfsdk:"expires_at"`
	Suffix           types.String `tfsdk:"suffix"`
	SharedRepository types.String `tfsdk:"shared_repository"`
	SharedDigest     types.String `tfsdk:"shared_digest"`
	ImageRef         types.String `tfsdk:"image_ref"`
}

func (r *sharedImageResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "namespace_registry_shared_image"
}

func (r *sharedImageResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Namespace Cloud container image share reference. Creating this resource exposes a container image publicly or to partners via a dedicated share URL. Import is intentionally unsupported because the API does not return the original source repository needed to reconstruct configuration losslessly.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Unique server-assigned identifier for the shared image.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"repository": schema.StringAttribute{
				Description: "Source container image repository name. Changing this forces recreation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"digest": schema.StringAttribute{
				Description: "Source image manifest digest (e.g. `sha256:...`). Changing this forces recreation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"visibility": schema.StringAttribute{
				Description: "Access visibility (`PUBLIC` or `PARTNER`). Changing this forces recreation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf("PUBLIC", "PARTNER"),
				},
			},
			"expires_at": schema.StringAttribute{
				Description: "RFC3339Nano expiration timestamp after which the share is automatically revoked. Changing this forces recreation.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					tfutil.ValidRFC3339(),
				},
			},
			"suffix": schema.StringAttribute{
				Description: "Optional URL disambiguation suffix for the public pull reference. Changing this forces recreation.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"shared_repository": schema.StringAttribute{
				Description: "Server-assigned shared repository name.",
				Computed:    true,
			},
			"shared_digest": schema.StringAttribute{
				Description: "Server-assigned shared image digest.",
				Computed:    true,
			},
			"image_ref": schema.StringAttribute{
				Description: "Full public pull reference URL for the shared container image.",
				Computed:    true,
			},
		},
	}
}

func (r *sharedImageResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *sharedImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan SharedImageResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var visEnum registryv1beta.Visibility
	switch plan.Visibility.ValueString() {
	case "PUBLIC":
		visEnum = registryv1beta.Visibility_PUBLIC
	case "PARTNER":
		visEnum = registryv1beta.Visibility_PARTNER
	default:
		resp.Diagnostics.AddError("Invalid Visibility", fmt.Sprintf("Visibility must be 'PUBLIC' or 'PARTNER', got %q", plan.Visibility.ValueString()))
		return
	}

	expiresPb, err := tfutil.TFStringToTimestamp(plan.ExpiresAt)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Expiration Timestamp", fmt.Sprintf("Failed to parse expires_at: %v", err))
		return
	}

	suffix := ""
	if !plan.Suffix.IsNull() && !plan.Suffix.IsUnknown() {
		suffix = plan.Suffix.ValueString()
	}

	shareResp, err := r.client.ShareImage(ctx, &registryv1beta.ShareImageRequest{
		Repository: plan.Repository.ValueString(),
		Digest:     plan.Digest.ValueString(),
		Visibility: visEnum,
		ExpiresAt:  expiresPb,
		Suffix:     suffix,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_registry_shared_image")
		return
	}

	state := mapSharedImageToModel(shareResp, plan.Repository.ValueString(), plan.Digest.ValueString(), plan)
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

func (r *sharedImageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state SharedImageResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	targetID := state.ID.ValueString()
	listResp, err := r.client.ListSharedImages(ctx, &registryv1beta.ListSharedImagesRequest{})
	if err != nil {
		if client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_registry_shared_image") {
			resp.State.RemoveResource(ctx)
			return
		}
		return
	}

	var match *registryv1beta.SharedImage
	for _, img := range listResp.SharedImages {
		if img.Id == targetID {
			match = img
			break
		}
	}

	if match == nil {
		// Share expired or removed on server -> remove from state
		resp.State.RemoveResource(ctx)
		return
	}

	newState := mapSharedImageToModel(match, state.Repository.ValueString(), state.Digest.ValueString(), state)
	diags = resp.State.Set(ctx, &newState)
	resp.Diagnostics.Append(diags...)
}

func (r *sharedImageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// All attributes have RequiresReplace, but handle unexpected update cleanly
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"namespace_registry_shared_image does not support in-place updates. Changes require replacement.",
	)
}

func (r *sharedImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state SharedImageResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, err := r.client.UnshareImage(ctx, &registryv1beta.UnshareImageRequest{
		Id: state.ID.ValueString(),
	})
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "delete", "namespace_registry_shared_image")
		return
	}
}

func mapSharedImageToModel(pb *registryv1beta.SharedImage, sourceRepo, sourceDigest string, current SharedImageResourceModel) SharedImageResourceModel {
	var visStr string
	switch pb.Visibility {
	case registryv1beta.Visibility_PUBLIC:
		visStr = "PUBLIC"
	case registryv1beta.Visibility_PARTNER:
		visStr = "PARTNER"
	default:
		visStr = pb.Visibility.String()
	}

	suffixVal := types.StringValue(pb.Suffix)
	if pb.Suffix == "" {
		if !current.Suffix.IsNull() && !current.Suffix.IsUnknown() && current.Suffix.ValueString() == "" {
			suffixVal = types.StringValue("")
		} else {
			suffixVal = types.StringNull()
		}
	}

	expiresVal := tfutil.TimestampToTFStringPreserve(pb.ExpiresAt, current.ExpiresAt)

	return SharedImageResourceModel{
		ID:               types.StringValue(pb.Id),
		Repository:       types.StringValue(sourceRepo),
		Digest:           types.StringValue(sourceDigest),
		Visibility:       types.StringValue(visStr),
		ExpiresAt:        expiresVal,
		Suffix:           suffixVal,
		SharedRepository: types.StringValue(pb.SharedRepository),
		SharedDigest:     types.StringValue(pb.SharedDigest),
		ImageRef:         types.StringValue(pb.ImageRef),
	}
}
