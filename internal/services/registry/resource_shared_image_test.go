package registry_test

import (
	"context"
	"testing"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

func setupTestSharedImageResource(t *testing.T, srv *testutil.MockServer) (resource.Resource, resource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	r := registry.NewSharedImageResource()

	ctx := context.Background()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

	var confResp resource.ConfigureResponse
	if confR, ok := r.(resource.ResourceWithConfigure); ok {
		confR.Configure(ctx, resource.ConfigureRequest{
			ProviderData: cl,
		}, &confResp)
		if confResp.Diagnostics.HasError() {
			t.Fatalf("unexpected configure diagnostics: %v", confResp.Diagnostics)
		}
	}

	return r, schemaResp
}

func TestSharedImageResource_Schema(t *testing.T) {
	ctx := context.Background()
	r := registry.NewSharedImageResource()

	var metaResp resource.MetadataResponse
	r.Metadata(ctx, resource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_registry_shared_image" {
		t.Fatalf("expected type name 'namespace_registry_shared_image', got %q", metaResp.TypeName)
	}

	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	expectedAttrs := []string{
		"id", "repository", "digest", "visibility", "expires_at", "suffix",
		"shared_repository", "shared_digest", "image_ref",
	}
	for _, name := range expectedAttrs {
		if _, exists := attrs[name]; !exists {
			t.Errorf("missing expected attribute %q in shared image schema", name)
		}
	}

	// Verify importer is NOT implemented
	if _, ok := r.(resource.ResourceWithImportState); ok {
		t.Error("namespace_registry_shared_image must not implement ResourceWithImportState")
	}
}

func TestSharedImageResource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	r := registry.NewSharedImageResource()

	var confResp resource.ConfigureResponse
	if confR, ok := r.(resource.ResourceWithConfigure); ok {
		confR.Configure(ctx, resource.ConfigureRequest{
			ProviderData: "invalid-type",
		}, &confResp)
		if !confResp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic for invalid configure provider data")
		}
	}
}

func TestSharedImageResource_Create_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	expTime := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	expTimeStr := expTime.Format(time.RFC3339Nano)

	var sharedReq *registryv1beta.ShareImageRequest
	srv.Registry.ShareImageFunc = func(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error) {
		sharedReq = req
		return &registryv1beta.SharedImage{
			Id:               "share_98765",
			SharedRepository: "shared/my-app",
			SharedDigest:     req.Digest,
			ExpiresAt:        timestamppb.New(expTime),
			ImageRef:         "public.nscr.io/share_98765/prod",
			Visibility:       req.Visibility,
			Suffix:           req.Suffix,
		}, nil
	}

	r, schemaResp := setupTestSharedImageResource(t, srv)

	planModel := registry.SharedImageResourceModel{
		ID:               types.StringUnknown(),
		Repository:       types.StringValue("my-app"),
		Digest:           types.StringValue("sha256:1234567890abcdef"),
		Visibility:       types.StringValue("PUBLIC"),
		ExpiresAt:        types.StringValue(expTimeStr),
		Suffix:           types.StringValue("prod"),
		SharedRepository: types.StringUnknown(),
		SharedDigest:     types.StringUnknown(),
		ImageRef:         types.StringUnknown(),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	diags := planState.Set(ctx, &planModel)
	if diags.HasError() {
		t.Fatalf("failed to set plan state: %v", diags)
	}

	req := resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
	}
	resp := &resource.CreateResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	r.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected create errors: %v", resp.Diagnostics)
	}

	if sharedReq == nil || sharedReq.Repository != "my-app" || sharedReq.Visibility != registryv1beta.Visibility_PUBLIC {
		t.Fatalf("unexpected ShareImageRequest: %+v", sharedReq)
	}

	var state registry.SharedImageResourceModel
	diags = resp.State.Get(ctx, &state)
	if diags.HasError() {
		t.Fatalf("failed to get response state: %v", diags)
	}

	if state.ID.ValueString() != "share_98765" {
		t.Errorf("expected state ID 'share_98765', got %q", state.ID.ValueString())
	}
	if state.Repository.ValueString() != "my-app" {
		t.Errorf("expected source repository 'my-app', got %q", state.Repository.ValueString())
	}
	if state.SharedRepository.ValueString() != "shared/my-app" {
		t.Errorf("expected shared repository 'shared/my-app', got %q", state.SharedRepository.ValueString())
	}
	if state.ImageRef.ValueString() != "public.nscr.io/share_98765/prod" {
		t.Errorf("expected image ref 'public.nscr.io/share_98765/prod', got %q", state.ImageRef.ValueString())
	}
}

func TestSharedImageResource_Read_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	expTime := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second)

	srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
		return &registryv1beta.ListSharedImagesResponse{
			SharedImages: []*registryv1beta.SharedImage{
				{
					Id:               "share_111",
					SharedRepository: "shared/app1",
					SharedDigest:     "sha256:111",
					ExpiresAt:        timestamppb.New(expTime),
					ImageRef:         "public.nscr.io/share_111",
					Visibility:       registryv1beta.Visibility_PARTNER,
					Suffix:           "",
				},
			},
		}, nil
	}

	r, schemaResp := setupTestSharedImageResource(t, srv)

	stateModel := registry.SharedImageResourceModel{
		ID:               types.StringValue("share_111"),
		Repository:       types.StringValue("app1"),
		Digest:           types.StringValue("sha256:111"),
		Visibility:       types.StringValue("PARTNER"),
		ExpiresAt:        tfutil.TimestampToTFString(timestamppb.New(expTime)),
		Suffix:           types.StringNull(),
		SharedRepository: types.StringValue("shared/app1"),
		SharedDigest:     types.StringValue("sha256:111"),
		ImageRef:         types.StringValue("public.nscr.io/share_111"),
	}

	initialState := tfsdk.State{Schema: schemaResp.Schema}
	diags := initialState.Set(ctx, &stateModel)
	if diags.HasError() {
		t.Fatalf("failed to set initial state: %v", diags)
	}

	req := resource.ReadRequest{State: initialState}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	var updatedState registry.SharedImageResourceModel
	diags = resp.State.Get(ctx, &updatedState)
	if diags.HasError() {
		t.Fatalf("failed to get state: %v", diags)
	}

	if updatedState.Visibility.ValueString() != "PARTNER" {
		t.Errorf("expected visibility 'PARTNER', got %q", updatedState.Visibility.ValueString())
	}
}

func TestSharedImageResource_Read_NotFound_ExpiredRemovesState(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	// List returns empty list because share expired
	srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
		return &registryv1beta.ListSharedImagesResponse{
			SharedImages: []*registryv1beta.SharedImage{},
		}, nil
	}

	r, schemaResp := setupTestSharedImageResource(t, srv)

	stateModel := registry.SharedImageResourceModel{
		ID:               types.StringValue("share_expired"),
		Repository:       types.StringValue("app1"),
		Digest:           types.StringValue("sha256:111"),
		Visibility:       types.StringValue("PUBLIC"),
		ExpiresAt:        types.StringNull(),
		Suffix:           types.StringNull(),
		SharedRepository: types.StringValue("shared/app1"),
		SharedDigest:     types.StringValue("sha256:111"),
		ImageRef:         types.StringValue("public.nscr.io/share_expired"),
	}

	initialState := tfsdk.State{Schema: schemaResp.Schema}
	_ = initialState.Set(ctx, &stateModel)

	req := resource.ReadRequest{State: initialState}
	resp := &resource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors: %v", resp.Diagnostics)
	}

	if !resp.State.Raw.IsNull() {
		t.Fatal("expected state to be removed (null) after share is no longer listed")
	}
}

func TestSharedImageResource_Update_Rejects(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestSharedImageResource(t, srv)

	planModel := registry.SharedImageResourceModel{
		ID:         types.StringValue("share_1"),
		Repository: types.StringValue("my-app"),
		Digest:     types.StringValue("sha256:abc"),
		Visibility: types.StringValue("PUBLIC"),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	req := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
		State: planState,
	}
	resp := &resource.UpdateResponse{State: planState}

	r.Update(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected update to return diagnostic error, got none")
	}
}

func TestSharedImageResource_Delete_Success(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var unsharedID string
	srv.Registry.UnshareImageFunc = func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
		unsharedID = req.Id
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestSharedImageResource(t, srv)

	stateModel := registry.SharedImageResourceModel{
		ID:         types.StringValue("share_to_delete"),
		Repository: types.StringValue("my-app"),
		Digest:     types.StringValue("sha256:abc"),
		Visibility: types.StringValue("PUBLIC"),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected delete errors: %v", resp.Diagnostics)
	}

	if unsharedID != "share_to_delete" {
		t.Errorf("expected UnshareImage called with 'share_to_delete', got %q", unsharedID)
	}
}

func TestSharedImageResource_Delete_NotFound_Succeeds(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.UnshareImageFunc = func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
		return nil, status.Error(codes.NotFound, "share not found")
	}

	r, schemaResp := setupTestSharedImageResource(t, srv)

	stateModel := registry.SharedImageResourceModel{
		ID:         types.StringValue("share_already_deleted"),
		Repository: types.StringValue("my-app"),
		Digest:     types.StringValue("sha256:abc"),
		Visibility: types.StringValue("PUBLIC"),
	}

	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}

	r.Delete(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected delete error when NotFound: %v", resp.Diagnostics)
	}
}

func TestSharedImageResource_EmptySuffix_PreservesEmptyString(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Registry.ShareImageFunc = func(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error) {
		return &registryv1beta.SharedImage{
			Id:               "share_empty_suffix",
			SharedRepository: "shared/app",
			SharedDigest:     "sha256:111",
			ImageRef:         "public.nscr.io/share_empty_suffix",
			Visibility:       registryv1beta.Visibility_PUBLIC,
			Suffix:           "", // Empty suffix from server
		}, nil
	}

	r, schemaResp := setupTestSharedImageResource(t, srv)

	planModel := registry.SharedImageResourceModel{
		ID:               types.StringNull(),
		Repository:       types.StringValue("app"),
		Digest:           types.StringValue("sha256:111"),
		Visibility:       types.StringValue("PUBLIC"),
		ExpiresAt:        types.StringNull(),
		Suffix:           types.StringValue(""), // User explicitly passed empty string
		SharedRepository: types.StringNull(),
		SharedDigest:     types.StringNull(),
		ImageRef:         types.StringNull(),
	}

	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
	resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	r.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected create errors: %v", resp.Diagnostics)
	}

	var stateResult registry.SharedImageResourceModel
	_ = resp.State.Get(ctx, &stateResult)

	if stateResult.Suffix.IsNull() {
		t.Error("expected state Suffix to be non-null empty string, got null")
	}
	if stateResult.Suffix.ValueString() != "" {
		t.Errorf("expected state Suffix to be empty string, got %q", stateResult.Suffix.ValueString())
	}
}
