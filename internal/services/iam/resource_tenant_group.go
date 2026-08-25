package iam

import (
	"context"
	"fmt"
	"sort"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ resource.Resource                = &tenantGroupResource{}
	_ resource.ResourceWithConfigure   = &tenantGroupResource{}
	_ resource.ResourceWithImportState = &tenantGroupResource{}
)

// NewTenantGroupResource creates a new tenant group resource instance.
func NewTenantGroupResource() resource.Resource {
	return &tenantGroupResource{}
}

type tenantGroupResource struct {
	client ServiceClient
}

var (
	TenantGroupPolicyAttrTypes = map[string]attr.Type{
		"policy": types.StringType,
		"value":  types.StringType,
	}
	TenantGroupPolicyObjectType = types.ObjectType{AttrTypes: TenantGroupPolicyAttrTypes}
)

type TenantGroupResourceModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	Policies          types.List   `tfsdk:"policies"`
	EnrolledTenantIDs types.List   `tfsdk:"enrolled_tenant_ids"`
}

type TenantGroupPolicyModel struct {
	Policy types.String `tfsdk:"policy"`
	Value  types.String `tfsdk:"value"`
}

func (r *tenantGroupResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "namespace_tenant_group"
}

func (r *tenantGroupResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Namespace Cloud IAM tenant group. Tenant groups bundle policy configurations applied across enrolled tenants. Exclusive ownership semantics apply: tenant group configuration uses a non-atomic Describe + Ensure lifecycle without optimistic concurrency revision locking, resulting in last-writer-wins behavior. To prevent race conditions and unintended configuration overwrites, tenant groups should be managed exclusively by Terraform without concurrent out-of-band writers. Namespace rejects deletion while tenants remain enrolled; unenroll every tenant before destroying the resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Stable identifier for the tenant group in Terraform state, matching the group name.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "Unique name of the tenant group. Changing this forces recreation.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"policies": schema.ListNestedAttribute{
				Description: "List of policy configurations attached to the tenant group.",
				Optional:    true,
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"policy": schema.StringAttribute{
							Description: "Fully qualified policy identifier type (e.g. `namespace.cloud.compute.v1beta.UsagePolicy`).",
							Required:    true,
							Validators: []validator.String{
								tfutil.NonEmptyString(),
							},
						},
						"value": schema.StringAttribute{
							Description: "JSON or string-serialized policy configuration payload.",
							Required:    true,
						},
					},
				},
			},
			"enrolled_tenant_ids": schema.ListAttribute{
				Description: "List of tenant IDs enrolled in this tenant group.",
				Computed:    true,
				ElementType: types.StringType,
			},
		},
	}
}

func (r *tenantGroupResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	cli, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	r.client = NewDomainClient(cli)
}

func (r *tenantGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan TenantGroupResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()

	// Preflight check: DescribeTenantGroup to detect collision before Ensure
	existing, err := r.client.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{
		Name: name,
	})
	if err == nil && existing != nil {
		resp.Diagnostics.AddError(
			"Tenant Group Already Exists",
			fmt.Sprintf("A tenant group named %q already exists remotely. To manage it with Terraform, run: terraform import namespace_tenant_group.%s %s", name, name, name),
		)
		return
	} else if err != nil && !client.IsNotFound(err) {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_tenant_group")
		return
	}

	// Build EnsureTenantGroupRequest with policies
	var pbPolicies []*iamv1beta.TenantGroupPolicy
	if !plan.Policies.IsNull() && !plan.Policies.IsUnknown() {
		var policyModels []TenantGroupPolicyModel
		d := plan.Policies.ElementsAs(ctx, &policyModels, false)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		pbPolicies = make([]*iamv1beta.TenantGroupPolicy, 0, len(policyModels))
		for _, p := range policyModels {
			pbPolicies = append(pbPolicies, &iamv1beta.TenantGroupPolicy{
				Policy: p.Policy.ValueString(),
				Value:  p.Value.ValueString(),
			})
		}
	}

	_, err = r.client.EnsureTenantGroup(ctx, &iamv1beta.EnsureTenantGroupRequest{
		Name:     name,
		Policies: pbPolicies,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_tenant_group")
		return
	}

	descResp, err := r.client.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{
		Name: name,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_tenant_group")
		return
	}

	state, d := mapTenantGroupResponseToModel(ctx, descResp, &plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tenantGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state TenantGroupResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := state.Name.ValueString()
	if name == "" {
		name = state.ID.ValueString()
	}

	descResp, err := r.client.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{
		Name: name,
	})
	if err != nil {
		if client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_tenant_group") {
			resp.State.RemoveResource(ctx)
			return
		}
		return
	}

	newState, d := mapTenantGroupResponseToModel(ctx, descResp, &state)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &newState)...)
}

func (r *tenantGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan TenantGroupResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := plan.Name.ValueString()
	if name == "" {
		name = plan.ID.ValueString()
	}

	var pbPolicies []*iamv1beta.TenantGroupPolicy
	if !plan.Policies.IsNull() && !plan.Policies.IsUnknown() {
		var policyModels []TenantGroupPolicyModel
		d := plan.Policies.ElementsAs(ctx, &policyModels, false)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		pbPolicies = make([]*iamv1beta.TenantGroupPolicy, 0, len(policyModels))
		for _, p := range policyModels {
			pbPolicies = append(pbPolicies, &iamv1beta.TenantGroupPolicy{
				Policy: p.Policy.ValueString(),
				Value:  p.Value.ValueString(),
			})
		}
	}

	_, err := r.client.EnsureTenantGroup(ctx, &iamv1beta.EnsureTenantGroupRequest{
		Name:     name,
		Policies: pbPolicies,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "update", "namespace_tenant_group")
		return
	}

	descResp, err := r.client.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{
		Name: name,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "update", "namespace_tenant_group")
		return
	}

	state, d := mapTenantGroupResponseToModel(ctx, descResp, &plan)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tenantGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state TenantGroupResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := state.Name.ValueString()
	if name == "" {
		name = state.ID.ValueString()
	}

	_, err := r.client.DeleteTenantGroup(ctx, &iamv1beta.TenantGroupReference{
		Name: name,
	})
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		if client.IsFailedPrecondition(err) {
			resp.Diagnostics.AddError(
				"Cannot Delete Tenant Group With Enrolled Tenants",
				client.RedactSecrets(fmt.Sprintf("Namespace rejected deletion of tenant group %q because a precondition was not satisfied: %v. Unenroll all tenants from the group, refresh, and retry destroy.", name, err)),
			)
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "delete", "namespace_tenant_group")
		return
	}
}

func (r *tenantGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importID := req.ID
	if importID == "" {
		resp.Diagnostics.AddError("Invalid Import ID", "Group name must be provided to import a tenant group")
		return
	}

	descResp, err := r.client.DescribeTenantGroup(ctx, &iamv1beta.TenantGroupReference{
		Name: importID,
	})
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("Resource Not Found", fmt.Sprintf("No tenant group found with name %q", importID))
			return
		}
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_tenant_group")
		return
	}

	state, d := mapTenantGroupResponseToModel(ctx, descResp)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func mapTenantGroupResponseToModel(ctx context.Context, resp *iamv1beta.DescribeTenantGroupResponse, current ...*TenantGroupResourceModel) (TenantGroupResourceModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	var plannedPolicies types.List
	if len(current) > 0 && current[0] != nil {
		plannedPolicies = current[0].Policies
	}

	var policyObjects []attr.Value
	if len(resp.Policies) > 0 {
		for _, p := range resp.Policies {
			obj, d := types.ObjectValue(TenantGroupPolicyAttrTypes, map[string]attr.Value{
				"policy": types.StringValue(p.Policy),
				"value":  types.StringValue(p.Value),
			})
			diags.Append(d...)
			policyObjects = append(policyObjects, obj)
		}
	}

	var policiesList types.List
	if len(policyObjects) > 0 {
		pList, d := types.ListValue(TenantGroupPolicyObjectType, policyObjects)
		diags.Append(d...)
		policiesList = pList
	} else if !plannedPolicies.IsNull() && !plannedPolicies.IsUnknown() && len(plannedPolicies.Elements()) == 0 {
		pList, d := types.ListValue(TenantGroupPolicyObjectType, []attr.Value{})
		diags.Append(d...)
		policiesList = pList
	} else {
		policiesList = types.ListNull(TenantGroupPolicyObjectType)
	}

	rawTenants := append([]string(nil), resp.EnrolledTenantIds...)
	sort.Strings(rawTenants)
	enrolledList, d := types.ListValueFrom(ctx, types.StringType, rawTenants)
	diags.Append(d...)
	if resp.EnrolledTenantIds == nil {
		enrolledList, _ = types.ListValueFrom(ctx, types.StringType, []string{})
	}

	return TenantGroupResourceModel{
		ID:                types.StringValue(resp.Name),
		Name:              types.StringValue(resp.Name),
		Policies:          policiesList,
		EnrolledTenantIDs: enrolledList,
	}, diags
}
