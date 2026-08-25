package compute

import (
	"context"
	"fmt"
	"sort"
	"time"

	builderv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/builder/v1beta"
	stdtime "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib/time"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &builderUsageDataSource{}
	_ datasource.DataSourceWithConfigure = &builderUsageDataSource{}
)

// NewBuilderUsageDataSource creates a new builder usage data source.
func NewBuilderUsageDataSource() datasource.DataSource {
	return &builderUsageDataSource{}
}

type builderUsageDataSource struct {
	client ServiceClient
}

// BuilderUsageDataSourceModel defines the data source state model.
type BuilderUsageDataSourceModel struct {
	ID           types.String              `tfsdk:"id"`
	PeriodStart  types.String              `tfsdk:"period_start"`
	PeriodEnd    types.String              `tfsdk:"period_end"`
	Total        *BuilderUsageSummaryModel `tfsdk:"total"`
	PendingTotal *BuilderUsageSummaryModel `tfsdk:"pending_total"`
	PerDay       []BuilderUsagePerDayModel `tfsdk:"per_day"`
}

// BuilderUsageSummaryModel defines total or pending usage metrics.
type BuilderUsageSummaryModel struct {
	BuildCount       types.Int64 `tfsdk:"build_count"`
	BuildWallMinutes types.Int64 `tfsdk:"build_wall_minutes"`
	BuildUnitMinutes types.Int64 `tfsdk:"build_unit_minutes"`
}

// BuilderUsagePerDayModel defines daily usage metrics breakdown.
type BuilderUsagePerDayModel struct {
	Date                    types.String `tfsdk:"date"`
	BuildCount              types.Int64  `tfsdk:"build_count"`
	BuildWallMinutes        types.Int64  `tfsdk:"build_wall_minutes"`
	BuildUnitMinutes        types.Int64  `tfsdk:"build_unit_minutes"`
	PendingBuildCount       types.Int64  `tfsdk:"pending_build_count"`
	PendingBuildWallMinutes types.Int64  `tfsdk:"pending_build_wall_minutes"`
	PendingBuildUnitMinutes types.Int64  `tfsdk:"pending_build_unit_minutes"`
}

func (d *builderUsageDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_builder_usage"
}

func (d *builderUsageDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads remote builder compute usage, build wall minutes, unit minutes, and day-by-day metrics.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Identifier for this usage query in Terraform state.",
				Computed:    true,
			},
			"period_start": schema.StringAttribute{
				Description: "Start date for usage period in `YYYY-MM-DD` format.",
				Required:    true,
				Validators: []validator.String{
					tfutil.ValidISODate(),
				},
			},
			"period_end": schema.StringAttribute{
				Description: "End date for usage period in `YYYY-MM-DD` format (optional, defaults to current date).",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					tfutil.ValidISODate(),
				},
			},
			"total": schema.SingleNestedAttribute{
				Description: "Finalized total builder usage for the period.",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"build_count": schema.Int64Attribute{
						Description: "Total number of builds executed.",
						Computed:    true,
					},
					"build_wall_minutes": schema.Int64Attribute{
						Description: "Total elapsed wall minutes across all builds.",
						Computed:    true,
					},
					"build_unit_minutes": schema.Int64Attribute{
						Description: "Total compute unit minutes consumed.",
						Computed:    true,
					},
				},
			},
			"pending_total": schema.SingleNestedAttribute{
				Description: "Pending / unfinalized builder usage for the period.",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"build_count": schema.Int64Attribute{
						Description: "Pending build count.",
						Computed:    true,
					},
					"build_wall_minutes": schema.Int64Attribute{
						Description: "Pending elapsed wall minutes.",
						Computed:    true,
					},
					"build_unit_minutes": schema.Int64Attribute{
						Description: "Pending compute unit minutes.",
						Computed:    true,
					},
				},
			},
			"per_day": schema.ListNestedAttribute{
				Description: "Daily breakdown of builder usage, sorted chronologically.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"date": schema.StringAttribute{
							Description: "Calendar date in `YYYY-MM-DD` format.",
							Computed:    true,
						},
						"build_count": schema.Int64Attribute{
							Description: "Number of builds executed on this day.",
							Computed:    true,
						},
						"build_wall_minutes": schema.Int64Attribute{
							Description: "Elapsed wall minutes on this day.",
							Computed:    true,
						},
						"build_unit_minutes": schema.Int64Attribute{
							Description: "Compute unit minutes consumed on this day.",
							Computed:    true,
						},
						"pending_build_count": schema.Int64Attribute{
							Description: "Pending build count for this day.",
							Computed:    true,
						},
						"pending_build_wall_minutes": schema.Int64Attribute{
							Description: "Pending wall minutes for this day.",
							Computed:    true,
						},
						"pending_build_unit_minutes": schema.Int64Attribute{
							Description: "Pending unit minutes for this day.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *builderUsageDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *builderUsageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config BuilderUsageDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var unknownAttributes []string
	if config.PeriodStart.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "period_start")
	}
	if config.PeriodEnd.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "period_end")
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	startStr := config.PeriodStart.ValueString()
	if startStr == "" {
		resp.Diagnostics.AddError("Missing Required Parameter", "`period_start` is required.")
		return
	}

	startDate, err := time.Parse(tfutil.ISODateFormat, startStr)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Period Start Date",
			fmt.Sprintf("`period_start` %q must be in YYYY-MM-DD format: %s", startStr, err),
		)
		return
	}

	rpcReq := &builderv1beta.GetBuilderUsageRequest{
		PeriodStart: &stdtime.Date{
			Year:  int32(startDate.Year()),
			Month: int32(startDate.Month()),
			Day:   int32(startDate.Day()),
		},
	}

	endStr := config.PeriodEnd.ValueString()
	if endStr != "" {
		endDate, err := time.Parse(tfutil.ISODateFormat, endStr)
		if err != nil {
			resp.Diagnostics.AddError(
				"Invalid Period End Date",
				fmt.Sprintf("`period_end` %q must be in YYYY-MM-DD format: %s", endStr, err),
			)
			return
		}
		if endDate.Before(startDate) {
			resp.Diagnostics.AddError(
				"Invalid Period Range",
				fmt.Sprintf("period_end %q must be greater than or equal to period_start %q", endStr, startStr),
			)
			return
		}
		rpcReq.PeriodEnd = &stdtime.Date{
			Year:  int32(endDate.Year()),
			Month: int32(endDate.Month()),
			Day:   int32(endDate.Day()),
		}
	}

	rpcResp, err := d.client.GetBuilderUsage(ctx, rpcReq)
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_builder_usage")
		return
	}

	stateID := fmt.Sprintf("usage:%s", startStr)
	if endStr != "" {
		stateID = fmt.Sprintf("usage:%s:%s", startStr, endStr)
	}

	var state BuilderUsageDataSourceModel
	state.ID = types.StringValue(stateID)
	state.PeriodStart = types.StringValue(startStr)
	if endStr != "" {
		state.PeriodEnd = types.StringValue(endStr)
	} else {
		state.PeriodEnd = types.StringNull()
	}

	total := rpcResp.GetTotal()
	if total != nil {
		state.Total = &BuilderUsageSummaryModel{
			BuildCount:       types.Int64Value(total.GetBuildCount()),
			BuildWallMinutes: types.Int64Value(total.GetBuildWallMinutes()),
			BuildUnitMinutes: types.Int64Value(total.GetBuildUnitMinutes()),
		}
	} else {
		state.Total = nil
	}

	pendingTotal := rpcResp.GetPendingTotal()
	if pendingTotal != nil {
		state.PendingTotal = &BuilderUsageSummaryModel{
			BuildCount:       types.Int64Value(pendingTotal.GetBuildCount()),
			BuildWallMinutes: types.Int64Value(pendingTotal.GetBuildWallMinutes()),
			BuildUnitMinutes: types.Int64Value(pendingTotal.GetBuildUnitMinutes()),
		}
	} else {
		state.PendingTotal = nil
	}

	perDayList := make([]BuilderUsagePerDayModel, 0, len(rpcResp.GetPerDay()))
	for _, day := range rpcResp.GetPerDay() {
		dateProto := day.GetDate()
		dateStr := ""
		if dateProto != nil {
			dateStr = fmt.Sprintf("%04d-%02d-%02d", dateProto.GetYear(), dateProto.GetMonth(), dateProto.GetDay())
		}

		dayModel := BuilderUsagePerDayModel{
			Date:                    types.StringValue(dateStr),
			BuildCount:              types.Int64Value(0),
			BuildWallMinutes:        types.Int64Value(0),
			BuildUnitMinutes:        types.Int64Value(0),
			PendingBuildCount:       types.Int64Value(0),
			PendingBuildWallMinutes: types.Int64Value(0),
			PendingBuildUnitMinutes: types.Int64Value(0),
		}

		if usage := day.GetUsage(); usage != nil {
			dayModel.BuildCount = types.Int64Value(usage.GetBuildCount())
			dayModel.BuildWallMinutes = types.Int64Value(usage.GetBuildWallMinutes())
			dayModel.BuildUnitMinutes = types.Int64Value(usage.GetBuildUnitMinutes())
		}

		if pendingUsage := day.GetPendingUsage(); pendingUsage != nil {
			dayModel.PendingBuildCount = types.Int64Value(pendingUsage.GetBuildCount())
			dayModel.PendingBuildWallMinutes = types.Int64Value(pendingUsage.GetBuildWallMinutes())
			dayModel.PendingBuildUnitMinutes = types.Int64Value(pendingUsage.GetBuildUnitMinutes())
		}

		perDayList = append(perDayList, dayModel)
	}

	// Sort perDayList chronologically by date
	sort.Slice(perDayList, func(i, j int) bool {
		return perDayList[i].Date.ValueString() < perDayList[j].Date.ValueString()
	})

	state.PerDay = perDayList

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
