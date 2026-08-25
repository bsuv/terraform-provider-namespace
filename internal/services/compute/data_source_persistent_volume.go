package compute

import (
	"context"
	"fmt"

	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &persistentVolumeDataSource{}
	_ datasource.DataSourceWithConfigure = &persistentVolumeDataSource{}
)

// NewPersistentVolumeDataSource creates a new persistent volume data source.
func NewPersistentVolumeDataSource() datasource.DataSource {
	return &persistentVolumeDataSource{}
}

type persistentVolumeDataSource struct {
	client ServiceClient
}

// PersistentVolumeDataSourceModel defines the data source state model.
type PersistentVolumeDataSourceModel struct {
	ID             types.String `tfsdk:"id"`
	Tag            types.String `tfsdk:"tag"`
	Site           types.String `tfsdk:"site"`
	SizeMb         types.Int64  `tfsdk:"size_mb"`
	CreatedAt      types.String `tfsdk:"created_at"`
	DestroyedAt    types.String `tfsdk:"destroyed_at"`
	AttachedTo     types.String `tfsdk:"attached_to"`
	ReleasedAt     types.String `tfsdk:"released_at"`
	ReleasedReason types.String `tfsdk:"released_reason"`
}

func (d *persistentVolumeDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_persistent_volume"
}

func (d *persistentVolumeDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads details of a Namespace persistent volume by ID or unique tag.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Unique persistent volume ID. Exactly one of `id` or `tag` must be specified.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("tag")),
				},
			},
			"tag": schema.StringAttribute{
				Description: "Unique persistent volume tag. Exactly one of `id` or `tag` must be specified.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("tag")),
				},
			},
			"site": schema.StringAttribute{
				Description: "Optional site/region filter (e.g. 'zrh', 'ord', 'iad') to scope the volume query.",
				Optional:    true,
				Computed:    true,
			},
			"size_mb": schema.Int64Attribute{
				Description: "Volume size in megabytes.",
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
			"attached_to": schema.StringAttribute{
				Description: "Compute instance ID the volume is currently attached to, or null if detached.",
				Computed:    true,
			},
			"released_at": schema.StringAttribute{
				Description: "RFC3339 timestamp when the volume was released/detached.",
				Computed:    true,
			},
			"released_reason": schema.StringAttribute{
				Description: "Reason why the volume was released.",
				Computed:    true,
			},
		},
	}
}

func (d *persistentVolumeDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *persistentVolumeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config PersistentVolumeDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var unknownAttributes []string
	if config.ID.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "id")
	}
	if config.Tag.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "tag")
	}
	if config.Site.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "site")
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	id := config.ID.ValueString()
	tag := config.Tag.ValueString()
	site := config.Site.ValueString()

	if id == "" && tag == "" {
		resp.Diagnostics.AddError(
			"Missing Lookup Identifier",
			"Exactly one of `id` or `tag` must be specified for namespace_persistent_volume.",
		)
		return
	}
	if id != "" && tag != "" {
		resp.Diagnostics.AddError(
			"Conflicting Lookup Identifiers",
			"Only one of `id` or `tag` may be specified, not both.",
		)
		return
	}

	rpcResp, err := d.client.DescribePersistentVolume(ctx, &computev1beta.DescribePersistentVolumeRequest{
		Id:   id,
		Tag:  tag,
		Site: site,
	})
	if err != nil {
		if st, ok := status.FromError(err); ok && st.Code() == codes.NotFound {
			filterDesc := fmt.Sprintf("id=%q", id)
			if tag != "" {
				filterDesc = fmt.Sprintf("tag=%q", tag)
			}
			resp.Diagnostics.AddError(
				"Persistent Volume Not Found",
				fmt.Sprintf("No persistent volume found matching %s: %s", filterDesc, st.Message()),
			)
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_persistent_volume")
		return
	}

	vol := rpcResp.GetVolume()
	if vol == nil {
		filterDesc := fmt.Sprintf("id=%q", id)
		if tag != "" {
			filterDesc = fmt.Sprintf("tag=%q", tag)
		}
		resp.Diagnostics.AddError(
			"Persistent Volume Not Found",
			fmt.Sprintf("No persistent volume returned matching %s", filterDesc),
		)
		return
	}

	var state PersistentVolumeDataSourceModel
	state.ID = types.StringValue(vol.GetId())
	state.Tag = types.StringValue(vol.GetTag())
	if vol.GetSite() != "" {
		state.Site = types.StringValue(vol.GetSite())
	} else if !config.Site.IsNull() && !config.Site.IsUnknown() {
		state.Site = config.Site
	} else {
		state.Site = types.StringNull()
	}
	state.SizeMb = types.Int64Value(int64(vol.GetSizeMb()))
	state.CreatedAt = tfutil.TimestampToTFString(vol.GetCreatedAt())
	state.DestroyedAt = tfutil.TimestampToTFString(vol.GetDestroyedAt())

	if vol.GetAttachedTo() != "" {
		state.AttachedTo = types.StringValue(vol.GetAttachedTo())
	} else {
		state.AttachedTo = types.StringNull()
	}

	state.ReleasedAt = tfutil.TimestampToTFString(vol.GetReleasedAt())
	if vol.GetReleasedReason() != "" {
		state.ReleasedReason = types.StringValue(vol.GetReleasedReason())
	} else {
		state.ReleasedReason = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
