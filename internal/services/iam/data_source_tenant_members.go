package iam

import (
	"context"
	"fmt"
	"sort"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

var (
	_ datasource.DataSource              = &tenantMembersDataSource{}
	_ datasource.DataSourceWithConfigure = &tenantMembersDataSource{}
)

// NewTenantMembersDataSource creates a new tenant members data source instance.
func NewTenantMembersDataSource() datasource.DataSource {
	return &tenantMembersDataSource{}
}

type tenantMembersDataSource struct {
	client ServiceClient
}

type TenantMembersDataSourceModel struct {
	ID      types.String        `tfsdk:"id"`
	Members []TenantMemberModel `tfsdk:"members"`
}

type TenantMemberModel struct {
	UserID    types.String `tfsdk:"user_id"`
	FirstName types.String `tfsdk:"first_name"`
	LastName  types.String `tfsdk:"last_name"`
	Email     types.String `tfsdk:"email"`
	Role      types.String `tfsdk:"role"`
}

func (d *tenantMembersDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_tenant_members"
}

func (d *tenantMembersDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Fetches all user members and their assigned roles within the current Namespace Cloud workspace tenant.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Constant identifier for the tenant members data source.",
				Computed:    true,
			},
			"members": schema.ListNestedAttribute{
				Description: "List of tenant members sorted deterministically by user_id.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"user_id": schema.StringAttribute{
							Description: "Opaque unique identifier for the user.",
							Computed:    true,
						},
						"first_name": schema.StringAttribute{
							Description: "User's first name.",
							Computed:    true,
						},
						"last_name": schema.StringAttribute{
							Description: "User's last name.",
							Computed:    true,
						},
						"email": schema.StringAttribute{
							Description: "User's email address.",
							Computed:    true,
						},
						"role": schema.StringAttribute{
							Description: "Assigned role (`TENANT_MEMBER_ROLE_OWNER`, `TENANT_MEMBER_ROLE_EDITOR`, `TENANT_MEMBER_ROLE_ADMIN`, `TENANT_MEMBER_ROLE_READER`, `TENANT_MEMBER_ROLE_ACCOUNTANT`, `TENANT_MEMBER_ROLE_UNSPECIFIED`).",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *tenantMembersDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *tenantMembersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	membersResp, err := d.client.ListTenantMembers(ctx, &iamv1beta.ListTenantMembersRequest{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_tenant_members")
		return
	}

	members := make([]TenantMemberModel, 0, len(membersResp.Members))
	for _, m := range membersResp.Members {
		members = append(members, TenantMemberModel{
			UserID:    types.StringValue(m.UserId),
			FirstName: types.StringValue(m.FirstName),
			LastName:  types.StringValue(m.LastName),
			Email:     types.StringValue(m.Email),
			Role:      types.StringValue(m.Role.String()),
		})
	}

	// Deterministically sort by user_id ascending
	sort.SliceStable(members, func(i, j int) bool {
		return members[i].UserID.ValueString() < members[j].UserID.ValueString()
	})

	state := TenantMembersDataSourceModel{
		ID:      types.StringValue("tenant_members"),
		Members: members,
	}

	diags := resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}
