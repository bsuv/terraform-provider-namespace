package github_test

import (
	"context"
	"strings"
	"testing"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	imagesv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/private/images"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceBaseImages(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := github.NewBaseImagesDataSource()

	ctx := context.Background()
	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)

	var confResp datasource.ConfigureResponse
	if confD, ok := d.(datasource.DataSourceWithConfigure); ok {
		confD.Configure(ctx, datasource.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return d, schemaResp
}

func sampleBaseImages() []*githubv1beta.RunnerBaseImage {
	return []*githubv1beta.RunnerBaseImage{
		{
			Label:    "ubuntu-24.04",
			ImageRef: "nscr.io/official/ubuntu-24.04:latest",
			Title:    "Ubuntu 24.04 LTS (Noble Numbat)",
			Spec: &imagesv1beta.BaseImage{
				Id:           "img_u24",
				Os:           "linux",
				OsName:       "Ubuntu",
				OsVersion:    "24.04",
				OsBuild:      "noble",
				Description:  "Ubuntu 24.04 LTS",
				ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION,
				Packages: []*imagesv1beta.Package{
					{
						Name:      "Docker",
						Type:      "system",
						Version:   "27.1.1",
						Build:     "1",
						IsDefault: true,
					},
					{
						Name:      "Rust",
						Type:      "tool",
						Version:   "1.80.0",
						Build:     "1",
						IsDefault: true,
					},
				},
			},
		},
		{
			Label:    "macos-15",
			ImageRef: "nscr.io/official/macos-15:latest",
			Title:    "macOS 15 (Sequoia)",
			Spec: &imagesv1beta.BaseImage{
				Id:           "img_m15",
				Os:           "macos",
				OsName:       "macOS Sequoia",
				OsVersion:    "15.0",
				OsBuild:      "24A335",
				Description:  "macOS 15 Sequoia",
				ReleaseStage: imagesv1beta.ReleaseStage_CANARY,
				Packages: []*imagesv1beta.Package{
					{
						Name:      "Xcode",
						Type:      "simruntime",
						Version:   "16.0",
						Build:     "16A242d",
						IsDefault: true,
					},
				},
			},
		},
		{
			Label:    "ubuntu-22.04",
			ImageRef: "nscr.io/official/ubuntu-22.04:latest",
			Title:    "Ubuntu 22.04 LTS (Jammy Jellyfish)",
			Spec: &imagesv1beta.BaseImage{
				Id:           "img_u22",
				Os:           "linux",
				OsName:       "Ubuntu",
				OsVersion:    "22.04",
				OsBuild:      "jammy",
				Description:  "Ubuntu 22.04 LTS",
				ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION,
				Packages: []*imagesv1beta.Package{
					{
						Name:      "Docker",
						Type:      "system",
						Version:   "26.1.0",
						Build:     "1",
						IsDefault: true,
					},
				},
			},
		},
	}
}

func TestBaseImagesDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := github.NewBaseImagesDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_github_base_images" {
		t.Fatalf("expected type name 'namespace_github_base_images', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	if _, exists := attrs["id"]; !exists {
		t.Error("missing 'id' attribute in base images data source schema")
	}
	if _, exists := attrs["labels"]; !exists {
		t.Error("missing 'labels' attribute in base images data source schema")
	}
	if _, exists := attrs["release_stages"]; !exists {
		t.Error("missing 'release_stages' attribute in base images data source schema")
	}
	if _, exists := attrs["images"]; !exists {
		t.Error("missing 'images' attribute in base images data source schema")
	}

	imagesAttr := attrs["images"].(dschema.ListNestedAttribute)
	specAttr := imagesAttr.NestedObject.Attributes["spec"].(dschema.SingleNestedAttribute)
	osAttr := specAttr.Attributes["os"].(dschema.StringAttribute)
	if !strings.Contains(strings.ToLower(osAttr.Description), "windows") {
		t.Errorf("expected images.*.spec.os description to include 'windows', got %q", osAttr.Description)
	}
}

func TestBaseImagesDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := github.NewBaseImagesDataSource()

	var confResp datasource.ConfigureResponse
	if confD, ok := d.(datasource.DataSourceWithConfigure); ok {
		confD.Configure(ctx, datasource.ConfigureRequest{
			ProviderData: "invalid-type",
		}, &confResp)
		if !confResp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for invalid configure provider data")
		}
	}
}

func TestBaseImagesDataSource_Read_AllImages_Sorted(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// Return images (sampleBaseImages is ubuntu-24.04, macos-15, ubuntu-22.04)
	srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: sampleBaseImages(),
		}, nil
	}

	d, schemaResp := setupTestDataSourceBaseImages(t, srv)

	configModel := github.BaseImagesDataSourceModel{
		ID:            types.StringNull(),
		Labels:        types.ListNull(types.StringType),
		ReleaseStages: types.ListNull(types.StringType),
		Images:        nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &configModel); diags.HasError() {
		t.Fatalf("failed to set config: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var stateResult github.BaseImagesDataSourceModel
	resp.State.Get(ctx, &stateResult)

	if stateResult.ID.ValueString() != "base_images" {
		t.Errorf("expected id 'base_images', got %q", stateResult.ID.ValueString())
	}
	if len(stateResult.Images) != 3 {
		t.Fatalf("expected 3 images in state, got %d", len(stateResult.Images))
	}

	// Verify ascending sort by label: macos-15, ubuntu-22.04, ubuntu-24.04
	if stateResult.Images[0].Label.ValueString() != "macos-15" {
		t.Errorf("expected index 0 to be 'macos-15', got %q", stateResult.Images[0].Label.ValueString())
	}
	if stateResult.Images[1].Label.ValueString() != "ubuntu-22.04" {
		t.Errorf("expected index 1 to be 'ubuntu-22.04', got %q", stateResult.Images[1].Label.ValueString())
	}
	if stateResult.Images[2].Label.ValueString() != "ubuntu-24.04" {
		t.Errorf("expected index 2 to be 'ubuntu-24.04', got %q", stateResult.Images[2].Label.ValueString())
	}

	// Verify nested package details
	ubuntu24 := stateResult.Images[2]
	if ubuntu24.Spec == nil || len(ubuntu24.Spec.Packages) != 2 {
		t.Fatalf("expected 2 packages in ubuntu-24.04, got %v", ubuntu24.Spec)
	}
	if ubuntu24.Spec.Packages[0].Name.ValueString() != "Docker" {
		t.Errorf("expected package 0 to be 'Docker', got %q", ubuntu24.Spec.Packages[0].Name.ValueString())
	}
}

func TestBaseImagesDataSource_Read_WithFilters(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var receivedLabels []string
	var receivedStages []imagesv1beta.ReleaseStage

	srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		if req.Labels != nil {
			receivedLabels = req.Labels.Values
		}
		receivedStages = req.ReleaseStages
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{sampleBaseImages()[0]},
		}, nil
	}

	d, schemaResp := setupTestDataSourceBaseImages(t, srv)

	labelsList, _ := types.ListValue(types.StringType, []attr.Value{types.StringValue("ubuntu-24.04")})
	stagesList, _ := types.ListValue(types.StringType, []attr.Value{types.StringValue("PRODUCTION")})

	configModel := github.BaseImagesDataSourceModel{
		ID:            types.StringNull(),
		Labels:        labelsList,
		ReleaseStages: stagesList,
		Images:        nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &configModel); diags.HasError() {
		t.Fatalf("failed to set config: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	if len(receivedLabels) != 1 || receivedLabels[0] != "ubuntu-24.04" {
		t.Errorf("expected receivedLabels ['ubuntu-24.04'], got %v", receivedLabels)
	}
	if len(receivedStages) != 1 || receivedStages[0] != imagesv1beta.ReleaseStage_PRODUCTION {
		t.Errorf("expected receivedStages [PRODUCTION], got %v", receivedStages)
	}
}

func TestBaseImagesDataSource_Read_InvalidReleaseStage(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	d, schemaResp := setupTestDataSourceBaseImages(t, srv)

	stagesList, _ := types.ListValue(types.StringType, []attr.Value{types.StringValue("INVALID_STAGE_NAME")})

	configModel := github.BaseImagesDataSourceModel{
		ID:            types.StringNull(),
		Labels:        types.ListNull(types.StringType),
		ReleaseStages: stagesList,
		Images:        nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &configModel); diags.HasError() {
		t.Fatalf("failed to set config: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error on invalid release stage")
	}

	if !strings.Contains(resp.Diagnostics[0].Summary(), "Invalid Release Stage") {
		t.Errorf("expected summary 'Invalid Release Stage', got %q", resp.Diagnostics[0].Summary())
	}
}

func TestBaseImagesDataSource_Read_GRPCError(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}

	d, schemaResp := setupTestDataSourceBaseImages(t, srv)

	configModel := github.BaseImagesDataSourceModel{
		ID:            types.StringNull(),
		Labels:        types.ListNull(types.StringType),
		ReleaseStages: types.ListNull(types.StringType),
		Images:        nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error on gRPC failure in base images read")
	}
}

func TestBaseImagesDataSource_Read_DeterministicPackageSorting(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{
				{
					Label:    "custom-img",
					ImageRef: "nscr.io/official/custom:latest",
					Title:    "Custom Image",
					Spec: &imagesv1beta.BaseImage{
						Id:           "img_c",
						Os:           "linux",
						ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION,
						Packages: []*imagesv1beta.Package{
							{Name: "zlib", Type: "system", Version: "1.2.11", Build: "1"},
							{Name: "bash", Type: "shell", Version: "5.1", Build: "1"},
							{Name: "bash", Type: "shell", Version: "5.0", Build: "2"},
							{Name: "curl", Type: "tool", Version: "7.68.0", Build: "1"},
						},
					},
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceBaseImages(t, srv)

	configModel := github.BaseImagesDataSourceModel{
		ID:            types.StringNull(),
		Labels:        types.ListNull(types.StringType),
		ReleaseStages: types.ListNull(types.StringType),
		Images:        nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var stateResult github.BaseImagesDataSourceModel
	_ = resp.State.Get(ctx, &stateResult)
	if len(stateResult.Images) != 1 || stateResult.Images[0].Spec == nil {
		t.Fatalf("expected 1 image with spec, got %v", stateResult.Images)
	}

	pkgs := stateResult.Images[0].Spec.Packages
	if len(pkgs) != 4 {
		t.Fatalf("expected 4 packages, got %d", len(pkgs))
	}

	// Expected deterministic order:
	// 0: bash 5.0 (build 2)
	// 1: bash 5.1 (build 1)
	// 2: curl 7.68.0
	// 3: zlib 1.2.11
	if pkgs[0].Name.ValueString() != "bash" || pkgs[0].Version.ValueString() != "5.0" {
		t.Errorf("pkg 0 mismatch: %v %v", pkgs[0].Name.ValueString(), pkgs[0].Version.ValueString())
	}
	if pkgs[1].Name.ValueString() != "bash" || pkgs[1].Version.ValueString() != "5.1" {
		t.Errorf("pkg 1 mismatch: %v %v", pkgs[1].Name.ValueString(), pkgs[1].Version.ValueString())
	}
	if pkgs[2].Name.ValueString() != "curl" {
		t.Errorf("pkg 2 mismatch: %v", pkgs[2].Name.ValueString())
	}
	if pkgs[3].Name.ValueString() != "zlib" {
		t.Errorf("pkg 3 mismatch: %v", pkgs[3].Name.ValueString())
	}
}

func TestBaseImagesDataSource_Read_WindowsOS(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{
				{
					Label:    "windows-2022",
					ImageRef: "nscr.io/official/windows-2022:latest",
					Title:    "Windows Server 2022",
					Spec: &imagesv1beta.BaseImage{
						Id:           "img_win2022",
						Os:           "windows",
						OsName:       "Windows Server",
						OsVersion:    "2022",
						Description:  "Windows Server 2022",
						ReleaseStage: imagesv1beta.ReleaseStage_PRODUCTION,
					},
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceBaseImages(t, srv)
	configModel := github.BaseImagesDataSourceModel{
		ID:            types.StringNull(),
		Labels:        types.ListNull(types.StringType),
		ReleaseStages: types.ListNull(types.StringType),
		Images:        nil,
	}
	configState := tfsdk.State{Schema: schemaResp.Schema}
	_ = configState.Set(ctx, &configModel)

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var stateResult github.BaseImagesDataSourceModel
	_ = resp.State.Get(ctx, &stateResult)
	if len(stateResult.Images) != 1 || stateResult.Images[0].Spec == nil || stateResult.Images[0].Spec.OS.ValueString() != "windows" {
		t.Fatalf("expected spec.os 'windows', got %+v", stateResult.Images)
	}
}

func TestBaseImagesDataSource_Read_UnknownListElements(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name            string
		labels          []attr.Value
		stages          []attr.Value
		deferralAllowed bool
	}{
		{
			name:            "unknown label element with deferral allowed",
			labels:          []attr.Value{types.StringUnknown()},
			deferralAllowed: true,
		},
		{
			name:            "unknown label element with deferral disallowed",
			labels:          []attr.Value{types.StringUnknown()},
			deferralAllowed: false,
		},
		{
			name:            "unknown release_stage element with deferral allowed",
			stages:          []attr.Value{types.StringUnknown()},
			deferralAllowed: true,
		},
		{
			name:            "unknown release_stage element with deferral disallowed",
			stages:          []attr.Value{types.StringUnknown()},
			deferralAllowed: false,
		},
		{
			name:            "mixed known and unknown label elements with deferral allowed",
			labels:          []attr.Value{types.StringValue("ubuntu-24.04"), types.StringUnknown()},
			deferralAllowed: true,
		},
		{
			name:            "mixed null and unknown release_stage elements with deferral disallowed",
			stages:          []attr.Value{types.StringNull(), types.StringUnknown()},
			deferralAllowed: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewMockServer()
			rpcCalled := false
			srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
				rpcCalled = true
				return &githubv1beta.ListBaseImagesResponse{Images: sampleBaseImages()}, nil
			}

			d, schemaResp := setupTestDataSourceBaseImages(t, srv)

			labelsList := types.ListNull(types.StringType)
			if tc.labels != nil {
				lv, diags := types.ListValue(types.StringType, tc.labels)
				if diags.HasError() {
					t.Fatalf("failed to construct labels list: %v", diags)
				}
				labelsList = lv
			}

			stagesList := types.ListNull(types.StringType)
			if tc.stages != nil {
				sv, diags := types.ListValue(types.StringType, tc.stages)
				if diags.HasError() {
					t.Fatalf("failed to construct release_stages list: %v", diags)
				}
				stagesList = sv
			}

			configModel := github.BaseImagesDataSourceModel{
				ID:            types.StringNull(),
				Labels:        labelsList,
				ReleaseStages: stagesList,
				Images:        nil,
			}

			configState := tfsdk.State{Schema: schemaResp.Schema}
			if diags := configState.Set(ctx, &configModel); diags.HasError() {
				t.Fatalf("failed to set config: %v", diags)
			}

			req := datasource.ReadRequest{
				Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
				ClientCapabilities: datasource.ReadClientCapabilities{
					DeferralAllowed: tc.deferralAllowed,
				},
			}
			resp := &datasource.ReadResponse{
				State: tfsdk.State{Schema: schemaResp.Schema},
			}

			d.Read(ctx, req, resp)

			if rpcCalled {
				t.Fatal("expected ListBaseImages RPC not to be called when config contains unknown list element")
			}

			if tc.deferralAllowed {
				if resp.Diagnostics.HasError() {
					t.Fatalf("expected no error diagnostics when deferral is allowed, got: %v", resp.Diagnostics)
				}
				if resp.Deferred == nil {
					t.Fatal("expected resp.Deferred to be non-nil when deferral is allowed")
				}
			} else {
				if resp.Deferred != nil {
					t.Fatalf("expected resp.Deferred to be nil when deferral is disallowed, got: %+v", resp.Deferred)
				}
				if !resp.Diagnostics.HasError() {
					t.Fatal("expected error diagnostic when deferral is disallowed")
				}
				hasUnknownErr := false
				for _, diag := range resp.Diagnostics.Errors() {
					if strings.Contains(diag.Summary(), "Value Conversion Error") {
						t.Fatalf("unexpected Value Conversion Error diagnostic: %v", diag)
					}
					if strings.Contains(diag.Summary(), "Unknown Data Source Configuration") {
						hasUnknownErr = true
					}
				}
				if !hasUnknownErr {
					t.Fatalf("expected 'Unknown Data Source Configuration' error, got: %v", resp.Diagnostics)
				}
			}
		})
	}
}

func TestBaseImagesDataSource_Read_NullListElements(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var receivedLabels []string
	var receivedStages []imagesv1beta.ReleaseStage
	srv.Image.ListBaseImagesFunc = func(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
		if req.Labels != nil {
			receivedLabels = req.Labels.Values
		}
		receivedStages = req.ReleaseStages
		return &githubv1beta.ListBaseImagesResponse{
			Images: []*githubv1beta.RunnerBaseImage{sampleBaseImages()[0]},
		}, nil
	}

	d, schemaResp := setupTestDataSourceBaseImages(t, srv)

	labelsList, diags := types.ListValue(types.StringType, []attr.Value{types.StringNull(), types.StringValue("ubuntu-24.04")})
	if diags.HasError() {
		t.Fatalf("failed to build labels list: %v", diags)
	}
	stagesList, diags := types.ListValue(types.StringType, []attr.Value{types.StringNull(), types.StringValue("PRODUCTION")})
	if diags.HasError() {
		t.Fatalf("failed to build stages list: %v", diags)
	}

	configModel := github.BaseImagesDataSourceModel{
		ID:            types.StringNull(),
		Labels:        labelsList,
		ReleaseStages: stagesList,
		Images:        nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &configModel); diags.HasError() {
		t.Fatalf("failed to set config: %v", diags)
	}

	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	d.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors with null list elements: %v", resp.Diagnostics)
	}
	if len(receivedLabels) != 1 || receivedLabels[0] != "ubuntu-24.04" {
		t.Fatalf("expected null element to be skipped and receivedLabels=['ubuntu-24.04'], got %v", receivedLabels)
	}
	if len(receivedStages) != 1 || receivedStages[0] != imagesv1beta.ReleaseStage_PRODUCTION {
		t.Fatalf("expected null element to be skipped and receivedStages=[PRODUCTION], got %v", receivedStages)
	}
}
