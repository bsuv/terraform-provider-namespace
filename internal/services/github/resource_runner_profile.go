package github

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ resource.Resource                     = &runnerProfileResource{}
	_ resource.ResourceWithConfigure        = &runnerProfileResource{}
	_ resource.ResourceWithImportState      = &runnerProfileResource{}
	_ resource.ResourceWithConfigValidators = &runnerProfileResource{}
)

// NewRunnerProfileResource creates a new runner profile resource instance.
func NewRunnerProfileResource() resource.Resource {
	return &runnerProfileResource{}
}

type runnerProfileResource struct {
	client ServiceClient
}

var (
	LabelAttrTypes = map[string]attr.Type{
		"name":  types.StringType,
		"value": types.StringType,
	}
	LabelObjectType = types.ObjectType{AttrTypes: LabelAttrTypes}

	InstanceShapeAttrTypes = map[string]attr.Type{
		"virtual_cpu":      types.Int64Type,
		"memory_megabytes": types.Int64Type,
		"machine_arch":     types.StringType,
		"os":               types.StringType,
		"selectors":        types.ListType{ElemType: LabelObjectType},
	}
	InstanceShapeObjectType = types.ObjectType{AttrTypes: InstanceShapeAttrTypes}

	CacheVolumeSettingAttrTypes = map[string]attr.Type{
		"volume_tag":                   types.StringType,
		"volume_size_gb":               types.Int64Type,
		"volume_reserved_size_gb":      types.Int64Type,
		"bundled_container_cache":      types.BoolType,
		"bundled_buildkit_cache":       types.BoolType,
		"bundled_git_mirror":           types.BoolType,
		"bundled_runner_tools_cache":   types.BoolType,
		"bundled_runner_actions_cache": types.BoolType,
		"allow_commit_from_branch":     types.ListType{ElemType: types.StringType},
	}
	CacheVolumeSettingObjectType = types.ObjectType{AttrTypes: CacheVolumeSettingAttrTypes}

	UbuntuSpecAttrTypes = map[string]attr.Type{
		"apt_package": types.ListType{ElemType: types.StringType},
	}
	UbuntuSpecObjectType = types.ObjectType{AttrTypes: UbuntuSpecAttrTypes}

	DockerSpecAttrTypes = map[string]attr.Type{
		"dockerfile_content": types.StringType,
	}
	DockerSpecObjectType = types.ObjectType{AttrTypes: DockerSpecAttrTypes}

	CustomRunnerImageSpecAttrTypes = map[string]attr.Type{
		"ubuntu_spec": UbuntuSpecObjectType,
		"docker_spec": DockerSpecObjectType,
	}
	CustomRunnerImageSpecObjectType = types.ObjectType{AttrTypes: CustomRunnerImageSpecAttrTypes}

	NetworkPolicyAttrTypes = map[string]attr.Type{
		"egress":                   types.StringType,
		"egress_domain_allow_list": types.ListType{ElemType: types.StringType},
		"advisory":                 types.BoolType,
		"egress_policy_tag":        types.StringType,
	}
	NetworkPolicyObjectType = types.ObjectType{AttrTypes: NetworkPolicyAttrTypes}

	CustomRunnerImageStatusAttrTypes = map[string]attr.Type{
		"image_ref":        types.StringType,
		"status":           types.StringType,
		"last_build_error": types.StringType,
		"started_build_at": types.StringType,
		"built_at":         types.StringType,
		"last_error_at":    types.StringType,
		"build_ref":        types.StringType,
	}
	CustomRunnerImageStatusObjectType = types.ObjectType{AttrTypes: CustomRunnerImageStatusAttrTypes}
)

type RunnerProfileResourceModel struct {
	ID                    types.String `tfsdk:"id"`
	ProfileID             types.String `tfsdk:"profile_id"`
	Version               types.Int64  `tfsdk:"version"`
	CreatedAt             types.String `tfsdk:"created_at"`
	UpdatedAt             types.String `tfsdk:"updated_at"`
	Tag                   types.String `tfsdk:"tag"`
	Description           types.String `tfsdk:"description"`
	OS                    types.String `tfsdk:"os"`
	Emoji                 types.String `tfsdk:"emoji"`
	SwapSizeMB            types.Int64  `tfsdk:"swap_size_mb"`
	BuilderMode           types.String `tfsdk:"builder_mode"`
	AccessLevel           types.String `tfsdk:"access_level"`
	ExperimentalFeatures  types.List   `tfsdk:"experimental_features"`
	InstanceShape         types.Object `tfsdk:"instance_shape"`
	CacheVolumeSettings   types.List   `tfsdk:"cache_volume_settings"`
	CustomRunnerImageSpec types.Object `tfsdk:"custom_runner_image_spec"`
	NetworkPolicy         types.Object `tfsdk:"network_policy"`
	CustomRunnerImages    types.List   `tfsdk:"custom_runner_images"`
}

type InstanceShapeModel struct {
	VirtualCPU      types.Int64  `tfsdk:"virtual_cpu"`
	MemoryMegabytes types.Int64  `tfsdk:"memory_megabytes"`
	MachineArch     types.String `tfsdk:"machine_arch"`
	OS              types.String `tfsdk:"os"`
	Selectors       types.List   `tfsdk:"selectors"`
}

type LabelModel struct {
	Name  types.String `tfsdk:"name"`
	Value types.String `tfsdk:"value"`
}

type CacheVolumeSettingModel struct {
	VolumeTag                 types.String `tfsdk:"volume_tag"`
	VolumeSizeGB              types.Int64  `tfsdk:"volume_size_gb"`
	VolumeReservedSizeGB      types.Int64  `tfsdk:"volume_reserved_size_gb"`
	BundledContainerCache     types.Bool   `tfsdk:"bundled_container_cache"`
	BundledBuildkitCache      types.Bool   `tfsdk:"bundled_buildkit_cache"`
	BundledGitMirror          types.Bool   `tfsdk:"bundled_git_mirror"`
	BundledRunnerToolsCache   types.Bool   `tfsdk:"bundled_runner_tools_cache"`
	BundledRunnerActionsCache types.Bool   `tfsdk:"bundled_runner_actions_cache"`
	AllowCommitFromBranch     types.List   `tfsdk:"allow_commit_from_branch"`
}

type CustomRunnerImageSpecModel struct {
	UbuntuSpec types.Object `tfsdk:"ubuntu_spec"`
	DockerSpec types.Object `tfsdk:"docker_spec"`
}

type UbuntuSpecModel struct {
	AptPackage types.List `tfsdk:"apt_package"`
}

type DockerSpecModel struct {
	DockerfileContent types.String `tfsdk:"dockerfile_content"`
}

type NetworkPolicyModel struct {
	Egress                types.String `tfsdk:"egress"`
	EgressDomainAllowList types.List   `tfsdk:"egress_domain_allow_list"`
	Advisory              types.Bool   `tfsdk:"advisory"`
	EgressPolicyTag       types.String `tfsdk:"egress_policy_tag"`
}

type CustomRunnerImageStatusModel struct {
	ImageRef       types.String `tfsdk:"image_ref"`
	Status         types.String `tfsdk:"status"`
	LastBuildError types.String `tfsdk:"last_build_error"`
	StartedBuildAt types.String `tfsdk:"started_build_at"`
	BuiltAt        types.String `tfsdk:"built_at"`
	LastErrorAt    types.String `tfsdk:"last_error_at"`
	BuildRef       types.String `tfsdk:"build_ref"`
}

func (r *runnerProfileResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "namespace_github_runner_profile"
}

func (r *runnerProfileResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Namespace Cloud GitHub Actions runner profile, configuring virtual instance compute specs, persistent cache attachments, image customizations, and egress network policies.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Stable server-assigned identifier for this runner profile.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"profile_id": schema.StringAttribute{
				Description: "Stable runner profile ID.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"version": schema.Int64Attribute{
				Description: "Monotonic revision number used for optimistic concurrency locking during updates.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "RFC3339Nano creation timestamp.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				Description: "RFC3339Nano last update timestamp.",
				Computed:    true,
			},
			"tag": schema.StringAttribute{
				Description: "Unique workspace alias for this runner profile, referenced in GitHub Actions `runs-on` labels.",
				Required:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"os": schema.StringAttribute{
				Description: "Base image operating system label (e.g. `ubuntu-24.04`, `macos-15`).",
				Required:    true,
				Validators: []validator.String{
					tfutil.NonEmptyString(),
				},
			},
			"description": schema.StringAttribute{
				Description: "Human-friendly description of the runner profile.",
				Optional:    true,
				Computed:    true,
			},
			"emoji": schema.StringAttribute{
				Description: "Optional emoji visually identifying the profile in the Namespace UI.",
				Optional:    true,
				Computed:    true,
			},
			"swap_size_mb": schema.Int64Attribute{
				Description: "Size of swap space to provision on the runner in megabytes.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.Int64{
					int64validator.Between(0, math.MaxInt32),
				},
			},
			"builder_mode": schema.StringAttribute{
				Description: "Build execution caching mode (`BUILDER_MODE_UNSPECIFIED`, `USE_REMOTE_BUILDER`, `USE_LOCAL_CACHE`, `NO_CACHING`).",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("BUILDER_MODE_UNSPECIFIED", "USE_REMOTE_BUILDER", "USE_LOCAL_CACHE", "NO_CACHING"),
				},
			},
			"access_level": schema.StringAttribute{
				Description: "Controls which Namespace features the runner workload can access (`ACCESS_LEVEL_UNSPECIFIED`, `ACCESS_LEVEL_PERMISSIVE`, `ACCESS_LEVEL_LIMITED`, `ACCESS_LEVEL_RESTRICTED`).",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					stringvalidator.OneOf("ACCESS_LEVEL_UNSPECIFIED", "ACCESS_LEVEL_PERMISSIVE", "ACCESS_LEVEL_LIMITED", "ACCESS_LEVEL_RESTRICTED"),
				},
			},
			"experimental_features": schema.ListAttribute{
				Description: "List of experimental feature flags enabled for this runner profile.",
				ElementType: types.StringType,
				Optional:    true,
				Computed:    true,
			},
			"instance_shape": schema.SingleNestedAttribute{
				Description: "Instance compute shape configuration (CPU, memory, machine arch, OS).",
				Optional:    true,
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"virtual_cpu": schema.Int64Attribute{
						Description: "Number of virtual CPUs.",
						Optional:    true,
						Computed:    true,
						Validators: []validator.Int64{
							int64validator.Between(1, math.MaxInt32),
						},
					},
					"memory_megabytes": schema.Int64Attribute{
						Description: "Memory in megabytes.",
						Optional:    true,
						Computed:    true,
						Validators: []validator.Int64{
							int64validator.Between(1, math.MaxInt32),
						},
					},
					"machine_arch": schema.StringAttribute{
						Description: "Architecture of the instance (`amd64`, `arm64`).",
						Optional:    true,
						Computed:    true,
						Validators: []validator.String{
							stringvalidator.OneOf("amd64", "arm64", ""),
						},
					},
					"os": schema.StringAttribute{
						Description: "Compute OS family (`linux`, `macos`).",
						Optional:    true,
						Computed:    true,
						Validators: []validator.String{
							stringvalidator.OneOf("linux", "macos", ""),
						},
					},
					"selectors": schema.ListNestedAttribute{
						Description: "Node label selectors for instance placement.",
						Optional:    true,
						Computed:    true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{
									Description: "Selector key name.",
									Required:    true,
									Validators: []validator.String{
										tfutil.NonEmptyString(),
									},
								},
								"value": schema.StringAttribute{
									Description: "Selector value.",
									Required:    true,
									Validators: []validator.String{
										tfutil.NonEmptyString(),
									},
								},
							},
						},
					},
				},
			},
			"cache_volume_settings": schema.ListNestedAttribute{
				Description: "Persistent cache volume attachments for this runner profile.",
				Optional:    true,
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"volume_tag": schema.StringAttribute{
							Description: "Unique identifier for this cache volume within the profile.",
							Required:    true,
							Validators: []validator.String{
								tfutil.NonEmptyString(),
							},
						},
						"volume_size_gb": schema.Int64Attribute{
							Description: "Total size of the cache volume in gigabytes.",
							Required:    true,
							Validators: []validator.Int64{
								int64validator.Between(1, math.MaxInt32),
							},
						},
						"volume_reserved_size_gb": schema.Int64Attribute{
							Description: "Reserved size that cannot be used by the cache, in gigabytes.",
							Optional:    true,
							Computed:    true,
							Validators: []validator.Int64{
								int64validator.Between(0, math.MaxInt32),
							},
						},
						"bundled_container_cache": schema.BoolAttribute{
							Description: "Whether to bundle container image cache in this volume.",
							Optional:    true,
							Computed:    true,
						},
						"bundled_buildkit_cache": schema.BoolAttribute{
							Description: "Whether to bundle BuildKit cache in this volume.",
							Optional:    true,
							Computed:    true,
						},
						"bundled_git_mirror": schema.BoolAttribute{
							Description: "Whether to bundle git repository mirror in this volume.",
							Optional:    true,
							Computed:    true,
						},
						"bundled_runner_tools_cache": schema.BoolAttribute{
							Description: "Whether to bundle runner tool cache in this volume.",
							Optional:    true,
							Computed:    true,
						},
						"bundled_runner_actions_cache": schema.BoolAttribute{
							Description: "Whether to bundle GitHub Actions cache in this volume.",
							Optional:    true,
							Computed:    true,
						},
						"allow_commit_from_branch": schema.ListAttribute{
							Description: "List of branch names allowed to write to this cache volume.",
							ElementType: types.StringType,
							Optional:    true,
							Computed:    true,
						},
					},
				},
			},
			"custom_runner_image_spec": schema.SingleNestedAttribute{
				Description: "Custom runner image specification (Ubuntu APT packages and/or inline Dockerfile).",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"ubuntu_spec": schema.SingleNestedAttribute{
						Description: "Ubuntu APT package customizations.",
						Optional:    true,
						Attributes: map[string]schema.Attribute{
							"apt_package": schema.ListAttribute{
								Description: "List of Ubuntu APT package names to install.",
								ElementType: types.StringType,
								Required:    true,
							},
						},
					},
					"docker_spec": schema.SingleNestedAttribute{
						Description: "Dockerfile customizations.",
						Optional:    true,
						Attributes: map[string]schema.Attribute{
							"dockerfile_content": schema.StringAttribute{
								Description: "Dockerfile content to execute as an image layer on top of base image.",
								Required:    true,
								Validators: []validator.String{
									tfutil.NonEmptyString(),
								},
							},
						},
					},
				},
			},
			"network_policy": schema.SingleNestedAttribute{
				Description: "Network egress policy configuration.",
				Optional:    true,
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"egress": schema.StringAttribute{
						Description: "Egress policy mode (`EGRESS_POLICY_UNKNOWN`, `EGRESS_POLICY_NONE`, `EGRESS_POLICY_DOMAIN_ALLOW_LIST`, `EGRESS_POLICY_TAG`).",
						Required:    true,
						Validators: []validator.String{
							stringvalidator.OneOf("EGRESS_POLICY_UNKNOWN", "EGRESS_POLICY_NONE", "EGRESS_POLICY_DOMAIN_ALLOW_LIST", "EGRESS_POLICY_TAG"),
						},
					},
					"egress_domain_allow_list": schema.ListAttribute{
						Description: "List of allowed domain names when egress mode is `EGRESS_POLICY_DOMAIN_ALLOW_LIST`.",
						ElementType: types.StringType,
						Optional:    true,
						Computed:    true,
					},
					"advisory": schema.BoolAttribute{
						Description: "When true, runs egress domain allowlist in advisory mode (log only, not blocked).",
						Optional:    true,
						Computed:    true,
					},
					"egress_policy_tag": schema.StringAttribute{
						Description: "Workspace policy tag to apply when egress mode is `EGRESS_POLICY_TAG`.",
						Optional:    true,
						Computed:    true,
					},
				},
			},
			"custom_runner_images": schema.ListNestedAttribute{
				Description: "List of custom runner image build statuses.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"image_ref": schema.StringAttribute{
							Description: "Registry image reference.",
							Computed:    true,
						},
						"status": schema.StringAttribute{
							Description: "Image build status (`STATUS_UNKNOWN`, `BUILDING`, `READY`, `FAILED`).",
							Computed:    true,
						},
						"last_build_error": schema.StringAttribute{
							Description: "Last image build error message if build failed.",
							Computed:    true,
						},
						"started_build_at": schema.StringAttribute{
							Description: "RFC3339 timestamp when image build started.",
							Computed:    true,
						},
						"built_at": schema.StringAttribute{
							Description: "RFC3339 timestamp when image build completed.",
							Computed:    true,
						},
						"last_error_at": schema.StringAttribute{
							Description: "RFC3339 timestamp of last build failure.",
							Computed:    true,
						},
						"build_ref": schema.StringAttribute{
							Description: "Build reference identifier.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (r *runnerProfileResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		runnerProfileConfigValidator{},
	}
}

type runnerProfileConfigValidator struct{}

func (v runnerProfileConfigValidator) Description(ctx context.Context) string {
	return "validates cross-field rules for runner profile"
}

func (v runnerProfileConfigValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v runnerProfileConfigValidator) ValidateResource(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config RunnerProfileResourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// 1. Custom runner image spec validation: if block is present, at least one of ubuntu_spec or docker_spec must be set
	if !config.CustomRunnerImageSpec.IsNull() && !config.CustomRunnerImageSpec.IsUnknown() {
		var customSpec CustomRunnerImageSpecModel
		diags := config.CustomRunnerImageSpec.As(ctx, &customSpec, basetypes.ObjectAsOptions{})
		resp.Diagnostics.Append(diags...)
		if !resp.Diagnostics.HasError() {
			ubuntuNull := customSpec.UbuntuSpec.IsNull()
			dockerNull := customSpec.DockerSpec.IsNull()
			if ubuntuNull && dockerNull {
				resp.Diagnostics.AddAttributeError(
					path.Root("custom_runner_image_spec"),
					"Invalid Custom Runner Image Spec",
					"When 'custom_runner_image_spec' is declared, at least one of 'ubuntu_spec' or 'docker_spec' must be configured.",
				)
			}
		}
	}

	// 2. Cache volume settings: volume_reserved_size_gb must be < volume_size_gb
	if !config.CacheVolumeSettings.IsNull() && !config.CacheVolumeSettings.IsUnknown() {
		var cvSettings []CacheVolumeSettingModel
		diags := config.CacheVolumeSettings.ElementsAs(ctx, &cvSettings, false)
		resp.Diagnostics.Append(diags...)
		if !resp.Diagnostics.HasError() {
			for idx, cv := range cvSettings {
				if !cv.VolumeSizeGB.IsNull() && !cv.VolumeSizeGB.IsUnknown() &&
					!cv.VolumeReservedSizeGB.IsNull() && !cv.VolumeReservedSizeGB.IsUnknown() {
					size := cv.VolumeSizeGB.ValueInt64()
					res := cv.VolumeReservedSizeGB.ValueInt64()
					if res >= size {
						resp.Diagnostics.AddAttributeError(
							path.Root("cache_volume_settings").AtListIndex(idx).AtName("volume_reserved_size_gb"),
							"Invalid Cache Volume Reserved Size",
							fmt.Sprintf("volume_reserved_size_gb (%d) must be strictly less than volume_size_gb (%d)", res, size),
						)
					}
				}
			}
		}
	}

	// 3. Network policy conditional rules
	if !config.NetworkPolicy.IsNull() && !config.NetworkPolicy.IsUnknown() {
		var np NetworkPolicyModel
		diags := config.NetworkPolicy.As(ctx, &np, basetypes.ObjectAsOptions{})
		resp.Diagnostics.Append(diags...)
		if !resp.Diagnostics.HasError() {
			if !np.Advisory.IsNull() && !np.Advisory.IsUnknown() && np.Advisory.ValueBool() {
				egress := np.Egress.ValueString()
				if egress != "EGRESS_POLICY_DOMAIN_ALLOW_LIST" {
					resp.Diagnostics.AddAttributeError(
						path.Root("network_policy").AtName("advisory"),
						"Invalid Network Policy Advisory Configuration",
						"Attribute 'advisory' cannot be set to true unless egress mode is 'EGRESS_POLICY_DOMAIN_ALLOW_LIST'.",
					)
				}
			}

			if !np.Egress.IsNull() && !np.Egress.IsUnknown() {
				egress := np.Egress.ValueString()
				switch egress {
				case "EGRESS_POLICY_DOMAIN_ALLOW_LIST":
					if np.EgressDomainAllowList.IsNull() || np.EgressDomainAllowList.IsUnknown() || len(np.EgressDomainAllowList.Elements()) == 0 {
						resp.Diagnostics.AddAttributeError(
							path.Root("network_policy").AtName("egress_domain_allow_list"),
							"Missing Required Allow List",
							"Attribute 'egress_domain_allow_list' must contain at least one domain when egress mode is 'EGRESS_POLICY_DOMAIN_ALLOW_LIST'.",
						)
					}
					if !np.EgressPolicyTag.IsNull() && !np.EgressPolicyTag.IsUnknown() && np.EgressPolicyTag.ValueString() != "" {
						resp.Diagnostics.AddAttributeError(
							path.Root("network_policy").AtName("egress_policy_tag"),
							"Invalid Field Combination",
							"Attribute 'egress_policy_tag' is only permitted when egress mode is 'EGRESS_POLICY_TAG'.",
						)
					}
				case "EGRESS_POLICY_TAG":
					if np.EgressPolicyTag.IsNull() || np.EgressPolicyTag.IsUnknown() || strings.TrimSpace(np.EgressPolicyTag.ValueString()) == "" {
						resp.Diagnostics.AddAttributeError(
							path.Root("network_policy").AtName("egress_policy_tag"),
							"Missing Required Egress Policy Tag",
							"Attribute 'egress_policy_tag' must be specified when egress mode is 'EGRESS_POLICY_TAG'.",
						)
					}
					if !np.EgressDomainAllowList.IsNull() && !np.EgressDomainAllowList.IsUnknown() && len(np.EgressDomainAllowList.Elements()) > 0 {
						resp.Diagnostics.AddAttributeError(
							path.Root("network_policy").AtName("egress_domain_allow_list"),
							"Invalid Field Combination",
							"Attribute 'egress_domain_allow_list' is only permitted when egress mode is 'EGRESS_POLICY_DOMAIN_ALLOW_LIST'.",
						)
					}
				default:
					if !np.EgressDomainAllowList.IsNull() && !np.EgressDomainAllowList.IsUnknown() && len(np.EgressDomainAllowList.Elements()) > 0 {
						resp.Diagnostics.AddAttributeError(
							path.Root("network_policy").AtName("egress_domain_allow_list"),
							"Invalid Field Combination",
							"Attribute 'egress_domain_allow_list' is only permitted when egress mode is 'EGRESS_POLICY_DOMAIN_ALLOW_LIST'.",
						)
					}
					if !np.EgressPolicyTag.IsNull() && !np.EgressPolicyTag.IsUnknown() && np.EgressPolicyTag.ValueString() != "" {
						resp.Diagnostics.AddAttributeError(
							path.Root("network_policy").AtName("egress_policy_tag"),
							"Invalid Field Combination",
							"Attribute 'egress_policy_tag' is only permitted when egress mode is 'EGRESS_POLICY_TAG'.",
						)
					}
				}
			}
		}
	}
}

func (r *runnerProfileResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *runnerProfileResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan RunnerProfileResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	spec, diags := buildRunnerProfileSpec(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	createResp, err := r.client.CreateProfile(ctx, &githubv1beta.CreateProfileRequest{
		Spec: spec,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "create", "namespace_github_runner_profile")
		return
	}

	if createResp.GetProfile() == nil {
		resp.Diagnostics.AddError("API Response Error", "Server returned nil profile in CreateProfileResponse")
		return
	}

	diags = flattenRunnerProfile(ctx, createResp.GetProfile(), &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *runnerProfileResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state RunnerProfileResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	profileID := state.ProfileID.ValueString()
	if profileID == "" {
		profileID = state.ID.ValueString()
	}

	listResp, err := r.client.ListProfiles(ctx, &emptypb.Empty{})
	if err != nil {
		if client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_github_runner_profile") {
			resp.State.RemoveResource(ctx)
		}
		return
	}

	var found *githubv1beta.RunnerProfileWithStatus
	for _, p := range listResp.GetProfiles() {
		if p.GetProfileId() == profileID {
			found = p
			break
		}
	}

	if found == nil {
		resp.State.RemoveResource(ctx)
		return
	}

	diags = flattenRunnerProfile(ctx, found, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *runnerProfileResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan RunnerProfileResourceModel
	var state RunnerProfileResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	spec, diags := buildRunnerProfileSpec(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	profileID := state.ProfileID.ValueString()
	if profileID == "" {
		profileID = state.ID.ValueString()
	}

	updateResp, err := r.client.UpdateProfile(ctx, &githubv1beta.UpdateProfileRequest{
		ProfileId:     profileID,
		UpdateVersion: state.Version.ValueInt64(),
		Spec:          spec,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "update", "namespace_github_runner_profile")
		return
	}

	if updateResp.GetProfile() == nil {
		resp.Diagnostics.AddError("API Response Error", "Server returned nil profile in UpdateProfileResponse")
		return
	}

	diags = flattenRunnerProfile(ctx, updateResp.GetProfile(), &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *runnerProfileResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state RunnerProfileResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	profileID := state.ProfileID.ValueString()
	if profileID == "" {
		profileID = state.ID.ValueString()
	}

	_, err := r.client.DeleteProfile(ctx, &githubv1beta.DeleteProfileRequest{
		ProfileId: profileID,
	})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "delete", "namespace_github_runner_profile")
		return
	}
}

func (r *runnerProfileResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importID := req.ID
	if importID == "" {
		resp.Diagnostics.AddError("Invalid Import ID", "Profile ID or tag must be provided to import a runner profile")
		return
	}

	listResp, err := r.client.ListProfiles(ctx, &emptypb.Empty{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_github_runner_profile")
		return
	}

	var matches []*githubv1beta.RunnerProfileWithStatus
	for _, p := range listResp.GetProfiles() {
		if p.GetProfileId() == importID || (p.GetSpec() != nil && p.GetSpec().GetTag() == importID) {
			matches = append(matches, p)
		}
	}

	if len(matches) == 0 {
		resp.Diagnostics.AddError("Resource Not Found", fmt.Sprintf("No runner profile found matching ID or tag %q", importID))
		return
	}
	if len(matches) > 1 {
		resp.Diagnostics.AddError("Ambiguous Import Identifier", fmt.Sprintf("Multiple runner profiles matched identifier %q. Use the exact profile_id instead.", importID))
		return
	}

	var state RunnerProfileResourceModel
	diags := flattenRunnerProfile(ctx, matches[0], &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// buildRunnerProfileSpec converts RunnerProfileResourceModel to githubv1beta.RunnerProfileSpec.
func buildRunnerProfileSpec(ctx context.Context, model RunnerProfileResourceModel) (*githubv1beta.RunnerProfileSpec, diag.Diagnostics) {
	var diags diag.Diagnostics

	spec := &githubv1beta.RunnerProfileSpec{
		Tag:         model.Tag.ValueString(),
		Description: model.Description.ValueString(),
		Os:          model.OS.ValueString(),
		Emoji:       model.Emoji.ValueString(),
		SwapSizeMb:  int32(model.SwapSizeMB.ValueInt64()),
	}

	// BuilderMode
	if !model.BuilderMode.IsNull() && !model.BuilderMode.IsUnknown() {
		bmVal, ok := githubv1beta.BuilderMode_value[model.BuilderMode.ValueString()]
		if ok {
			spec.BuilderMode = githubv1beta.BuilderMode(bmVal)
		}
	}

	// AccessLevel
	if !model.AccessLevel.IsNull() && !model.AccessLevel.IsUnknown() {
		alVal, ok := githubv1beta.AccessLevel_value[model.AccessLevel.ValueString()]
		if ok {
			spec.AccessLevel = githubv1beta.AccessLevel(alVal)
		}
	}

	// ExperimentalFeatures
	if !model.ExperimentalFeatures.IsNull() && !model.ExperimentalFeatures.IsUnknown() {
		expFeatures, d := tfToStringSlice(ctx, model.ExperimentalFeatures)
		diags.Append(d...)
		spec.ExperimentalFeatures = expFeatures
	}

	// InstanceShape
	if !model.InstanceShape.IsNull() && !model.InstanceShape.IsUnknown() {
		var shapeModel InstanceShapeModel
		d := model.InstanceShape.As(ctx, &shapeModel, basetypes.ObjectAsOptions{})
		diags.Append(d...)
		if !diags.HasError() {
			shape := &computev1beta.InstanceShape{
				VirtualCpu:      int32(shapeModel.VirtualCPU.ValueInt64()),
				MemoryMegabytes: int32(shapeModel.MemoryMegabytes.ValueInt64()),
				MachineArch:     shapeModel.MachineArch.ValueString(),
				Os:              shapeModel.OS.ValueString(),
			}
			if !shapeModel.Selectors.IsNull() && !shapeModel.Selectors.IsUnknown() {
				var selectors []LabelModel
				d := shapeModel.Selectors.ElementsAs(ctx, &selectors, false)
				diags.Append(d...)
				for _, sel := range selectors {
					shape.Selectors = append(shape.Selectors, &stdlib.Label{
						Name:  sel.Name.ValueString(),
						Value: sel.Value.ValueString(),
					})
				}
			}
			spec.InstanceShape = shape
		}
	}

	// CacheVolumeSettings
	if !model.CacheVolumeSettings.IsNull() && !model.CacheVolumeSettings.IsUnknown() {
		var cvSettings []CacheVolumeSettingModel
		d := model.CacheVolumeSettings.ElementsAs(ctx, &cvSettings, false)
		diags.Append(d...)
		if !diags.HasError() {
			for _, cv := range cvSettings {
				branches, d := tfToStringSlice(ctx, cv.AllowCommitFromBranch)
				diags.Append(d...)

				spec.CacheVolumeSettings = append(spec.CacheVolumeSettings, &githubv1beta.CacheVolumeSettings{
					VolumeTag:                 cv.VolumeTag.ValueString(),
					VolumeSizeGb:              int32(cv.VolumeSizeGB.ValueInt64()),
					VolumeReservedSizeGb:      int32(cv.VolumeReservedSizeGB.ValueInt64()),
					BundledContainerCache:     cv.BundledContainerCache.ValueBool(),
					BundledBuildkitCache:      cv.BundledBuildkitCache.ValueBool(),
					BundledGitMirror:          cv.BundledGitMirror.ValueBool(),
					BundledRunnerToolsCache:   cv.BundledRunnerToolsCache.ValueBool(),
					BundledRunnerActionsCache: cv.BundledRunnerActionsCache.ValueBool(),
					AllowCommitFromBranch:     branches,
				})
			}
		}
	}

	// CustomRunnerImageSpec
	if !model.CustomRunnerImageSpec.IsNull() && !model.CustomRunnerImageSpec.IsUnknown() {
		var customSpecModel CustomRunnerImageSpecModel
		d := model.CustomRunnerImageSpec.As(ctx, &customSpecModel, basetypes.ObjectAsOptions{})
		diags.Append(d...)
		if !diags.HasError() {
			customSpec := &githubv1beta.CustomRunnerImageSpec{}
			hasSpec := false

			if !customSpecModel.UbuntuSpec.IsNull() && !customSpecModel.UbuntuSpec.IsUnknown() {
				var uSpec UbuntuSpecModel
				d := customSpecModel.UbuntuSpec.As(ctx, &uSpec, basetypes.ObjectAsOptions{})
				diags.Append(d...)
				if !diags.HasError() {
					pkgs, d := tfToStringSlice(ctx, uSpec.AptPackage)
					diags.Append(d...)
					customSpec.UbuntuSpec = &githubv1beta.UbuntuBaseImageSpec{
						AptPackage: pkgs,
					}
					hasSpec = true
				}
			}

			if !customSpecModel.DockerSpec.IsNull() && !customSpecModel.DockerSpec.IsUnknown() {
				var dSpec DockerSpecModel
				d := customSpecModel.DockerSpec.As(ctx, &dSpec, basetypes.ObjectAsOptions{})
				diags.Append(d...)
				if !diags.HasError() {
					customSpec.DockerSpec = &githubv1beta.DockerBaseImageSpec{
						DockerfileContent: dSpec.DockerfileContent.ValueString(),
					}
					hasSpec = true
				}
			}

			if hasSpec {
				spec.CustomRunnerImageSpec = customSpec
			}
		}
	}

	// NetworkPolicy
	if !model.NetworkPolicy.IsNull() && !model.NetworkPolicy.IsUnknown() {
		var npModel NetworkPolicyModel
		d := model.NetworkPolicy.As(ctx, &npModel, basetypes.ObjectAsOptions{})
		diags.Append(d...)
		if !diags.HasError() {
			np := &githubv1beta.NetworkPolicy{}
			if !npModel.Egress.IsNull() && !npModel.Egress.IsUnknown() {
				egressVal, ok := githubv1beta.NetworkPolicy_EgressPolicy_value[npModel.Egress.ValueString()]
				if ok {
					np.Egress = githubv1beta.NetworkPolicy_EgressPolicy(egressVal)
				}
			}

			domains, d := tfToStringSlice(ctx, npModel.EgressDomainAllowList)
			diags.Append(d...)
			np.EgressDomainAllowList = domains
			np.Advisory = npModel.Advisory.ValueBool()
			np.EgressPolicyTag = npModel.EgressPolicyTag.ValueString()

			spec.NetworkPolicy = np
		}
	}

	return spec, diags
}

// flattenRunnerProfile populates target model from githubv1beta.RunnerProfileWithStatus.
func flattenRunnerProfile(ctx context.Context, profile *githubv1beta.RunnerProfileWithStatus, target *RunnerProfileResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics

	// Save planned / existing list attributes to preserve explicit empty lists ([] vs null)
	plannedExpFeatures := target.ExperimentalFeatures
	plannedCVSettings := target.CacheVolumeSettings
	var plannedCVModels []CacheVolumeSettingModel
	if !target.CacheVolumeSettings.IsNull() && !target.CacheVolumeSettings.IsUnknown() {
		_ = target.CacheVolumeSettings.ElementsAs(ctx, &plannedCVModels, false)
	}

	var plannedSelectors types.List
	if !target.InstanceShape.IsNull() && !target.InstanceShape.IsUnknown() {
		var prevShape InstanceShapeModel
		if d := target.InstanceShape.As(ctx, &prevShape, basetypes.ObjectAsOptions{}); !d.HasError() {
			plannedSelectors = prevShape.Selectors
		}
	}

	var plannedAptPackages types.List
	if !target.CustomRunnerImageSpec.IsNull() && !target.CustomRunnerImageSpec.IsUnknown() {
		var prevCustom CustomRunnerImageSpecModel
		if d := target.CustomRunnerImageSpec.As(ctx, &prevCustom, basetypes.ObjectAsOptions{}); !d.HasError() {
			if !prevCustom.UbuntuSpec.IsNull() && !prevCustom.UbuntuSpec.IsUnknown() {
				var prevUbuntu UbuntuSpecModel
				if d2 := prevCustom.UbuntuSpec.As(ctx, &prevUbuntu, basetypes.ObjectAsOptions{}); !d2.HasError() {
					plannedAptPackages = prevUbuntu.AptPackage
				}
			}
		}
	}

	var plannedEgressDomains types.List
	if !target.NetworkPolicy.IsNull() && !target.NetworkPolicy.IsUnknown() {
		var prevNP NetworkPolicyModel
		if d := target.NetworkPolicy.As(ctx, &prevNP, basetypes.ObjectAsOptions{}); !d.HasError() {
			plannedEgressDomains = prevNP.EgressDomainAllowList
		}
	}

	target.ID = types.StringValue(profile.GetProfileId())
	target.ProfileID = types.StringValue(profile.GetProfileId())
	target.Version = types.Int64Value(profile.GetVersion())
	target.CreatedAt = tfutil.TimestampToTFString(profile.GetCreatedAt())
	target.UpdatedAt = tfutil.TimestampToTFString(profile.GetUpdatedAt())

	spec := profile.GetSpec()
	if spec == nil {
		target.Tag = types.StringNull()
		target.OS = types.StringNull()
		target.Description = types.StringNull()
		target.Emoji = types.StringNull()
		target.SwapSizeMB = types.Int64Null()
		target.BuilderMode = types.StringNull()
		target.AccessLevel = types.StringNull()
		target.ExperimentalFeatures = types.ListNull(types.StringType)
		target.InstanceShape = types.ObjectNull(InstanceShapeAttrTypes)
		target.CacheVolumeSettings = types.ListNull(CacheVolumeSettingObjectType)
		target.CustomRunnerImageSpec = types.ObjectNull(CustomRunnerImageSpecAttrTypes)
		target.NetworkPolicy = types.ObjectNull(NetworkPolicyAttrTypes)
		target.CustomRunnerImages = types.ListNull(CustomRunnerImageStatusObjectType)
		return diags
	}

	target.Tag = types.StringValue(spec.GetTag())
	target.OS = types.StringValue(spec.GetOs())
	target.Description = types.StringValue(spec.GetDescription())
	target.Emoji = types.StringValue(spec.GetEmoji())
	target.SwapSizeMB = types.Int64Value(int64(spec.GetSwapSizeMb()))
	target.BuilderMode = types.StringValue(spec.GetBuilderMode().String())
	target.AccessLevel = types.StringValue(spec.GetAccessLevel().String())

	// ExperimentalFeatures
	if len(spec.GetExperimentalFeatures()) > 0 {
		expFeatures, d := stringSliceToTF(ctx, spec.GetExperimentalFeatures())
		diags.Append(d...)
		target.ExperimentalFeatures = expFeatures
	} else if isExplicitEmptyList(plannedExpFeatures) {
		emptyList, d := types.ListValue(types.StringType, []attr.Value{})
		diags.Append(d...)
		target.ExperimentalFeatures = emptyList
	} else {
		target.ExperimentalFeatures = types.ListNull(types.StringType)
	}

	// InstanceShape
	if spec.GetInstanceShape() != nil {
		var selectorObjects []attr.Value
		for _, s := range spec.GetInstanceShape().GetSelectors() {
			selObj, d := types.ObjectValue(LabelAttrTypes, map[string]attr.Value{
				"name":  types.StringValue(s.GetName()),
				"value": types.StringValue(s.GetValue()),
			})
			diags.Append(d...)
			selectorObjects = append(selectorObjects, selObj)
		}
		var selectorsList types.List
		if len(selectorObjects) > 0 {
			selList, d := types.ListValue(LabelObjectType, selectorObjects)
			diags.Append(d...)
			selectorsList = selList
		} else if isExplicitEmptyList(plannedSelectors) {
			selList, d := types.ListValue(LabelObjectType, []attr.Value{})
			diags.Append(d...)
			selectorsList = selList
		} else {
			selectorsList = types.ListNull(LabelObjectType)
		}

		shapeObj, d := types.ObjectValue(InstanceShapeAttrTypes, map[string]attr.Value{
			"virtual_cpu":      types.Int64Value(int64(spec.GetInstanceShape().GetVirtualCpu())),
			"memory_megabytes": types.Int64Value(int64(spec.GetInstanceShape().GetMemoryMegabytes())),
			"machine_arch":     types.StringValue(spec.GetInstanceShape().GetMachineArch()),
			"os":               types.StringValue(spec.GetInstanceShape().GetOs()),
			"selectors":        selectorsList,
		})
		diags.Append(d...)
		target.InstanceShape = shapeObj
	} else {
		target.InstanceShape = types.ObjectNull(InstanceShapeAttrTypes)
	}

	// CacheVolumeSettings
	if len(spec.GetCacheVolumeSettings()) > 0 {
		var cvObjects []attr.Value
		for i, cv := range spec.GetCacheVolumeSettings() {
			var plannedBranches types.List
			if i < len(plannedCVModels) {
				plannedBranches = plannedCVModels[i].AllowCommitFromBranch
			}
			for _, pcv := range plannedCVModels {
				if !pcv.VolumeTag.IsNull() && !pcv.VolumeTag.IsUnknown() && pcv.VolumeTag.ValueString() == cv.GetVolumeTag() {
					plannedBranches = pcv.AllowCommitFromBranch
					break
				}
			}

			var branches types.List
			if len(cv.GetAllowCommitFromBranch()) > 0 {
				bList, d := stringSliceToTF(ctx, cv.GetAllowCommitFromBranch())
				diags.Append(d...)
				branches = bList
			} else if isExplicitEmptyList(plannedBranches) {
				bList, d := types.ListValue(types.StringType, []attr.Value{})
				diags.Append(d...)
				branches = bList
			} else {
				branches = types.ListNull(types.StringType)
			}

			cvObj, d := types.ObjectValue(CacheVolumeSettingAttrTypes, map[string]attr.Value{
				"volume_tag":                   types.StringValue(cv.GetVolumeTag()),
				"volume_size_gb":               types.Int64Value(int64(cv.GetVolumeSizeGb())),
				"volume_reserved_size_gb":      types.Int64Value(int64(cv.GetVolumeReservedSizeGb())),
				"bundled_container_cache":      types.BoolValue(cv.GetBundledContainerCache()),
				"bundled_buildkit_cache":       types.BoolValue(cv.GetBundledBuildkitCache()),
				"bundled_git_mirror":           types.BoolValue(cv.GetBundledGitMirror()),
				"bundled_runner_tools_cache":   types.BoolValue(cv.GetBundledRunnerToolsCache()),
				"bundled_runner_actions_cache": types.BoolValue(cv.GetBundledRunnerActionsCache()),
				"allow_commit_from_branch":     branches,
			})
			diags.Append(d...)
			cvObjects = append(cvObjects, cvObj)
		}
		cvList, d := types.ListValue(CacheVolumeSettingObjectType, cvObjects)
		diags.Append(d...)
		target.CacheVolumeSettings = cvList
	} else if isExplicitEmptyList(plannedCVSettings) {
		cvList, d := types.ListValue(CacheVolumeSettingObjectType, []attr.Value{})
		diags.Append(d...)
		target.CacheVolumeSettings = cvList
	} else {
		target.CacheVolumeSettings = types.ListNull(CacheVolumeSettingObjectType)
	}

	// CustomRunnerImageSpec
	if spec.GetCustomRunnerImageSpec() != nil {
		var uSpecObj attr.Value = types.ObjectNull(UbuntuSpecAttrTypes)
		if spec.GetCustomRunnerImageSpec().GetUbuntuSpec() != nil {
			var pkgs types.List
			if len(spec.GetCustomRunnerImageSpec().GetUbuntuSpec().GetAptPackage()) > 0 {
				pList, d := stringSliceToTF(ctx, spec.GetCustomRunnerImageSpec().GetUbuntuSpec().GetAptPackage())
				diags.Append(d...)
				pkgs = pList
			} else if isExplicitEmptyList(plannedAptPackages) {
				pList, d := types.ListValue(types.StringType, []attr.Value{})
				diags.Append(d...)
				pkgs = pList
			} else {
				pkgs = types.ListNull(types.StringType)
			}
			uObj, d := types.ObjectValue(UbuntuSpecAttrTypes, map[string]attr.Value{
				"apt_package": pkgs,
			})
			diags.Append(d...)
			uSpecObj = uObj
		}

		var dSpecObj attr.Value = types.ObjectNull(DockerSpecAttrTypes)
		if spec.GetCustomRunnerImageSpec().GetDockerSpec() != nil {
			dObj, d := types.ObjectValue(DockerSpecAttrTypes, map[string]attr.Value{
				"dockerfile_content": types.StringValue(spec.GetCustomRunnerImageSpec().GetDockerSpec().GetDockerfileContent()),
			})
			diags.Append(d...)
			dSpecObj = dObj
		}

		if spec.GetCustomRunnerImageSpec().GetUbuntuSpec() != nil || spec.GetCustomRunnerImageSpec().GetDockerSpec() != nil {
			customObj, d := types.ObjectValue(CustomRunnerImageSpecAttrTypes, map[string]attr.Value{
				"ubuntu_spec": uSpecObj,
				"docker_spec": dSpecObj,
			})
			diags.Append(d...)
			target.CustomRunnerImageSpec = customObj
		} else {
			target.CustomRunnerImageSpec = types.ObjectNull(CustomRunnerImageSpecAttrTypes)
		}
	} else {
		target.CustomRunnerImageSpec = types.ObjectNull(CustomRunnerImageSpecAttrTypes)
	}

	// NetworkPolicy
	if spec.GetNetworkPolicy() != nil {
		var domains types.List
		if len(spec.GetNetworkPolicy().GetEgressDomainAllowList()) > 0 {
			dList, d := stringSliceToTF(ctx, spec.GetNetworkPolicy().GetEgressDomainAllowList())
			diags.Append(d...)
			domains = dList
		} else if isExplicitEmptyList(plannedEgressDomains) {
			dList, d := types.ListValue(types.StringType, []attr.Value{})
			diags.Append(d...)
			domains = dList
		} else {
			domains = types.ListNull(types.StringType)
		}

		npObj, d := types.ObjectValue(NetworkPolicyAttrTypes, map[string]attr.Value{
			"egress":                   types.StringValue(spec.GetNetworkPolicy().GetEgress().String()),
			"egress_domain_allow_list": domains,
			"advisory":                 types.BoolValue(spec.GetNetworkPolicy().GetAdvisory()),
			"egress_policy_tag":        types.StringValue(spec.GetNetworkPolicy().GetEgressPolicyTag()),
		})
		diags.Append(d...)
		target.NetworkPolicy = npObj
	} else {
		target.NetworkPolicy = types.ObjectNull(NetworkPolicyAttrTypes)
	}

	// CustomRunnerImages (status)
	if profile.GetStatus() != nil && profile.GetStatus().GetCustomRunnerImage() != nil {
		var imgObjects []attr.Value
		rawImages := make([]*githubv1beta.CustomRunnerImage, 0, len(profile.GetStatus().GetCustomRunnerImage()))
		for _, img := range profile.GetStatus().GetCustomRunnerImage() {
			if img.GetImageStatus() != nil {
				rawImages = append(rawImages, img)
			}
		}
		sort.Slice(rawImages, func(i, j int) bool {
			stI := rawImages[i].GetImageStatus()
			stJ := rawImages[j].GetImageStatus()
			if stI.GetImageRef() != stJ.GetImageRef() {
				return stI.GetImageRef() < stJ.GetImageRef()
			}
			return stI.GetBuildRef() < stJ.GetBuildRef()
		})

		for _, img := range rawImages {
			st := img.GetImageStatus()
			imgObj, d := types.ObjectValue(CustomRunnerImageStatusAttrTypes, map[string]attr.Value{
				"image_ref":        types.StringValue(st.GetImageRef()),
				"status":           types.StringValue(st.GetStatus().String()),
				"last_build_error": types.StringValue(st.GetLastBuildError()),
				"started_build_at": tfutil.TimestampToTFString(st.GetStartedBuildAt()),
				"built_at":         tfutil.TimestampToTFString(st.GetBuiltAt()),
				"last_error_at":    tfutil.TimestampToTFString(st.GetLastErrorAt()),
				"build_ref":        types.StringValue(st.GetBuildRef()),
			})
			diags.Append(d...)
			imgObjects = append(imgObjects, imgObj)
		}
		imgList, d := types.ListValue(CustomRunnerImageStatusObjectType, imgObjects)
		diags.Append(d...)
		target.CustomRunnerImages = imgList
	} else {
		target.CustomRunnerImages = types.ListNull(CustomRunnerImageStatusObjectType)
	}

	return diags
}

func stringSliceToTF(ctx context.Context, slice []string) (types.List, diag.Diagnostics) {
	if slice == nil {
		return types.ListNull(types.StringType), nil
	}
	elems := make([]attr.Value, len(slice))
	for i, v := range slice {
		elems[i] = types.StringValue(v)
	}
	return types.ListValue(types.StringType, elems)
}

func tfToStringSlice(ctx context.Context, list types.List) ([]string, diag.Diagnostics) {
	if list.IsNull() || list.IsUnknown() {
		return nil, nil
	}
	var res []string
	diags := list.ElementsAs(ctx, &res, false)
	return res, diags
}

func isExplicitEmptyList(val types.List) bool {
	return !val.IsNull() && !val.IsUnknown() && len(val.Elements()) == 0
}
