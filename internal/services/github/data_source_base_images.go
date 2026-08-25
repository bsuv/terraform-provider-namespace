package github

import (
	"context"
	"fmt"
	"sort"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	imagesv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/images"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

var (
	_ datasource.DataSource              = &baseImagesDataSource{}
	_ datasource.DataSourceWithConfigure = &baseImagesDataSource{}
)

// NewBaseImagesDataSource creates a new base images data source instance.
func NewBaseImagesDataSource() datasource.DataSource {
	return &baseImagesDataSource{}
}

type baseImagesDataSource struct {
	client ServiceClient
}

type BaseImagesDataSourceModel struct {
	ID            types.String           `tfsdk:"id"`
	Labels        types.List             `tfsdk:"labels"`
	ReleaseStages types.List             `tfsdk:"release_stages"`
	Images        []RunnerBaseImageModel `tfsdk:"images"`
}

type RunnerBaseImageModel struct {
	Label    types.String    `tfsdk:"label"`
	ImageRef types.String    `tfsdk:"image_ref"`
	Title    types.String    `tfsdk:"title"`
	Spec     *BaseImageModel `tfsdk:"spec"`
}

type BaseImageModel struct {
	ID           types.String       `tfsdk:"id"`
	OS           types.String       `tfsdk:"os"`
	OSName       types.String       `tfsdk:"os_name"`
	OSVersion    types.String       `tfsdk:"os_version"`
	OSBuild      types.String       `tfsdk:"os_build"`
	Description  types.String       `tfsdk:"description"`
	ReleaseStage types.String       `tfsdk:"release_stage"`
	Packages     []BasePackageModel `tfsdk:"packages"`
}

type BasePackageModel struct {
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Version   types.String `tfsdk:"version"`
	Build     types.String `tfsdk:"build"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

func (d *baseImagesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = "namespace_github_base_images"
}

func (d *baseImagesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Queries official Namespace Cloud runner base images with optional label and release stage filters.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Identifier for this data source.",
				Computed:    true,
			},
			"labels": schema.ListAttribute{
				Description: "Optional list of runner labels to filter base images by.",
				ElementType: types.StringType,
				Optional:    true,
			},
			"release_stages": schema.ListAttribute{
				Description: "Optional list of release stages to filter by (`STAGE_UNKNOWN`, `PRODUCTION`, `CANARY`).",
				ElementType: types.StringType,
				Optional:    true,
			},
			"images": schema.ListNestedAttribute{
				Description: "List of matching runner base images sorted deterministically by label.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"label": schema.StringAttribute{
							Description: "Runs-on label identifier (e.g. `ubuntu-24.04`, `macos-15`).",
							Computed:    true,
						},
						"image_ref": schema.StringAttribute{
							Description: "Canonical container image reference.",
							Computed:    true,
						},
						"title": schema.StringAttribute{
							Description: "Human-readable title (e.g. `Ubuntu 24.04 LTS`).",
							Computed:    true,
						},
						"spec": schema.SingleNestedAttribute{
							Description: "Detailed base image specification.",
							Computed:    true,
							Attributes: map[string]schema.Attribute{
								"id": schema.StringAttribute{
									Description: "Base image unique identifier.",
									Computed:    true,
								},
								"os": schema.StringAttribute{
									Description: "Compute OS family (`linux`, `macos`).",
									Computed:    true,
								},
								"os_name": schema.StringAttribute{
									Description: "Human-friendly OS distribution name.",
									Computed:    true,
								},
								"os_version": schema.StringAttribute{
									Description: "OS distribution version.",
									Computed:    true,
								},
								"os_build": schema.StringAttribute{
									Description: "OS build identifier.",
									Computed:    true,
								},
								"description": schema.StringAttribute{
									Description: "Description of the base image.",
									Computed:    true,
								},
								"release_stage": schema.StringAttribute{
									Description: "Release stage (`STAGE_UNKNOWN`, `PRODUCTION`, `CANARY`).",
									Computed:    true,
									Validators: []validator.String{
										stringvalidator.OneOf("STAGE_UNKNOWN", "PRODUCTION", "CANARY"),
									},
								},
								"packages": schema.ListNestedAttribute{
									Description: "Pre-installed software packages included in this base image.",
									Computed:    true,
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"name": schema.StringAttribute{
												Description: "Package name (e.g. `Docker`, `Rust`, `Xcode`).",
												Computed:    true,
											},
											"type": schema.StringAttribute{
												Description: "Package installation type (e.g. `system`, `simruntime`).",
												Computed:    true,
											},
											"version": schema.StringAttribute{
												Description: "Package version.",
												Computed:    true,
											},
											"build": schema.StringAttribute{
												Description: "Package build number.",
												Computed:    true,
											},
											"is_default": schema.BoolAttribute{
												Description: "Whether this version is the default.",
												Computed:    true,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func (d *baseImagesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *baseImagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config BaseImagesDataSourceModel
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	var unknownAttributes []string
	if config.Labels.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "labels")
	}
	if config.ReleaseStages.IsUnknown() {
		unknownAttributes = append(unknownAttributes, "release_stages")
	}
	if tfutil.DeferDataSourceReadIfUnknown(req, resp, unknownAttributes...) {
		return
	}

	rpcReq := &githubv1beta.ListBaseImagesRequest{}

	labels, dList := tfToStringSlice(ctx, config.Labels)
	resp.Diagnostics.Append(dList...)
	if len(labels) > 0 {
		rpcReq.Labels = &stdlib.StringMatcher{
			Values: labels,
			Op:     stdlib.StringMatcher_IS_ANY_OF,
		}
	}

	stages, dStages := tfToStringSlice(ctx, config.ReleaseStages)
	resp.Diagnostics.Append(dStages...)
	for _, stage := range stages {
		val, ok := imagesv1beta.ReleaseStage_value[stage]
		if !ok {
			resp.Diagnostics.AddError("Invalid Release Stage", fmt.Sprintf("Invalid release stage: %q. Allowed: `STAGE_UNKNOWN`, `PRODUCTION`, `CANARY`", stage))
			return
		}
		rpcReq.ReleaseStages = append(rpcReq.ReleaseStages, imagesv1beta.ReleaseStage(val))
	}

	listResp, err := d.client.ListBaseImages(ctx, rpcReq)
	if err != nil {
		client.HandleGRPCError(ctx, err, &resp.Diagnostics, "read", "namespace_github_base_images")
		return
	}

	images := listResp.GetImages()
	sort.Slice(images, func(i, j int) bool {
		return images[i].GetLabel() < images[j].GetLabel()
	})

	var resultImages []RunnerBaseImageModel
	for _, img := range images {
		imgModel := RunnerBaseImageModel{
			Label:    types.StringValue(img.GetLabel()),
			ImageRef: types.StringValue(img.GetImageRef()),
			Title:    types.StringValue(img.GetTitle()),
		}

		if spec := img.GetSpec(); spec != nil {
			var pkgs []BasePackageModel
			for _, pkg := range spec.GetPackages() {
				pkgs = append(pkgs, BasePackageModel{
					Name:      types.StringValue(pkg.GetName()),
					Type:      types.StringValue(pkg.GetType()),
					Version:   types.StringValue(pkg.GetVersion()),
					Build:     types.StringValue(pkg.GetBuild_()),
					IsDefault: types.BoolValue(pkg.GetIsDefault()),
				})
			}
			sort.Slice(pkgs, func(i, j int) bool {
				if pkgs[i].Name.ValueString() != pkgs[j].Name.ValueString() {
					return pkgs[i].Name.ValueString() < pkgs[j].Name.ValueString()
				}
				if pkgs[i].Version.ValueString() != pkgs[j].Version.ValueString() {
					return pkgs[i].Version.ValueString() < pkgs[j].Version.ValueString()
				}
				if pkgs[i].Build.ValueString() != pkgs[j].Build.ValueString() {
					return pkgs[i].Build.ValueString() < pkgs[j].Build.ValueString()
				}
				return pkgs[i].Type.ValueString() < pkgs[j].Type.ValueString()
			})

			imgModel.Spec = &BaseImageModel{
				ID:           types.StringValue(spec.GetId()),
				OS:           types.StringValue(spec.GetOs()),
				OSName:       types.StringValue(spec.GetOsName()),
				OSVersion:    types.StringValue(spec.GetOsVersion()),
				OSBuild:      types.StringValue(spec.GetOsBuild()),
				Description:  types.StringValue(spec.GetDescription()),
				ReleaseStage: types.StringValue(spec.GetReleaseStage().String()),
				Packages:     pkgs,
			}
		}

		resultImages = append(resultImages, imgModel)
	}

	labelsVal := config.Labels
	if labelsVal.IsUnknown() {
		labelsVal = types.ListNull(types.StringType)
	}
	stagesVal := config.ReleaseStages
	if stagesVal.IsUnknown() {
		stagesVal = types.ListNull(types.StringType)
	}

	state := BaseImagesDataSourceModel{
		ID:            types.StringValue("base_images"),
		Labels:        labelsVal,
		ReleaseStages: stagesVal,
		Images:        resultImages,
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
