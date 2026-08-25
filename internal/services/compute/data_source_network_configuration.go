package compute

import (
	"context"
	"fmt"
	"sort"

	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

var (
	_ datasource.DataSource              = &networkConfigurationDataSource{}
	_ datasource.DataSourceWithConfigure = &networkConfigurationDataSource{}
)

// NewNetworkConfigurationDataSource creates a new network configuration data source.
func NewNetworkConfigurationDataSource() datasource.DataSource {
	return &networkConfigurationDataSource{}
}

type networkConfigurationDataSource struct {
	client ServiceClient
}

// NetworkConfigurationDataSourceModel defines the data source state model.
type NetworkConfigurationDataSourceModel struct {
	ID          types.String             `tfsdk:"id"`
	EgressCidrs []NetworkEgressCIDRModel `tfsdk:"egress_cidrs"`
}

// NetworkEgressCIDRModel defines an egress CIDR block with kind and segment.
type NetworkEgressCIDRModel struct {
	Cidr    types.String `tfsdk:"cidr"`
	Kind    types.String `tfsdk:"kind"`
	Segment types.String `tfsdk:"segment"`
}

func (d *networkConfigurationDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_network_configuration"
}

func (d *networkConfigurationDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the effective workspace network routing and egress CIDR configuration.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Fixed identifier for the workspace network configuration in Terraform state (`network`).",
				Computed:    true,
			},
			"egress_cidrs": schema.ListNestedAttribute{
				Description: "List of possible source CIDR blocks used by compute instances in this workspace.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"cidr": schema.StringAttribute{
							Description: "Egress IP CIDR block (e.g. `198.51.100.0/24`).",
							Computed:    true,
						},
						"kind": schema.StringAttribute{
							Description: "CIDR allocation kind (`NAMESPACE_MANAGED`, `EXTERNAL_TRANSIT_PROVIDER`, `DEDICATED`, or `KIND_UNKNOWN`).",
							Computed:    true,
						},
						"segment": schema.StringAttribute{
							Description: "Dedicated network segment identifier, if applicable.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *networkConfigurationDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *networkConfigurationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	rpcResp, err := d.client.GetNetworkConfiguration(ctx, &computev1beta.GetNetworkConfigurationRequest{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_network_configuration")
		return
	}

	var state NetworkConfigurationDataSourceModel
	state.ID = types.StringValue("network")

	var cidrList []NetworkEgressCIDRModel
	if egress := rpcResp.GetEgress(); egress != nil {
		for _, c := range egress.GetEgressCidrs() {
			cidrModel := NetworkEgressCIDRModel{
				Cidr: types.StringValue(c.GetCidr()),
				Kind: types.StringValue(c.GetKind().String()),
			}
			if c.GetSegment() != "" {
				cidrModel.Segment = types.StringValue(c.GetSegment())
			} else {
				cidrModel.Segment = types.StringNull()
			}
			cidrList = append(cidrList, cidrModel)
		}
	}

	if cidrList == nil {
		cidrList = []NetworkEgressCIDRModel{}
	} else {
		// Deterministic sort: by CIDR string, then by Segment
		sort.Slice(cidrList, func(i, j int) bool {
			if cidrList[i].Cidr.ValueString() != cidrList[j].Cidr.ValueString() {
				return cidrList[i].Cidr.ValueString() < cidrList[j].Cidr.ValueString()
			}
			return cidrList[i].Segment.ValueString() < cidrList[j].Segment.ValueString()
		})
	}

	state.EgressCidrs = cidrList

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
