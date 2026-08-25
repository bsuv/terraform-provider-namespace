package iam

import (
	"context"
	"fmt"
	"sort"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &tenantGroupDataSource{}
	_ datasource.DataSourceWithConfigure = &tenantGroupDataSource{}
)

// NewTenantGroupDataSource creates a new tenant group data source instance.
func NewTenantGroupDataSource() datasource.DataSource {
	return &tenantGroupDataSource{}
}

type tenantGroupDataSource struct {
	client ServiceClient
}

type TenantGroupDataSourceModel struct {
	ID                types.String             `tfsdk:"id"`
	Name              types.String             `tfsdk:"name"`
	Policies          []TenantGroupPolicyModel `tfsdk:"policies"`
	EnrolledTenantIDs types.List               `tfsdk:"enrolled_tenant_ids"`
	Versions          []TenantGroupPolicyModel `tfsdk:"versions"`
}

func (d *tenantGroupDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_tenant_group"
}

func (d *tenantGroupDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Fetches details of an existing Namespace Cloud IAM tenant group by name.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Identifier matching the tenant group name.",
				Computed:    true,
			},
			"name": schema.StringAttribute{
				Description: "Name of the tenant group to query.",
				Required:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"policies": schema.ListNestedAttribute{
				Description: "List of active policies attached to the tenant group.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"policy": schema.StringAttribute{
							Description: "Fully qualified policy identifier type.",
							Computed:    true,
						},
						"value": schema.StringAttribute{
							Description: "Policy configuration value.",
							Computed:    true,
						},
					},
				},
			},
			"enrolled_tenant_ids": schema.ListAttribute{
				Description: "List of tenant IDs enrolled in this tenant group.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"versions": schema.ListNestedAttribute{
				Description: "Historical policy versions configured for this tenant group.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"policy": schema.StringAttribute{
							Description: "Policy identifier type.",
							Computed:    true,
						},
						"value": schema.StringAttribute{
							Description: "Policy configuration value.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *tenantGroupDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *tenantGroupDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config TenantGroupDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	var unknownAttributes []string
	if config.Name.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "name")
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	name := config.Name.ValueString()
	descResp, err := d.client.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{
		Name: name,
	})
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("Tenant Group Not Found", fmt.Sprintf("No tenant group found with name %q", name))
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_tenant_group")
		return
	}

	rawPolicies := append([]*iamv1beta.TenantGroupPolicy(nil), descResp.Policies...)
	sort.Slice(rawPolicies, func(i, j int) bool {
		if rawPolicies[i].Policy != rawPolicies[j].Policy {
			return rawPolicies[i].Policy < rawPolicies[j].Policy
		}
		return rawPolicies[i].Value < rawPolicies[j].Value
	})
	policies := make([]TenantGroupPolicyModel, 0, len(rawPolicies))
	for _, p := range rawPolicies {
		policies = append(policies, TenantGroupPolicyModel{
			Policy: types.StringValue(p.Policy),
			Value:  types.StringValue(p.Value),
		})
	}

	rawVersions := append([]*iamv1beta.TenantGroupPolicy(nil), descResp.Versions...)
	sort.Slice(rawVersions, func(i, j int) bool {
		if rawVersions[i].Policy != rawVersions[j].Policy {
			return rawVersions[i].Policy < rawVersions[j].Policy
		}
		return rawVersions[i].Value < rawVersions[j].Value
	})
	versions := make([]TenantGroupPolicyModel, 0, len(rawVersions))
	for _, v := range rawVersions {
		versions = append(versions, TenantGroupPolicyModel{
			Policy: types.StringValue(v.Policy),
			Value:  types.StringValue(v.Value),
		})
	}

	rawTenants := append([]string(nil), descResp.EnrolledTenantIds...)
	sort.Strings(rawTenants)
	enrolledList, _ := types.ListValueFrom(ctx, types.StringType, rawTenants)
	if descResp.EnrolledTenantIds == nil {
		enrolledList, _ = types.ListValueFrom(ctx, types.StringType, []string{})
	}

	state := TenantGroupDataSourceModel{
		ID:                types.StringValue(descResp.Name),
		Name:              types.StringValue(descResp.Name),
		Policies:          policies,
		EnrolledTenantIDs: enrolledList,
		Versions:          versions,
	}

	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}
