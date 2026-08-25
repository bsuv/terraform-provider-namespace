package registry_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
	"github.com/bsuv/terraform-provider-namespace/internal/tfutil"
)

// =============================================================================
// GROUP 1: SHARED IMAGE ADVERSARIAL TESTS
// =============================================================================

// TestSharedImage_Adversarial_RequiresReplaceOnAllMutableAttributes verifies
// that every mutable attribute in the schema enforces RequiresReplace plan modifier.
func TestSharedImage_Adversarial_RequiresReplaceOnAllMutableAttributes(t *testing.T) {
	ctx := context.Background()
	r := registry.NewSharedImageResource()
	var schemaResp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema error: %v", schemaResp.Diagnostics)
	}

	mutableAttrs := []string{"repository", "digest", "visibility", "expires_at", "suffix"}
	for _, attrName := range mutableAttrs {
		attr, ok := schemaResp.Schema.Attributes[attrName]
		if !ok {
			t.Fatalf("attribute %q missing from schema", attrName)
		}
		strAttr, ok := attr.(schema.StringAttribute)
		if !ok {
			t.Fatalf("attribute %q is not a StringAttribute", attrName)
		}
		if len(strAttr.PlanModifiers) == 0 {
			t.Errorf("attribute %q has no plan modifiers, expected RequiresReplace", attrName)
		}
		hasRequiresReplace := false
		for _, pm := range strAttr.PlanModifiers {
			pmDesc := fmt.Sprintf("%T", pm)
			if strings.Contains(pmDesc, "requiresReplace") || strings.Contains(pmDesc, "RequiresReplace") {
				hasRequiresReplace = true
				break
			}
		}
		if !hasRequiresReplace {
			t.Errorf("attribute %q does not contain RequiresReplace plan modifier", attrName)
		}
	}
}

// TestSharedImage_Adversarial_UpdateRejection verifies that Update() explicitly rejects
// in-place updates with an informative diagnostic error.
func TestSharedImage_Adversarial_UpdateRejection(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()
	r, schemaResp := setupTestSharedImageResource(t, srv)

	stateModel := registry.SharedImageResourceModel{
		ID:         types.StringValue("share_123"),
		Repository: types.StringValue("repo-a"),
		Digest:     types.StringValue("sha256:111111"),
		Visibility: types.StringValue("PUBLIC"),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	planModel := stateModel
	planModel.Visibility = types.StringValue("PARTNER")
	planState := tfsdk.State{Schema: schemaResp.Schema}
	_ = planState.Set(ctx, &planModel)

	req := resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
		State: state,
	}
	resp := &resource.UpdateResponse{State: state}

	r.Update(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected Update to return error diagnostic, got none")
	}

	foundMsg := false
	for _, err := range resp.Diagnostics.Errors() {
		if strings.Contains(err.Summary(), "Update Not Supported") || strings.Contains(err.Detail(), "replacement") {
			foundMsg = true
			break
		}
	}
	if !foundMsg {
		t.Errorf("expected 'Update Not Supported' error diagnostic, got %v", resp.Diagnostics)
	}
}

// TestSharedImage_Adversarial_ReadExpiredOrNotFoundRemovesState verifies state removal
// when ListSharedImages does not contain the resource, or when the server returns NotFound.
func TestSharedImage_Adversarial_ReadExpiredOrNotFoundRemovesState(t *testing.T) {
	ctx := context.Background()

	t.Run("ImageMissingFromList", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
			return &registryv1beta.ListSharedImagesResponse{
				SharedImages: []*registryv1beta.SharedImage{
					{Id: "other_share_1"},
					{Id: "other_share_2"},
				},
			}, nil
		}
		r, schemaResp := setupTestSharedImageResource(t, srv)

		stateModel := registry.SharedImageResourceModel{
			ID:         types.StringValue("my_expired_share"),
			Repository: types.StringValue("app"),
			Digest:     types.StringValue("sha256:abc"),
			Visibility: types.StringValue("PUBLIC"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.ReadRequest{State: state}
		resp := &resource.ReadResponse{State: state}
		r.Read(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected read error: %v", resp.Diagnostics)
		}
		if !resp.State.Raw.IsNull() {
			t.Fatal("expected state to be removed (null) when image missing from ListSharedImages")
		}
	})

	t.Run("ServerReturnsNotFound", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
			return nil, status.Error(codes.NotFound, "shared image registry partition not found")
		}
		r, schemaResp := setupTestSharedImageResource(t, srv)

		stateModel := registry.SharedImageResourceModel{
			ID:         types.StringValue("my_share"),
			Repository: types.StringValue("app"),
			Digest:     types.StringValue("sha256:abc"),
			Visibility: types.StringValue("PUBLIC"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.ReadRequest{State: state}
		resp := &resource.ReadResponse{State: state}
		r.Read(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected read error on NotFound: %v", resp.Diagnostics)
		}
		if !resp.State.Raw.IsNull() {
			t.Fatal("expected state to be removed (null) when server returns NotFound")
		}
	})

	t.Run("ServerReturnsTransientUnavailable_StatePreserved", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
			return nil, status.Error(codes.Unavailable, "registry service temporarily unavailable")
		}
		r, schemaResp := setupTestSharedImageResource(t, srv)

		stateModel := registry.SharedImageResourceModel{
			ID:         types.StringValue("my_share"),
			Repository: types.StringValue("app"),
			Digest:     types.StringValue("sha256:abc"),
			Visibility: types.StringValue("PUBLIC"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.ReadRequest{State: state}
		resp := &resource.ReadResponse{State: state}
		r.Read(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected error diagnostic on Unavailable, got none")
		}
		if resp.State.Raw.IsNull() {
			t.Fatal("state must NOT be removed on transient Unavailable error")
		}
	})
}

// TestSharedImage_Adversarial_DeleteIdempotency verifies that Delete cleanly succeeds
// if the shared image was already removed or expired (NotFound), and reports real errors.
func TestSharedImage_Adversarial_DeleteIdempotency(t *testing.T) {
	ctx := context.Background()

	t.Run("AlreadyDeleted_NotFoundSucceeds", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.UnshareImageFunc = func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
			return nil, status.Error(codes.NotFound, "share reference does not exist")
		}
		r, schemaResp := setupTestSharedImageResource(t, srv)

		stateModel := registry.SharedImageResourceModel{
			ID:         types.StringValue("share_already_gone"),
			Repository: types.StringValue("backend"),
			Digest:     types.StringValue("sha256:999"),
			Visibility: types.StringValue("PUBLIC"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("expected Delete to succeed cleanly on NotFound, got: %v", resp.Diagnostics)
		}
	})

	t.Run("PermissionDenied_FailsWithDiagnostic", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.UnshareImageFunc = func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
			return nil, status.Error(codes.PermissionDenied, "unauthorized caller")
		}
		r, schemaResp := setupTestSharedImageResource(t, srv)

		stateModel := registry.SharedImageResourceModel{
			ID:         types.StringValue("share_denied"),
			Repository: types.StringValue("backend"),
			Digest:     types.StringValue("sha256:999"),
			Visibility: types.StringValue("PUBLIC"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected Delete to return diagnostic error on PermissionDenied, got none")
		}
	})
}

// TestSharedImage_Adversarial_CreateEdgeCases verifies Create behavior with
// various payload combinations and error handling.
func TestSharedImage_Adversarial_CreateEdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("InvalidVisibilityString", func(t *testing.T) {
		srv := testutil.NewMockServer()
		r, schemaResp := setupTestSharedImageResource(t, srv)

		planModel := registry.SharedImageResourceModel{
			ID:         types.StringUnknown(),
			Repository: types.StringValue("backend"),
			Digest:     types.StringValue("sha256:123"),
			Visibility: types.StringValue("INTERNAL_ONLY"), // Invalid visibility
		}
		planState := tfsdk.State{Schema: schemaResp.Schema}
		_ = planState.Set(ctx, &planModel)

		req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
		resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		r.Create(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected Create to fail with invalid visibility string, got none")
		}
	})

	t.Run("InvalidExpiresAtFormat", func(t *testing.T) {
		srv := testutil.NewMockServer()
		r, schemaResp := setupTestSharedImageResource(t, srv)

		planModel := registry.SharedImageResourceModel{
			ID:         types.StringUnknown(),
			Repository: types.StringValue("backend"),
			Digest:     types.StringValue("sha256:123"),
			Visibility: types.StringValue("PUBLIC"),
			ExpiresAt:  types.StringValue("2026-99-99T99:99:99Z"), // Invalid timestamp
		}
		planState := tfsdk.State{Schema: schemaResp.Schema}
		_ = planState.Set(ctx, &planModel)

		req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
		resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		r.Create(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected Create to fail with malformed expires_at timestamp, got none")
		}
	})

	t.Run("ServerRPCFailure", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.ShareImageFunc = func(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error) {
			return nil, status.Error(codes.InvalidArgument, "invalid image digest format")
		}
		r, schemaResp := setupTestSharedImageResource(t, srv)

		planModel := registry.SharedImageResourceModel{
			ID:         types.StringUnknown(),
			Repository: types.StringValue("backend"),
			Digest:     types.StringValue("invalid-digest"),
			Visibility: types.StringValue("PUBLIC"),
		}
		planState := tfsdk.State{Schema: schemaResp.Schema}
		_ = planState.Set(ctx, &planModel)

		req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
		resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		r.Create(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected Create to fail with gRPC error diagnostic, got none")
		}
	})
}

// =============================================================================
// GROUP 2: DEFAULT POLICY ADVERSARIAL TESTS
// =============================================================================

// TestDefaultPolicy_Adversarial_ConcurrencyConflictHandling verifies that
// optimistic locking failures return actionable Concurrency Conflict diagnostics.
func TestDefaultPolicy_Adversarial_ConcurrencyConflictHandling(t *testing.T) {
	ctx := context.Background()

	t.Run("UpdateRevisionMismatch", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
			return nil, status.Error(codes.FailedPrecondition, "revision mismatch: expected revision 50, got revision 40")
		}
		r, schemaResp := setupTestDefaultPolicyResource(t, srv)

		planModel := registry.DefaultPolicyResourceModel{
			ID:                    types.StringValue("default"),
			Revision:              types.Int64Unknown(),
			DefaultExpiration:     types.StringValue("48h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		stateModel := registry.DefaultPolicyResourceModel{
			ID:                    types.StringValue("default"),
			Revision:              types.Int64Value(40),
			DefaultExpiration:     types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}

		planState := tfsdk.State{Schema: schemaResp.Schema}
		_ = planState.Set(ctx, &planModel)
		origState := tfsdk.State{Schema: schemaResp.Schema}
		_ = origState.Set(ctx, &stateModel)

		req := resource.UpdateRequest{
			Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
			State: origState,
		}
		resp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

		r.Update(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected Concurrency Conflict error diagnostic, got none")
		}

		foundConflict := false
		for _, err := range resp.Diagnostics.Errors() {
			if strings.Contains(err.Summary(), "Concurrency Conflict") && strings.Contains(err.Detail(), "re-plan") {
				foundConflict = true
				break
			}
		}
		if !foundConflict {
			t.Errorf("expected Concurrency Conflict diagnostic with re-plan instructions, got %v", resp.Diagnostics)
		}
	})

	t.Run("CreateFailedPrecondition", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
			return &registryv1beta.GetDefaultPolicyResponse{
				Revision:              10,
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
			}, nil
		}
		srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
			return nil, status.Error(codes.FailedPrecondition, "concurrent modification detected on revision 10")
		}
		r, schemaResp := setupTestDefaultPolicyResource(t, srv)

		planModel := registry.DefaultPolicyResourceModel{
			ID:                    types.StringUnknown(),
			Revision:              types.Int64Unknown(),
			DefaultExpiration:     types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		planState := tfsdk.State{Schema: schemaResp.Schema}
		_ = planState.Set(ctx, &planModel)

		req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
		resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		r.Create(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected Create to fail on FailedPrecondition error, got none")
		}
	})
}

// TestDefaultPolicy_Adversarial_DurationValidation tests duration boundaries (< 4h rejected, >= 4h accepted, invalid formats rejected).
func TestDefaultPolicy_Adversarial_DurationValidation(t *testing.T) {
	ctx := context.Background()
	durValidator := tfutil.DurationAtLeast(4 * time.Hour)

	testCases := []struct {
		input       string
		expectError bool
		errorSubstr string
	}{
		{"3h", true, "must be at least 4h0m0s"},
		{"3h59m59s", true, "must be at least 4h0m0s"},
		{"10m", true, "must be at least 4h0m0s"},
		{"0s", true, "must be at least 4h0m0s"},
		{"-1h", true, "must be at least 4h0m0s"},
		{"invalid-duration", true, "must be a valid Go duration string"},
		{"24hours", true, "must be a valid Go duration string"},
		{"4h", false, ""},
		{"4h1s", false, ""},
		{"24h", false, ""},
		{"168h", false, ""},
		{"720h", false, ""},
		{"8760h", false, ""},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("Duration_%s", tc.input), func(t *testing.T) {
			req := validator.StringRequest{
				Path:        path.Root("default_expiration"),
				ConfigValue: types.StringValue(tc.input),
			}
			resp := &validator.StringResponse{}
			durValidator.ValidateString(ctx, req, resp)

			if tc.expectError {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("expected validation error for %q, got none", tc.input)
				}
				if tc.errorSubstr != "" {
					found := false
					for _, d := range resp.Diagnostics.Errors() {
						if strings.Contains(d.Detail(), tc.errorSubstr) {
							found = true
							break
						}
					}
					if !found {
						t.Errorf("expected error containing %q, got %v", tc.errorSubstr, resp.Diagnostics)
					}
				}
			} else {
				if resp.Diagnostics.HasError() {
					t.Fatalf("expected valid duration for %q, got errors: %v", tc.input, resp.Diagnostics)
				}
			}
		})
	}
}

// TestDefaultPolicy_Adversarial_DestroyNeutralReset verifies that Destroy properly resets
// the default policy to DO_NOT_ENFORCE and nil expiration with fresh revision.
func TestDefaultPolicy_Adversarial_DestroyNeutralReset(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var getCallCount int
	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		getCallCount++
		if getCallCount == 1 {
			// Pre-reset read: server has updated revision 105
			return &registryv1beta.GetDefaultPolicyResponse{
				Revision:              105,
				DefaultExpiration:     durationpb.New(24 * time.Hour),
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
			}, nil
		}
		// Post-reset read
		return &registryv1beta.GetDefaultPolicyResponse{
			Revision:              106,
			DefaultExpiration:     nil,
			ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
		}, nil
	}

	var resetReq *registryv1beta.UpdateDefaultPolicyRequest
	srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
		resetReq = req
		return &emptypb.Empty{}, nil
	}

	r, schemaResp := setupTestDefaultPolicyResource(t, srv)

	// State held older revision 100
	stateModel := registry.DefaultPolicyResourceModel{
		ID:                    types.StringValue("default"),
		Revision:              types.Int64Value(100),
		DefaultExpiration:     types.StringValue("24h"),
		ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
	}
	state := tfsdk.State{Schema: schemaResp.Schema}
	_ = state.Set(ctx, &stateModel)

	req := resource.DeleteRequest{State: state}
	resp := &resource.DeleteResponse{State: state}
	r.Delete(ctx, req, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected delete error: %v", resp.Diagnostics)
	}

	if resetReq == nil {
		t.Fatal("expected UpdateDefaultPolicy to be called on Delete")
	}
	if resetReq.Revision != 105 {
		t.Errorf("expected Delete to use freshly fetched revision 105, got %d", resetReq.Revision)
	}
	if resetReq.DefaultExpiration != nil {
		t.Errorf("expected DefaultExpiration to be nil on reset, got %v", resetReq.DefaultExpiration)
	}
	if resetReq.ExpirationEnforcement != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE {
		t.Errorf("expected ExpirationEnforcement DO_NOT_ENFORCE, got %v", resetReq.ExpirationEnforcement)
	}
	if getCallCount < 2 {
		t.Errorf("expected post-update verification GetDefaultPolicy call, got %d calls", getCallCount)
	}
}

// =============================================================================
// GROUP 3: REPOSITORY POLICY ADVERSARIAL TESTS
// =============================================================================

// TestRepositoryPolicy_Adversarial_ConcurrencyConflictHandling verifies that
// optimistic locking failures on repository policies return actionable Concurrency Conflict diagnostics.
func TestRepositoryPolicy_Adversarial_ConcurrencyConflictHandling(t *testing.T) {
	ctx := context.Background()

	t.Run("UpdateRevisionConflict", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			return nil, status.Error(codes.FailedPrecondition, "revision mismatch: expected 12, got 10")
		}
		r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

		planModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("microservice-api"),
			Repository:            types.StringValue("microservice-api"),
			Revision:              types.Int64Unknown(),
			Expiration:            types.StringValue("48h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("microservice-api"),
			Repository:            types.StringValue("microservice-api"),
			Revision:              types.Int64Value(10),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}

		planState := tfsdk.State{Schema: schemaResp.Schema}
		_ = planState.Set(ctx, &planModel)
		origState := tfsdk.State{Schema: schemaResp.Schema}
		_ = origState.Set(ctx, &stateModel)

		req := resource.UpdateRequest{
			Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw},
			State: origState,
		}
		resp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

		r.Update(ctx, req, resp)
		if !resp.Diagnostics.HasError() {
			t.Fatal("expected Concurrency Conflict error diagnostic, got none")
		}

		foundConflict := false
		for _, err := range resp.Diagnostics.Errors() {
			if strings.Contains(err.Summary(), "Concurrency Conflict") {
				foundConflict = true
				break
			}
		}
		if !foundConflict {
			t.Errorf("expected Concurrency Conflict diagnostic, got %v", resp.Diagnostics)
		}
	})

	t.Run("CreateRevisionConflict", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              5,
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN,
			}, nil
		}
		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			return nil, status.Error(codes.FailedPrecondition, "concurrent modification on revision 5")
		}
		r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

		planModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringUnknown(),
			Repository:            types.StringValue("microservice-api"),
			Revision:              types.Int64Unknown(),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		planState := tfsdk.State{Schema: schemaResp.Schema}
		_ = planState.Set(ctx, &planModel)

		req := resource.CreateRequest{Plan: tfsdk.Plan{Schema: schemaResp.Schema, Raw: planState.Raw}}
		resp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
		r.Create(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected Create to fail with Concurrency Conflict, got none")
		}
	})
}

// TestRepositoryPolicy_Adversarial_DurationValidation tests duration boundaries on repository policy.
func TestRepositoryPolicy_Adversarial_DurationValidation(t *testing.T) {
	ctx := context.Background()
	durValidator := tfutil.DurationAtLeast(4 * time.Hour)

	testCases := []struct {
		input       string
		expectError bool
	}{
		{"1h", true},
		{"3h59m", true},
		{"0m", true},
		{"-24h", true},
		{"invalid", true},
		{"4h", false},
		{"12h", false},
		{"72h", false},
		{"720h", false},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("RepoPolicy_Duration_%s", tc.input), func(t *testing.T) {
			req := validator.StringRequest{
				Path:        path.Root("expiration"),
				ConfigValue: types.StringValue(tc.input),
			}
			resp := &validator.StringResponse{}
			durValidator.ValidateString(ctx, req, resp)

			if tc.expectError && !resp.Diagnostics.HasError() {
				t.Fatalf("expected error for %q, got none", tc.input)
			}
			if !tc.expectError && resp.Diagnostics.HasError() {
				t.Fatalf("expected valid duration for %q, got %v", tc.input, resp.Diagnostics)
			}
		})
	}
}

// TestRepositoryPolicy_Adversarial_DestroyClearOverride verifies that Destroy clears
// the repository policy override by sending nil expiration and UNKNOWN enforcement.
func TestRepositoryPolicy_Adversarial_DestroyClearOverride(t *testing.T) {
	ctx := context.Background()

	t.Run("NormalDestroyClearOverride", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var getCallCount int
		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			getCallCount++
			if getCallCount == 1 {
				return &registryv1beta.GetRepositoryPolicyResponse{
					Revision:              35,
					Expiration:            durationpb.New(24 * time.Hour),
					ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
				}, nil
			}
			// Post-update verification read: policy override cleared (returning NotFound)
			return nil, status.Error(codes.NotFound, "repository policy override cleared")
		}

		var clearReq *registryv1beta.UpdateRepositoryPolicyRequest
		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			clearReq = req
			return &emptypb.Empty{}, nil
		}

		r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("payment-gateway"),
			Repository:            types.StringValue("payment-gateway"),
			Revision:              types.Int64Value(30),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("unexpected delete error: %v", resp.Diagnostics)
		}

		if clearReq == nil {
			t.Fatal("expected UpdateRepositoryPolicy to be called")
		}
		if clearReq.Repository != "payment-gateway" {
			t.Errorf("expected repository 'payment-gateway', got %q", clearReq.Repository)
		}
		if clearReq.Revision != 35 {
			t.Errorf("expected fresh revision 35, got %d", clearReq.Revision)
		}
		if clearReq.Expiration != nil {
			t.Errorf("expected nil expiration, got %v", clearReq.Expiration)
		}
		if clearReq.ExpirationEnforcement != registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN {
			t.Errorf("expected UNKNOWN enforcement, got %v", clearReq.ExpirationEnforcement)
		}
	})

	t.Run("DestroyWhenServerReturnsAnyPolicyResponse_FailsResetVerification", func(t *testing.T) {
		srv := testutil.NewMockServer()
		var getCallCount int
		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			getCallCount++
			if getCallCount == 1 {
				return &registryv1beta.GetRepositoryPolicyResponse{
					Revision:              35,
					Expiration:            durationpb.New(24 * time.Hour),
					ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_ENFORCE,
				}, nil
			}
			// Post-update verification read erroneously returns 200 OK policy (e.g. UNKNOWN / nil)
			return &registryv1beta.GetRepositoryPolicyResponse{
				Revision:              36,
				Expiration:            nil,
				ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN,
			}, nil
		}

		srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		}

		r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("payment-gateway"),
			Repository:            types.StringValue("payment-gateway"),
			Revision:              types.Int64Value(30),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if !resp.Diagnostics.HasError() {
			t.Fatal("expected diagnostic error when server returns 200 OK policy after delete, got none")
		}
		var foundError bool
		for _, diag := range resp.Diagnostics.Errors() {
			if diag.Summary() == "Registry Repository Policy Reset Verification Failed" {
				foundError = true
				break
			}
		}
		if !foundError {
			t.Fatalf("expected 'Registry Repository Policy Reset Verification Failed' diagnostic error, got: %v", resp.Diagnostics)
		}
	})

	t.Run("DestroyWhenRepositoryNotFound_SucceedsCleanly", func(t *testing.T) {
		srv := testutil.NewMockServer()
		srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
			return nil, status.Error(codes.NotFound, "repository no longer exists")
		}

		r, schemaResp := setupTestRepositoryPolicyResource(t, srv)

		stateModel := registry.RepositoryPolicyResourceModel{
			ID:                    types.StringValue("defunct-service"),
			Repository:            types.StringValue("defunct-service"),
			Revision:              types.Int64Value(1),
			Expiration:            types.StringValue("24h"),
			ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
		}
		state := tfsdk.State{Schema: schemaResp.Schema}
		_ = state.Set(ctx, &stateModel)

		req := resource.DeleteRequest{State: state}
		resp := &resource.DeleteResponse{State: state}
		r.Delete(ctx, req, resp)

		if resp.Diagnostics.HasError() {
			t.Fatalf("expected clean delete on NotFound, got: %v", resp.Diagnostics)
		}
	})
}

// =============================================================================
// GROUP 4: HIGH-CONCURRENCY RACE CONDITIONS & THREAD-SAFETY HARNESS
// =============================================================================

// TestRegistry_Adversarial_ConcurrentStressHarness executes simultaneous operations
// across all 3 registry resources in parallel goroutines to verify race condition freedom.
func TestRegistry_Adversarial_ConcurrentStressHarness(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var mu sync.Mutex
	sharedImages := make(map[string]*registryv1beta.SharedImage)
	repoPolicies := make(map[string]*registryv1beta.GetRepositoryPolicyResponse)
	defaultPolicy := &registryv1beta.GetDefaultPolicyResponse{
		Revision:              1,
		DefaultExpiration:     nil,
		ExpirationEnforcement: registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_DO_NOT_ENFORCE,
	}

	// Mock Shared Images
	srv.Registry.ShareImageFunc = func(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error) {
		mu.Lock()
		defer mu.Unlock()
		id := fmt.Sprintf("share_race_%d", len(sharedImages)+1)
		img := &registryv1beta.SharedImage{
			Id:               id,
			SharedRepository: "shared/" + req.Repository,
			SharedDigest:     req.Digest,
			ExpiresAt:        req.ExpiresAt,
			ImageRef:         "public.nscr.io/" + id,
			Visibility:       req.Visibility,
			Suffix:           req.Suffix,
		}
		sharedImages[id] = img
		return proto.Clone(img).(*registryv1beta.SharedImage), nil
	}

	srv.Registry.ListSharedImagesFunc = func(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		var list []*registryv1beta.SharedImage
		for _, img := range sharedImages {
			list = append(list, proto.Clone(img).(*registryv1beta.SharedImage))
		}
		return &registryv1beta.ListSharedImagesResponse{SharedImages: list}, nil
	}

	srv.Registry.UnshareImageFunc = func(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		delete(sharedImages, req.Id)
		return &emptypb.Empty{}, nil
	}

	// Mock Default Policy
	srv.Registry.GetDefaultPolicyFunc = func(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		return proto.Clone(defaultPolicy).(*registryv1beta.GetDefaultPolicyResponse), nil
	}

	srv.Registry.UpdateDefaultPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		if req.Revision != defaultPolicy.Revision {
			return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("revision mismatch: expected %d, got %d", defaultPolicy.Revision, req.Revision))
		}
		defaultPolicy.Revision++
		defaultPolicy.DefaultExpiration = req.DefaultExpiration
		defaultPolicy.ExpirationEnforcement = req.ExpirationEnforcement
		return &emptypb.Empty{}, nil
	}

	// Mock Repo Policies
	srv.Registry.GetRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		mu.Lock()
		defer mu.Unlock()
		p, exists := repoPolicies[req.Repository]
		if !exists {
			return nil, status.Error(codes.NotFound, "repository policy not found")
		}
		return proto.Clone(p).(*registryv1beta.GetRepositoryPolicyResponse), nil
	}

	srv.Registry.UpdateRepositoryPolicyFunc = func(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
		mu.Lock()
		defer mu.Unlock()
		p, exists := repoPolicies[req.Repository]
		var expectedRev int64
		if exists {
			expectedRev = p.Revision
		}
		if req.Revision != expectedRev {
			return nil, status.Error(codes.FailedPrecondition, fmt.Sprintf("revision mismatch: expected %d, got %d", expectedRev, req.Revision))
		}
		if req.ExpirationEnforcement == registryv1beta.ExpirationEnforcement_EXPIRATION_ENFORCEMENT_UNKNOWN && req.Expiration == nil {
			delete(repoPolicies, req.Repository)
			return &emptypb.Empty{}, nil
		}
		if !exists {
			p = &registryv1beta.GetRepositoryPolicyResponse{Revision: 0}
			repoPolicies[req.Repository] = p
		}
		p.Revision++
		p.Expiration = req.Expiration
		p.ExpirationEnforcement = req.ExpirationEnforcement
		return &emptypb.Empty{}, nil
	}

	sharedR, sharedSchema := setupTestSharedImageResource(t, srv)
	repoR, repoSchema := setupTestRepositoryPolicyResource(t, srv)

	const workerCount = 15
	var wg sync.WaitGroup
	wg.Add(workerCount)

	for i := 0; i < workerCount; i++ {
		workerID := i
		go func() {
			defer wg.Done()

			// 1. Shared Image Lifecycle
			planModel := registry.SharedImageResourceModel{
				ID:               types.StringUnknown(),
				Repository:       types.StringValue(fmt.Sprintf("race-service-%d", workerID)),
				Digest:           types.StringValue(fmt.Sprintf("sha256:%064d", workerID)),
				Visibility:       types.StringValue("PUBLIC"),
				ExpiresAt:        types.StringNull(),
				Suffix:           types.StringValue(fmt.Sprintf("tag-%d", workerID)),
				SharedRepository: types.StringUnknown(),
				SharedDigest:     types.StringUnknown(),
				ImageRef:         types.StringUnknown(),
			}

			planState := tfsdk.State{Schema: sharedSchema.Schema}
			_ = planState.Set(ctx, &planModel)

			createReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: sharedSchema.Schema, Raw: planState.Raw}}
			createResp := &resource.CreateResponse{State: tfsdk.State{Schema: sharedSchema.Schema}}
			sharedR.Create(ctx, createReq, createResp)
			if createResp.Diagnostics.HasError() {
				t.Errorf("worker %d shared image create failed: %v", workerID, createResp.Diagnostics)
				return
			}

			// Read
			readReq := resource.ReadRequest{State: createResp.State}
			readResp := &resource.ReadResponse{State: tfsdk.State{Schema: sharedSchema.Schema}}
			sharedR.Read(ctx, readReq, readResp)
			if readResp.Diagnostics.HasError() {
				t.Errorf("worker %d shared image read failed: %v", workerID, readResp.Diagnostics)
				return
			}

			// Delete
			delReq := resource.DeleteRequest{State: readResp.State}
			delResp := &resource.DeleteResponse{State: readResp.State}
			sharedR.Delete(ctx, delReq, delResp)
			if delResp.Diagnostics.HasError() {
				t.Errorf("worker %d shared image delete failed: %v", workerID, delResp.Diagnostics)
				return
			}

			// 2. Repository Policy Lifecycle
			repoPlanModel := registry.RepositoryPolicyResourceModel{
				ID:                    types.StringUnknown(),
				Repository:            types.StringValue(fmt.Sprintf("repo-worker-%d", workerID)),
				Revision:              types.Int64Unknown(),
				Expiration:            types.StringValue("24h"),
				ExpirationEnforcement: types.StringValue("EXPIRATION_ENFORCEMENT_ENFORCE"),
			}
			repoPlanState := tfsdk.State{Schema: repoSchema.Schema}
			_ = repoPlanState.Set(ctx, &repoPlanModel)

			repoCreateReq := resource.CreateRequest{Plan: tfsdk.Plan{Schema: repoSchema.Schema, Raw: repoPlanState.Raw}}
			repoCreateResp := &resource.CreateResponse{State: tfsdk.State{Schema: repoSchema.Schema}}
			repoR.Create(ctx, repoCreateReq, repoCreateResp)
			if repoCreateResp.Diagnostics.HasError() {
				t.Errorf("worker %d repo policy create failed: %v", workerID, repoCreateResp.Diagnostics)
				return
			}

			// Read
			repoReadReq := resource.ReadRequest{State: repoCreateResp.State}
			repoReadResp := &resource.ReadResponse{State: tfsdk.State{Schema: repoSchema.Schema}}
			repoR.Read(ctx, repoReadReq, repoReadResp)
			if repoReadResp.Diagnostics.HasError() {
				t.Errorf("worker %d repo policy read failed: %v", workerID, repoReadResp.Diagnostics)
				return
			}

			// Delete (Clear Override)
			repoDelReq := resource.DeleteRequest{State: repoReadResp.State}
			repoDelResp := &resource.DeleteResponse{State: repoReadResp.State}
			repoR.Delete(ctx, repoDelReq, repoDelResp)
			if repoDelResp.Diagnostics.HasError() {
				t.Errorf("worker %d repo policy delete failed: %v", workerID, repoDelResp.Diagnostics)
				return
			}
		}()
	}

	wg.Wait()
}
