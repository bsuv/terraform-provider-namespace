package registry

import (
	"context"
	"fmt"
	"sort"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"google.golang.org/protobuf/proto"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource                     = &registryImagesDataSource{}
	_ datasource.DataSourceWithConfigure        = &registryImagesDataSource{}
	_ datasource.DataSourceWithConfigValidators = &registryImagesDataSource{}
)

// ExpiresAtFilterAttrTypes defines the attribute types for the expires_at filter object.
var ExpiresAtFilterAttrTypes = map[string]attr.Type{
	"after":  types.StringType,
	"before": types.StringType,
}

// NewRegistryImagesDataSource creates a new registry images data source.
func NewRegistryImagesDataSource() datasource.DataSource {
	return &registryImagesDataSource{}
}

type registryImagesDataSource struct {
	client ServiceClient
}

// RegistryImagesDataSourceModel defines the data source state model.
type RegistryImagesDataSourceModel struct {
	ID             types.String              `tfsdk:"id"`
	Repository     types.String              `tfsdk:"repository"`
	DeletionFilter types.String              `tfsdk:"deletion_filter"`
	ExpiresAt      types.Object              `tfsdk:"expires_at"`
	Images         []RegistryImageEntryModel `tfsdk:"images"`
}

// RegistryImagesExpiresAtModel defines the optional expires_at timestamp range filter.
type RegistryImagesExpiresAtModel struct {
	After  types.String `tfsdk:"after"`
	Before types.String `tfsdk:"before"`
}

// RegistryImageEntryModel defines individual image entry in the repository list.
type RegistryImageEntryModel struct {
	Repository types.String   `tfsdk:"repository"`
	Digest     types.String   `tfsdk:"digest"`
	CreatedAt  types.String   `tfsdk:"created_at"`
	ExpiresAt  types.String   `tfsdk:"expires_at"`
	DeletedAt  types.String   `tfsdk:"deleted_at"`
	SizeBytes  types.Int64    `tfsdk:"size_bytes"`
	Labels     types.Map      `tfsdk:"labels"`
	Tags       []types.String `tfsdk:"tags"`
}

func (d *registryImagesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_registry_images"
}

func (d *registryImagesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists and paginates all images stored within a container registry repository.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Identifier for this image list query, matching the repository name.",
				Computed:    true,
			},
			"repository": schema.StringAttribute{
				Description: "Name of the container registry repository to list images from.",
				Required:    true,
			},
			"deletion_filter": schema.StringAttribute{
				Description: "Controls whether deleted images are included (`DELETION_FILTER_UNSPECIFIED`, `INCLUDE_DELETED`, `EXCLUDE_DELETED`).",
				Optional:    true,
				Validators: []validator.String{
					stringvalidator.OneOf(
						"DELETION_FILTER_UNSPECIFIED",
						"INCLUDE_DELETED",
						"EXCLUDE_DELETED",
					),
				},
			},
			"expires_at": schema.SingleNestedAttribute{
				Description: "Optional exclusive RFC3339 expiration timestamp range filter. When either bound is set, images that never expire are excluded.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"after": schema.StringAttribute{
						Description: "Include images expiring strictly after this RFC3339 timestamp.",
						Optional:    true,
						Validators: []validator.String{
							tfutil.ValidRFC3339(),
						},
					},
					"before": schema.StringAttribute{
						Description: "Include images expiring strictly before this RFC3339 timestamp.",
						Optional:    true,
						Validators: []validator.String{
							tfutil.ValidRFC3339(),
						},
					},
				},
			},
			"images": schema.ListNestedAttribute{
				Description: "List of container images in the repository, sorted deterministically.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"repository": schema.StringAttribute{
							Description: "Repository name.",
							Computed:    true,
						},
						"digest": schema.StringAttribute{
							Description: "Image sha256 digest.",
							Computed:    true,
						},
						"created_at": schema.StringAttribute{
							Description: "RFC3339 creation timestamp.",
							Computed:    true,
						},
						"expires_at": schema.StringAttribute{
							Description: "RFC3339 expiration timestamp, if set.",
							Computed:    true,
						},
						"deleted_at": schema.StringAttribute{
							Description: "RFC3339 deletion timestamp, if the image has been soft-deleted.",
							Computed:    true,
						},
						"size_bytes": schema.Int64Attribute{
							Description: "Total image size in bytes.",
							Computed:    true,
						},
						"labels": schema.MapAttribute{
							Description: "Key-value labels attached to the image.",
							ElementType: types.StringType,
							Computed:    true,
						},
						"tags": schema.ListAttribute{
							Description: "Tags associated with this image digest, sorted alphabetically.",
							ElementType: types.StringType,
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *registryImagesDataSource) ConfigValidators(ctx context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		registryImagesConfigValidator{},
	}
}

type registryImagesConfigValidator struct{}

func (v registryImagesConfigValidator) Description(ctx context.Context) string {
	return "validates that expires_at.after is earlier than expires_at.before when both are specified"
}

func (v registryImagesConfigValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v registryImagesConfigValidator) ValidateDataSource(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var config RegistryImagesDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.ExpiresAt.IsNull() || config.ExpiresAt.IsUnknown() {
		return
	}

	var expCfg RegistryImagesExpiresAtModel
	diags = config.ExpiresAt.As(ctx, &expCfg, basetypes.ObjectAsOptions{})
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if expCfg.After.IsNull() || expCfg.After.IsUnknown() || expCfg.Before.IsNull() || expCfg.Before.IsUnknown() {
		return
	}

	afterPb, err := tfutil.ParseTimestamp(expCfg.After.ValueString())
	if err != nil {
		return
	}
	beforePb, err := tfutil.ParseTimestamp(expCfg.Before.ValueString())
	if err != nil {
		return
	}
	if afterPb != nil && beforePb != nil && !afterPb.AsTime().Before(beforePb.AsTime()) {
		tfutil.AddAttributeErrorf(
			&resp.Diagnostics,
			path.Root("expires_at"),
			"Invalid Expiration Time Range",
			"Attribute expires_at.after (%s) must be earlier than expires_at.before (%s).",
			expCfg.After.ValueString(),
			expCfg.Before.ValueString(),
		)
	}
}

func (d *registryImagesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cli, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	d.client = NewDomainClient(cli)
}

func (d *registryImagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config RegistryImagesDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	var unknownAttributes []string
	if config.Repository.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "repository")
	}
	if config.DeletionFilter.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "deletion_filter")
	}
	if config.ExpiresAt.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "expires_at")
	} else if !config.ExpiresAt.IsNull() {
		var expCfg RegistryImagesExpiresAtModel
		diags = config.ExpiresAt.As(ctx, &expCfg, basetypes.ObjectAsOptions{})
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		if expCfg.After.IsUnknown() {
			unknownAttributes = append(unknownAttributes, "expires_at.after")
		}
		if expCfg.Before.IsUnknown() {
			unknownAttributes = append(unknownAttributes, "expires_at.before")
		}
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	repo := config.Repository.ValueString()
	if repo == "" {
		resp.Diagnostics.AddError(
			"Missing Required Attribute",
			"`repository` must be specified for namespace_registry_images.",
		)
		return
	}

	deletionFilter := registryv1beta.ListImagesRequest_DELETION_FILTER_UNSPECIFIED
	if !config.DeletionFilter.IsNull() && !config.DeletionFilter.IsUnknown() {
		rawFilter := config.DeletionFilter.ValueString()
		if rawFilter != "" {
			val, ok := registryv1beta.ListImagesRequest_DeletionFilter_value[rawFilter]
			if !ok {
				tfutil.AddAttributeErrorf(
					&resp.Diagnostics,
					path.Root("deletion_filter"),
					"Invalid Deletion Filter",
					"Attribute deletion_filter %q is not a supported ListImagesRequest.DeletionFilter.", rawFilter,
				)
				return
			}
			deletionFilter = registryv1beta.ListImagesRequest_DeletionFilter(val)
		}
	}

	var expiresAtRange *stdlib.TimestampRange
	if !config.ExpiresAt.IsNull() && !config.ExpiresAt.IsUnknown() {
		var expCfg RegistryImagesExpiresAtModel
		diags = config.ExpiresAt.As(ctx, &expCfg, basetypes.ObjectAsOptions{})
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		afterPb, err := tfutil.TFStringToTimestamp(expCfg.After)
		if err != nil {
			tfutil.AddAttributeErrorf(
				&resp.Diagnostics,
				path.Root("expires_at").AtName("after"),
				"Invalid RFC3339 Timestamp",
				"Attribute expires_at.after %s", err,
			)
			return
		}
		beforePb, err := tfutil.TFStringToTimestamp(expCfg.Before)
		if err != nil {
			tfutil.AddAttributeErrorf(
				&resp.Diagnostics,
				path.Root("expires_at").AtName("before"),
				"Invalid RFC3339 Timestamp",
				"Attribute expires_at.before %s", err,
			)
			return
		}
		if afterPb != nil && beforePb != nil && !afterPb.AsTime().Before(beforePb.AsTime()) {
			tfutil.AddAttributeErrorf(
				&resp.Diagnostics,
				path.Root("expires_at"),
				"Invalid Expiration Time Range",
				"Attribute expires_at.after (%s) must be earlier than expires_at.before (%s).",
				expCfg.After.ValueString(),
				expCfg.Before.ValueString(),
			)
			return
		}
		if afterPb != nil || beforePb != nil {
			expiresAtRange = &stdlib.TimestampRange{
				After:  afterPb,
				Before: beforePb,
			}
		}
	}

	fetcher := func(fetchCtx context.Context, pageToken string) ([]*registryv1beta.Image, string, error) {
		rpcReq := &registryv1beta.ListImagesRequest{
			MatchRepository: &stdlib.StringMatcher{
				Values: []string{repo},
				Op:     stdlib.StringMatcher_IS_ANY_OF,
			},
			DeletionFilter: deletionFilter,
		}
		if expiresAtRange != nil {
			rpcReq.ExpiresAt = proto.Clone(expiresAtRange).(*stdlib.TimestampRange)
		}
		if pageToken != "" {
			rpcReq.PaginationCursor = []byte(pageToken)
		}

		rpcResp, err := d.client.ListImages(fetchCtx, rpcReq)
		if err != nil {
			return nil, "", err
		}

		var nextCursor string
		if len(rpcResp.GetPaginationCursor()) > 0 {
			nextCursor = string(rpcResp.GetPaginationCursor())
		}

		return rpcResp.GetImages(), nextCursor, nil
	}

	images, err := tfutil.PaginateAll(ctx, fetcher)
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_registry_images")
		return
	}

	// Deterministic sorting: CreatedAt ascending, then Digest ascending
	sort.Slice(images, func(i, j int) bool {
		secI := images[i].GetCreatedAt().GetSeconds()
		secJ := images[j].GetCreatedAt().GetSeconds()
		if secI != secJ {
			return secI < secJ
		}
		nanosI := images[i].GetCreatedAt().GetNanos()
		nanosJ := images[j].GetCreatedAt().GetNanos()
		if nanosI != nanosJ {
			return nanosI < nanosJ
		}
		return images[i].GetDigest() < images[j].GetDigest()
	})

	var state RegistryImagesDataSourceModel
	state.ID = types.StringValue(repo)
	state.Repository = types.StringValue(repo)
	state.DeletionFilter = config.DeletionFilter
	state.ExpiresAt = config.ExpiresAt

	imageList := make([]RegistryImageEntryModel, 0, len(images))
	for _, img := range images {
		rawTags := make([]string, len(img.GetTags()))
		copy(rawTags, img.GetTags())
		sort.Strings(rawTags)

		tagsList := make([]types.String, 0, len(rawTags))
		for _, t := range rawTags {
			tagsList = append(tagsList, types.StringValue(t))
		}

		sizeBytes := int64(0)
		if sizes := img.GetSizes(); sizes != nil {
			sizeBytes = sizes.GetTotal()
		}

		labelsVal, labelDiags := flattenImageLabels(ctx, img.GetLabels())
		resp.Diagnostics.Append(labelDiags...)
		if resp.Diagnostics.HasError() {
			return
		}

		entry := RegistryImageEntryModel{
			Repository: types.StringValue(img.GetRepository()),
			Digest:     types.StringValue(img.GetDigest()),
			CreatedAt:  tfutil.TimestampToTFString(img.GetCreatedAt()),
			ExpiresAt:  tfutil.TimestampToTFString(img.GetExpiresAt()),
			DeletedAt:  tfutil.TimestampToTFString(img.GetDeletedAt()),
			SizeBytes:  types.Int64Value(sizeBytes),
			Labels:     labelsVal,
			Tags:       tagsList,
		}

		imageList = append(imageList, entry)
	}

	state.Images = imageList

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
