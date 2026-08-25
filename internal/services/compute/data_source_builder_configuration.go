package compute

import (
	"context"
	"fmt"

	builderv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/builder/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &builderConfigurationDataSource{}
	_ datasource.DataSourceWithConfigure = &builderConfigurationDataSource{}
)

// NewBuilderConfigurationDataSource creates a new builder configuration data source.
func NewBuilderConfigurationDataSource() datasource.DataSource {
	return &builderConfigurationDataSource{}
}

type builderConfigurationDataSource struct {
	client ServiceClient
}

// BuilderConfigurationDataSourceModel defines the data source state model.
type BuilderConfigurationDataSourceModel struct {
	ID                   types.String           `tfsdk:"id"`
	Platform             types.String           `tfsdk:"platform"`
	BuilderName          types.String           `tfsdk:"builder_name"`
	BuildkitEndpoint     types.String           `tfsdk:"buildkit_endpoint"`
	FullBuildkitEndpoint types.String           `tfsdk:"full_buildkit_endpoint"`
	ServerCaPem          types.String           `tfsdk:"server_ca_pem"`
	CacheSizeMb          types.Int64            `tfsdk:"cache_size_mb"`
	Shape                *BuilderShapeDataModel `tfsdk:"shape"`
}

// BuilderShapeDataModel defines the instance compute shape of the builder.
type BuilderShapeDataModel struct {
	VirtualCpu      types.Int64  `tfsdk:"virtual_cpu"`
	MemoryMegabytes types.Int64  `tfsdk:"memory_megabytes"`
	MachineArch     types.String `tfsdk:"machine_arch"`
	OS              types.String `tfsdk:"os"`
}

func (d *builderConfigurationDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_builder_configuration"
}

func (d *builderConfigurationDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads remote builder placement, endpoints, and sizing without exposing credentials or pre-spawning instances.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Unique identifier for the builder configuration in Terraform state.",
				Computed:    true,
			},
			"platform": schema.StringAttribute{
				Description: "Target architecture platform (e.g. `linux/amd64`, `linux/arm64`).",
				Optional:    true,
				Computed:    true,
			},
			"builder_name": schema.StringAttribute{
				Description: "Specific remote builder target name.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"buildkit_endpoint": schema.StringAttribute{
				Description: "Host and port endpoint for connecting to BuildKit.",
				Computed:    true,
			},
			"full_buildkit_endpoint": schema.StringAttribute{
				Description: "Full URI with scheme and port for connecting to BuildKit.",
				Computed:    true,
			},
			"server_ca_pem": schema.StringAttribute{
				Description: "PEM-encoded server CA certificates for verifying the remote builder.",
				Computed:    true,
			},
			"cache_size_mb": schema.Int64Attribute{
				Description: "Size of the builder cache volume in megabytes.",
				Computed:    true,
			},
			"shape": schema.SingleNestedAttribute{
				Description: "Instance sizing and compute shape of the builder.",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"virtual_cpu": schema.Int64Attribute{
						Description: "Number of virtual CPUs.",
						Computed:    true,
					},
					"memory_megabytes": schema.Int64Attribute{
						Description: "RAM in megabytes.",
						Computed:    true,
					},
					"machine_arch": schema.StringAttribute{
						Description: "Architecture (`amd64`, `arm64`).",
						Computed:    true,
					},
					"os": schema.StringAttribute{
						Description: "OS family (`linux`, `macos`).",
						Computed:    true,
					},
				},
			},
		},
	}
}

func (d *builderConfigurationDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *builderConfigurationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config BuilderConfigurationDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var unknownAttributes []string
	if config.Platform.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "platform")
	}
	if config.BuilderName.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "builder_name")
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	platform := config.Platform.ValueString()
	builderName := config.BuilderName.ValueString()

	// CRITICAL SECURITY ENFORCEMENT:
	// 1. SkipBuilderPreSpawn must ALWAYS be true so read-only data sources do not spawn compute VMs.
	// 2. ReturnNewCredentials must ALWAYS be false so credentials/keys are not generated.
	rpcReq := &builderv1beta.GetBuilderConfigurationRequest{
		Platform:             platform,
		BuilderName:          builderName,
		SkipBuilderPreSpawn:  true,
		ReturnNewCredentials: false,
	}

	rpcResp, err := d.client.GetBuilderConfiguration(ctx, rpcReq)
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_builder_configuration")
		return
	}

	// Calculate a deterministic state ID
	stateID := "builder_config"
	if builderName != "" && platform != "" {
		stateID = fmt.Sprintf("%s:%s", builderName, platform)
	} else if builderName != "" {
		stateID = builderName
	} else if platform != "" {
		stateID = platform
	}

	var state BuilderConfigurationDataSourceModel
	state.ID = types.StringValue(stateID)

	if platform != "" {
		state.Platform = types.StringValue(platform)
	} else {
		state.Platform = types.StringNull()
	}

	if builderName != "" {
		state.BuilderName = types.StringValue(builderName)
	} else {
		state.BuilderName = types.StringNull()
	}

	state.BuildkitEndpoint = types.StringValue(rpcResp.GetBuildkitEndpoint())
	state.FullBuildkitEndpoint = types.StringValue(rpcResp.GetFullBuildkitEndpoint())
	state.ServerCaPem = types.StringValue(rpcResp.GetServerCaPem())
	state.CacheSizeMb = types.Int64Value(rpcResp.GetCacheSizeMb())

	shapeProto := rpcResp.GetShape()
	if shapeProto != nil {
		state.Shape = &BuilderShapeDataModel{
			VirtualCpu:      types.Int64Value(int64(shapeProto.GetVirtualCpu())),
			MemoryMegabytes: types.Int64Value(int64(shapeProto.GetMemoryMegabytes())),
			MachineArch:     types.StringValue(shapeProto.GetMachineArch()),
			OS:              types.StringValue(shapeProto.GetOs()),
		}
	} else {
		state.Shape = nil
	}

	// Defensive check: rpcResp.Credentials is ignored and intentionally NOT copied to state.

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
