package github

import (
	"context"
	"fmt"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &runnerProfileDataSource{}
	_ datasource.DataSourceWithConfigure = &runnerProfileDataSource{}
)

// NewRunnerProfileDataSource creates a new runner profile data source instance.
func NewRunnerProfileDataSource() datasource.DataSource {
	return &runnerProfileDataSource{}
}

type runnerProfileDataSource struct {
	client ServiceClient
}

type RunnerProfileDataSourceModel = RunnerProfileResourceModel

func (d *runnerProfileDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_github_runner_profile"
}

func (d *runnerProfileDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads information about a single Namespace Cloud GitHub Actions runner profile by profile_id or tag.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Stable identifier for the runner profile in Terraform state, matching profile_id.",
				Computed:    true,
			},
			"profile_id": schema.StringAttribute{
				Description: "Unique server-assigned identifier for the runner profile. Exactly one of `profile_id` or `tag` must be specified.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("profile_id"), path.MatchRoot("tag")),
				},
			},
			"tag": schema.StringAttribute{
				Description: "Unique workspace alias for this runner profile. Exactly one of `profile_id` or `tag` must be specified.",
				Optional:    true,
				Computed:    true,
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("profile_id"), path.MatchRoot("tag")),
				},
			},
			"version": schema.Int64Attribute{
				Description: "Optimistic concurrency revision number for the runner profile.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "RFC3339Nano creation timestamp.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "RFC3339Nano last update timestamp.",
				Computed:    true,
			},
			"os": schema.StringAttribute{
				Description: "Base image operating system label (e.g. `ubuntu-24.04`, `macos-15`).",
				Computed:    true,
			},
			"description": schema.StringAttribute{
				Description: "Human-friendly description of the runner profile.",
				Computed:    true,
			},
			"emoji": schema.StringAttribute{
				Description: "Optional emoji visually identifying the profile in the Namespace UI.",
				Computed:    true,
			},
			"swap_size_mb": schema.Int64Attribute{
				Description: "Size of swap space to provision on the runner in megabytes.",
				Computed:    true,
			},
			"builder_mode": schema.StringAttribute{
				Description: "Build execution caching mode (`BUILDER_MODE_UNSPECIFIED`, `USE_REMOTE_BUILDER`, `USE_LOCAL_CACHE`, `NO_CACHING`).",
				Computed:    true,
			},
			"access_level": schema.StringAttribute{
				Description: "Controls which Namespace features the runner workload can access (`ACCESS_LEVEL_UNSPECIFIED`, `ACCESS_LEVEL_PERMISSIVE`, `ACCESS_LEVEL_LIMITED`, `ACCESS_LEVEL_RESTRICTED`).",
				Computed:    true,
			},
			"experimental_features": schema.ListAttribute{
				Description: "List of experimental feature flags enabled for this runner profile.",
				ElementType: types.StringType,
				Computed:    true,
			},
			"instance_shape": schema.SingleNestedAttribute{
				Description: "Instance compute shape configuration (CPU, memory, machine arch, OS).",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"virtual_cpu": schema.Int64Attribute{
						Description: "Number of virtual CPUs.",
						Computed:    true,
					},
					"memory_megabytes": schema.Int64Attribute{
						Description: "Memory in megabytes.",
						Computed:    true,
					},
					"machine_arch": schema.StringAttribute{
						Description: "Architecture of the instance (`amd64`, `arm64`).",
						Computed:    true,
					},
					"os": schema.StringAttribute{
						Description: "Compute OS family (`linux`, `macos`).",
						Computed:    true,
					},
					"selectors": schema.ListNestedAttribute{
						Description: "Node label selectors for instance placement.",
						Computed:    true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{
									Description: "Selector key name.",
									Computed:    true,
								},
								"value": schema.StringAttribute{
									Description: "Selector value.",
									Computed:    true,
								},
							},
						},
					},
				},
			},
			"cache_volume_settings": schema.ListNestedAttribute{
				Description: "Persistent cache volume attachments for this runner profile.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"volume_tag": schema.StringAttribute{
							Description: "Unique identifier for this cache volume within the profile.",
							Computed:    true,
						},
						"volume_size_gb": schema.Int64Attribute{
							Description: "Total size of the cache volume in gigabytes.",
							Computed:    true,
						},
						"volume_reserved_size_gb": schema.Int64Attribute{
							Description: "Reserved size that cannot be used by the cache, in gigabytes.",
							Computed:    true,
						},
						"bundled_container_cache": schema.BoolAttribute{
							Description: "Whether to bundle container image cache in this volume.",
							Computed:    true,
						},
						"bundled_buildkit_cache": schema.BoolAttribute{
							Description: "Whether to bundle BuildKit cache in this volume.",
							Computed:    true,
						},
						"bundled_git_mirror": schema.BoolAttribute{
							Description: "Whether to bundle git repository mirror in this volume.",
							Computed:    true,
						},
						"bundled_runner_tools_cache": schema.BoolAttribute{
							Description: "Whether to bundle runner tool cache in this volume.",
							Computed:    true,
						},
						"bundled_runner_actions_cache": schema.BoolAttribute{
							Description: "Whether to bundle GitHub Actions cache in this volume.",
							Computed:    true,
						},
						"allow_commit_from_branch": schema.ListAttribute{
							Description: "List of branch names allowed to write to this cache volume.",
							ElementType: types.StringType,
							Computed:    true,
						},
					},
				},
			},
			"custom_runner_image_spec": schema.SingleNestedAttribute{
				Description: "Custom runner image specification (Ubuntu APT packages and/or inline Dockerfile).",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"ubuntu_spec": schema.SingleNestedAttribute{
						Description: "Ubuntu APT package customizations.",
						Computed:    true,
						Attributes: map[string]schema.Attribute{
							"apt_package": schema.ListAttribute{
								Description: "List of Ubuntu APT package names to install.",
								ElementType: types.StringType,
								Computed:    true,
							},
						},
					},
					"docker_spec": schema.SingleNestedAttribute{
						Description: "Dockerfile customizations.",
						Computed:    true,
						Attributes: map[string]schema.Attribute{
							"dockerfile_content": schema.StringAttribute{
								Description: "Dockerfile content to execute as an image layer on top of base image.",
								Computed:    true,
							},
						},
					},
				},
			},
			"network_policy": schema.SingleNestedAttribute{
				Description: "Network egress policy configuration.",
				Computed:    true,
				Attributes: map[string]schema.Attribute{
					"egress": schema.StringAttribute{
						Description: "Egress policy mode (`EGRESS_POLICY_UNKNOWN`, `EGRESS_POLICY_NONE`, `EGRESS_POLICY_DOMAIN_ALLOW_LIST`, `EGRESS_POLICY_TAG`).",
						Computed:    true,
					},
					"egress_domain_allow_list": schema.ListAttribute{
						Description: "List of allowed domain names when egress mode is `EGRESS_POLICY_DOMAIN_ALLOW_LIST`.",
						ElementType: types.StringType,
						Computed:    true,
					},
					"advisory": schema.BoolAttribute{
						Description: "When true, runs egress domain allowlist in advisory mode (log only, not blocked).",
						Computed:    true,
					},
					"egress_policy_tag": schema.StringAttribute{
						Description: "Workspace policy tag to apply when egress mode is `EGRESS_POLICY_TAG`.",
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

func (d *runnerProfileDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *runnerProfileDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config RunnerProfileDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var unknownAttributes []string
	if config.ProfileID.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "profile_id")
	}
	if config.Tag.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "tag")
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	profileID := config.ProfileID.ValueString()
	tag := config.Tag.ValueString()

	if profileID == "" && tag == "" {
		resp.Diagnostics.AddError("Missing Filter Parameter", "Exactly one of `profile_id` or `tag` must be specified")
		return
	}

	listResp, err := d.client.ListProfiles(ctx, &emptypb.Empty{})
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_github_runner_profile")
		return
	}

	var matches []*githubv1beta.RunnerProfileWithStatus
	for _, p := range listResp.GetProfiles() {
		if profileID != "" && p.GetProfileId() == profileID {
			matches = append(matches, p)
		} else if tag != "" && p.GetSpec() != nil && p.GetSpec().GetTag() == tag {
			matches = append(matches, p)
		}
	}

	filterDesc := fmt.Sprintf("profile_id=%q", profileID)
	if tag != "" {
		filterDesc = fmt.Sprintf("tag=%q", tag)
	}

	if len(matches) == 0 {
		resp.Diagnostics.AddError("Runner Profile Not Found", fmt.Sprintf("No runner profile found matching %s", filterDesc))
		return
	}
	if len(matches) > 1 {
		resp.Diagnostics.AddError("Multiple Runner Profiles Found", fmt.Sprintf("Multiple runner profiles matched %s. Refine your query or use `profile_id`.", filterDesc))
		return
	}

	var state RunnerProfileResourceModel
	diags = flattenRunnerProfile(ctx, matches[0], &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
