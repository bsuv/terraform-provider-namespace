package registry

import (
	"context"
	"fmt"
	"sort"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &registryImagesDataSource{}
	_ datasource.DataSourceWithConfigure = &registryImagesDataSource{}
)

// NewRegistryImagesDataSource creates a new registry images data source.
func NewRegistryImagesDataSource() datasource.DataSource {
	return &registryImagesDataSource{}
}

type registryImagesDataSource struct {
	client ServiceClient
}

// RegistryImagesDataSourceModel defines the data source state model.
type RegistryImagesDataSourceModel struct {
	ID         types.String              `tfsdk:"id"`
	Repository types.String              `tfsdk:"repository"`
	Images     []RegistryImageEntryModel `tfsdk:"images"`
}

// RegistryImageEntryModel defines individual image entry in the repository list.
type RegistryImageEntryModel struct {
	Repository types.String   `tfsdk:"repository"`
	Digest     types.String   `tfsdk:"digest"`
	CreatedAt  types.String   `tfsdk:"created_at"`
	ExpiresAt  types.String   `tfsdk:"expires_at"`
	SizeBytes  types.Int64    `tfsdk:"size_bytes"`
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
						"size_bytes": schema.Int64Attribute{
							Description: "Total image size in bytes.",
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

	fetcher := func(fetchCtx context.Context, pageToken string) ([]*registryv1beta.Image, string, error) {
		rpcReq := &registryv1beta.ListImagesRequest{
			MatchRepository: &stdlib.StringMatcher{
				Values: []string{repo},
				Op:     stdlib.StringMatcher_IS_ANY_OF,
			},
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

		entry := RegistryImageEntryModel{
			Repository: types.StringValue(img.GetRepository()),
			Digest:     types.StringValue(img.GetDigest()),
			CreatedAt:  tfutil.TimestampToTFString(img.GetCreatedAt()),
			ExpiresAt:  tfutil.TimestampToTFString(img.GetExpiresAt()),
			SizeBytes:  types.Int64Value(sizeBytes),
			Tags:       tagsList,
		}

		imageList = append(imageList, entry)
	}

	state.Images = imageList

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
