package registry

import (
	"context"
	"fmt"
	"sort"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &registryImageDataSource{}
	_ datasource.DataSourceWithConfigure = &registryImageDataSource{}
)

// NewRegistryImageDataSource creates a new registry image data source.
func NewRegistryImageDataSource() datasource.DataSource {
	return &registryImageDataSource{}
}

type registryImageDataSource struct {
	client ServiceClient
}

// RegistryImageDataSourceModel defines the data source state model.
type RegistryImageDataSourceModel struct {
	ID         types.String   `tfsdk:"id"`
	Repository types.String   `tfsdk:"repository"`
	Reference  types.String   `tfsdk:"reference"`
	Digest     types.String   `tfsdk:"digest"`
	CreatedAt  types.String   `tfsdk:"created_at"`
	ExpiresAt  types.String   `tfsdk:"expires_at"`
	SizeBytes  types.Int64    `tfsdk:"size_bytes"`
	Tags       []types.String `tfsdk:"tags"`
}

func (d *registryImageDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_registry_image"
}

func (d *registryImageDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads details and digest for a single container image in the Namespace registry by tag or sha256 digest.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Unique identifier formatted as `<repository>@<digest>`.",
				Computed:    true,
			},
			"repository": schema.StringAttribute{
				Description: "Name of the container registry repository.",
				Required:    true,
			},
			"reference": schema.StringAttribute{
				Description: "Tag (e.g. `v1.2.0`) or digest (e.g. `sha256:...`) reference for the image.",
				Required:    true,
			},
			"digest": schema.StringAttribute{
				Description: "Authoritative sha256 content digest for the image.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "RFC3339 creation timestamp.",
				Computed:    true,
			},
			"expires_at": schema.StringAttribute{
				Description: "RFC3339 expiration timestamp, if configured.",
				Computed:    true,
			},
			"size_bytes": schema.Int64Attribute{
				Description: "Total size of the image in bytes.",
				Computed:    true,
			},
			"tags": schema.ListAttribute{
				Description: "List of all tags pointing to this image digest, sorted alphabetically.",
				ElementType: types.StringType,
				Computed:    true,
			},
		},
	}
}

func (d *registryImageDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *registryImageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config RegistryImageDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var unknownAttributes []string
	if config.Repository.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "repository")
	}
	if config.Reference.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "reference")
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	repo := config.Repository.ValueString()
	ref := config.Reference.ValueString()

	if repo == "" || ref == "" {
		resp.Diagnostics.AddError(
			"Missing Required Attributes",
			"Both `repository` and `reference` must be specified for namespace_registry_image.",
		)
		return
	}

	rpcResp, err := d.client.GetImage(ctx, &registryv1beta.GetImageRequest{
		Repository: repo,
		Reference:  ref,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			resp.Diagnostics.AddError(
				"Registry Image Not Found",
				fmt.Sprintf("No registry image found for %s:%s: %s", repo, ref, st.Message()),
			)
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_registry_image")
		return
	}

	img := rpcResp.GetImage()
	if img == nil {
		resp.Diagnostics.AddError(
			"Registry Image Not Found",
			fmt.Sprintf("No image returned for repository %q and reference %q", repo, ref),
		)
		return
	}

	var state RegistryImageDataSourceModel
	state.ID = types.StringValue(fmt.Sprintf("%s@%s", img.GetRepository(), img.GetDigest()))
	state.Repository = types.StringValue(img.GetRepository())
	state.Reference = types.StringValue(ref)
	state.Digest = types.StringValue(img.GetDigest())
	state.CreatedAt = tfutil.TimestampToTFString(img.GetCreatedAt())
	state.ExpiresAt = tfutil.TimestampToTFString(img.GetExpiresAt())

	if sizes := img.GetSizes(); sizes != nil {
		state.SizeBytes = types.Int64Value(sizes.GetTotal())
	} else {
		state.SizeBytes = types.Int64Value(0)
	}

	rawTags := make([]string, len(img.GetTags()))
	copy(rawTags, img.GetTags())
	sort.Strings(rawTags)

	tagsList := make([]types.String, 0, len(rawTags))
	for _, t := range rawTags {
		tagsList = append(tagsList, types.StringValue(t))
	}
	state.Tags = tagsList

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
