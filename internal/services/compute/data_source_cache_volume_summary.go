package compute

import (
	"context"
	"fmt"
	"sort"

	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &cacheVolumeSummaryDataSource{}
	_ datasource.DataSourceWithConfigure = &cacheVolumeSummaryDataSource{}
)

// NewCacheVolumeSummaryDataSource creates a new cache volume summary data source.
func NewCacheVolumeSummaryDataSource() datasource.DataSource {
	return &cacheVolumeSummaryDataSource{}
}

type cacheVolumeSummaryDataSource struct {
	client ServiceClient
}

// CacheVolumeSummaryDataSourceModel defines the data source state model.
type CacheVolumeSummaryDataSourceModel struct {
	ID          types.String                 `tfsdk:"id"`
	Tag         types.String                 `tfsdk:"tag"`
	SizeMb      types.Int64                  `tfsdk:"size_mb"`
	CreatedAt   types.String                 `tfsdk:"created_at"`
	Generations []CacheVolumeGenerationModel `tfsdk:"generations"`
}

// CacheVolumeGenerationModel defines individual cache volume generation within a tag.
type CacheVolumeGenerationModel struct {
	ID                 types.String `tfsdk:"id"`
	Tag                types.String `tfsdk:"tag"`
	CreatedAt          types.String `tfsdk:"created_at"`
	DestroyedAt        types.String `tfsdk:"destroyed_at"`
	SizeMb             types.Int64  `tfsdk:"size_mb"`
	State              types.String `tfsdk:"state"`
	GenerationReason   types.String `tfsdk:"generation_reason"`
	AttachedTo         types.String `tfsdk:"attached_to"`
	AttachedAt         types.String `tfsdk:"attached_at"`
	DetachedAt         types.String `tfsdk:"detached_at"`
	UsedMbWhenAttached types.Int64  `tfsdk:"used_mb_when_attached"`
	UsedMbWhenDetached types.Int64  `tfsdk:"used_mb_when_detached"`
}

func (d *cacheVolumeSummaryDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_cache_volume_summary"
}

func (d *cacheVolumeSummaryDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads tag-level summary and generation metadata for Namespace cache volumes.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Identifier for the data source, matching the volume tag.",
				Computed:    true,
			},
			"tag": schema.StringAttribute{
				Description: "Volume tag to query.",
				Required:    true,
			},
			"size_mb": schema.Int64Attribute{
				Description: "Requested size of the volume tag in megabytes.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "RFC3339 timestamp of when the volume tag was first created.",
				Computed:    true,
			},
			"generations": schema.ListNestedAttribute{
				Description: "List of cache volume generations associated with this tag.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "Unique identifier for this cache volume generation.",
							Computed:    true,
						},
						"tag": schema.StringAttribute{
							Description: "Volume tag name.",
							Computed:    true,
						},
						"created_at": schema.StringAttribute{
							Description: "RFC3339 creation timestamp.",
							Computed:    true,
						},
						"destroyed_at": schema.StringAttribute{
							Description: "RFC3339 destruction timestamp, if destroyed.",
							Computed:    true,
						},
						"size_mb": schema.Int64Attribute{
							Description: "Volume size in megabytes.",
							Computed:    true,
						},
						"state": schema.StringAttribute{
							Description: "Volume lifecycle state (e.g. STATE_IN_USE, STATE_AT_REST, STATE_EXPIRED, STATE_ABANDONED).",
							Computed:    true,
						},
						"generation_reason": schema.StringAttribute{
							Description: "Reason this cache volume generation was started.",
							Computed:    true,
						},
						"attached_to": schema.StringAttribute{
							Description: "Compute instance ID the volume was attached to.",
							Computed:    true,
						},
						"attached_at": schema.StringAttribute{
							Description: "RFC3339 timestamp when attached.",
							Computed:    true,
						},
						"detached_at": schema.StringAttribute{
							Description: "RFC3339 timestamp when detached.",
							Computed:    true,
						},
						"used_mb_when_attached": schema.Int64Attribute{
							Description: "Data used on volume when attached in megabytes.",
							Computed:    true,
						},
						"used_mb_when_detached": schema.Int64Attribute{
							Description: "Data used on volume when detached in megabytes.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *cacheVolumeSummaryDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *cacheVolumeSummaryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config CacheVolumeSummaryDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	var unknownAttributes []string
	if config.Tag.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "tag")
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	tag := config.Tag.ValueString()
	if tag == "" {
		resp.Diagnostics.AddError("Missing Required Attribute", "Volume tag must not be empty.")
		return
	}

	rpcResp, err := d.client.GetVolumeTagSummary(ctx, &computev1beta.GetVolumeTagSummaryRequest{
		Tag: tag,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			resp.Diagnostics.AddError(
				"Cache Volume Tag Summary Not Found",
				fmt.Sprintf("No cache volume summary found for tag %q: %s", tag, st.Message()),
			)
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_cache_volume_summary")
		return
	}

	summary := rpcResp.GetSummary()
	if summary == nil {
		resp.Diagnostics.AddError(
			"Cache Volume Tag Summary Not Found",
			fmt.Sprintf("No cache volume summary returned for tag %q", tag),
		)
		return
	}

	var state CacheVolumeSummaryDataSourceModel
	state.ID = types.StringValue(summary.GetTag())
	state.Tag = types.StringValue(summary.GetTag())
	state.SizeMb = types.Int64Value(int64(summary.GetSizeMb()))
	state.CreatedAt = tfutil.TimestampToTFString(summary.GetCreatedAt())

	generations := make([]CacheVolumeGenerationModel, 0, len(summary.GetGenerations()))
	for _, gen := range summary.GetGenerations() {
		meta := gen.GetMetadata()
		att := gen.GetAttachment()

		genModel := CacheVolumeGenerationModel{
			ID:                 types.StringValue(gen.GetId()),
			Tag:                types.StringValue(gen.GetTag()),
			CreatedAt:          types.StringNull(),
			DestroyedAt:        types.StringNull(),
			SizeMb:             types.Int64Null(),
			State:              types.StringNull(),
			GenerationReason:   types.StringNull(),
			AttachedTo:         types.StringNull(),
			AttachedAt:         types.StringNull(),
			DetachedAt:         types.StringNull(),
			UsedMbWhenAttached: types.Int64Null(),
			UsedMbWhenDetached: types.Int64Null(),
		}

		if meta != nil {
			genModel.CreatedAt = tfutil.TimestampToTFString(meta.GetCreatedAt())
			genModel.DestroyedAt = tfutil.TimestampToTFString(meta.GetDestroyedAt())
			genModel.SizeMb = types.Int64Value(int64(meta.GetSizeMb()))
			genModel.State = types.StringValue(meta.GetState().String())
			genModel.GenerationReason = types.StringValue(meta.GetGenerationReason().String())
		}

		if att != nil {
			if att.GetAttachedTo() != "" {
				genModel.AttachedTo = types.StringValue(att.GetAttachedTo())
			}
			genModel.AttachedAt = tfutil.TimestampToTFString(att.GetAttachedAt())
			genModel.DetachedAt = tfutil.TimestampToTFString(att.GetDetachedAt())
			genModel.UsedMbWhenAttached = types.Int64Value(int64(att.GetUsedMbWhenAttached()))
			genModel.UsedMbWhenDetached = types.Int64Value(int64(att.GetUsedMbWhenDetached()))
		}

		generations = append(generations, genModel)
	}

	sort.Slice(generations, func(i, j int) bool {
		caI := generations[i].CreatedAt.ValueString()
		caJ := generations[j].CreatedAt.ValueString()
		if caI != caJ {
			return caI < caJ
		}
		return generations[i].ID.ValueString() < generations[j].ID.ValueString()
	})

	state.Generations = generations

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
