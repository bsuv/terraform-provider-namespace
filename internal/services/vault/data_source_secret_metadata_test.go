package vault_test

import (
	"context"
	"strings"
	"testing"
	"time"

	vaultv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/vault/v1beta"
	stdlib "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/vault"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

func setupTestDataSourceSecretMetadata(t *testing.T, srv *testutil.MockServer) (datasource.DataSource, datasource.SchemaResponse) {
	t.Helper()
	cl := testutil.NewTestClient(t, srv)
	d := vault.NewSecretMetadataDataSource()

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

func TestSecretMetadataDataSource_Schema(t *testing.T) {
	ctx := context.Background()
	d := vault.NewSecretMetadataDataSource()

	var metaResp datasource.MetadataResponse
	d.Metadata(ctx, datasource.MetadataRequest{}, &metaResp)
	if metaResp.TypeName != "namespace_vault_secret_metadata" {
		t.Fatalf("expected type name 'namespace_vault_secret_metadata', got %q", metaResp.TypeName)
	}

	var schemaResp datasource.SchemaResponse
	d.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("unexpected schema errors: %v", schemaResp.Diagnostics)
	}

	attrs := schemaResp.Schema.Attributes
	for _, attrName := range []string{"id", "object_id", "description", "version", "created_at", "deleted_at", "labels"} {
		if _, ok := attrs[attrName]; !ok {
			t.Errorf("missing expected attribute %q", attrName)
		}
	}
}

func TestSecretMetadataDataSource_Configure_InvalidType(t *testing.T) {
	ctx := context.Background()
	d := vault.NewSecretMetadataDataSource()

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

func TestSecretMetadataDataSource_Read_SuccessAndSecurityEnforcement(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	createdTime := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	// STRICT SECURITY ENFORCEMENT:
	// DescribeObject must NEVER be invoked. If invoked, fail the test immediately.
	srv.Vault.DescribeObjectFunc = func(ctx context.Context, req *vaultv1beta.DescribeObjectRequest) (*vaultv1beta.DescribeObjectResponse, error) {
		t.Fatalf("CRITICAL SECURITY VIOLATION: DescribeObject was invoked for secret %q; metadata data source must use ListObjects only", req.GetObjectId())
		return nil, status.Errorf(codes.PermissionDenied, "DescribeObject is strictly forbidden")
	}

	srv.Vault.ListObjectsFunc = func(ctx context.Context, req *vaultv1beta.ListObjectsRequest) (*vaultv1beta.ListObjectsResponse, error) {
		return &vaultv1beta.ListObjectsResponse{
			Objects: []*vaultv1beta.VaultObjectMetadata{
				{
					ObjectId:    "sec-other",
					Description: "other secret",
					Version:     "v1",
					CreatedAt:   timestamppb.New(createdTime),
				},
				{
					ObjectId:    "sec-target",
					Description: "Target production secret metadata",
					Version:     "v3_opt_concurrency",
					CreatedAt:   timestamppb.New(createdTime),
					Labels: []*stdlib.Label{
						{Name: "env", Value: "prod"},
						{Name: "team", Value: "platform"},
					},
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceSecretMetadata(t, srv)

	cfg := vault.SecretMetadataDataSourceModel{
		ID:          types.StringNull(),
		ObjectID:    types.StringValue("sec-target"),
		Description: types.StringNull(),
		Version:     types.StringNull(),
		CreatedAt:   types.StringNull(),
		DeletedAt:   types.StringNull(),
		Labels:      types.MapNull(types.StringType),
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read diagnostics: %v", resp.Diagnostics)
	}

	var state vault.SecretMetadataDataSourceModel
	if diags := resp.State.Get(ctx, &state); diags.HasError() {
		t.Fatalf("unexpected state get diagnostics: %v", diags)
	}

	if state.ID.ValueString() != "sec-target" {
		t.Errorf("expected ID 'sec-target', got %q", state.ID.ValueString())
	}
	if state.ObjectID.ValueString() != "sec-target" {
		t.Errorf("expected ObjectID 'sec-target', got %q", state.ObjectID.ValueString())
	}
	if state.Description.ValueString() != "Target production secret metadata" {
		t.Errorf("expected Description 'Target production secret metadata', got %q", state.Description.ValueString())
	}
	if state.Version.ValueString() != "v3_opt_concurrency" {
		t.Errorf("expected Version 'v3_opt_concurrency', got %q", state.Version.ValueString())
	}

	var labelsMap map[string]string
	if diags := state.Labels.ElementsAs(ctx, &labelsMap, false); diags.HasError() {
		t.Fatalf("failed to convert labels map: %v", diags)
	}
	if labelsMap["env"] != "prod" || labelsMap["team"] != "platform" {
		t.Errorf("unexpected labels map: %v", labelsMap)
	}
}

func TestSecretMetadataDataSource_Read_ZeroMatch_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Vault.ListObjectsFunc = func(ctx context.Context, req *vaultv1beta.ListObjectsRequest) (*vaultv1beta.ListObjectsResponse, error) {
		return &vaultv1beta.ListObjectsResponse{
			Objects: []*vaultv1beta.VaultObjectMetadata{},
		}, nil
	}

	d, schemaResp := setupTestDataSourceSecretMetadata(t, srv)

	cfg := vault.SecretMetadataDataSourceModel{
		ID:          types.StringNull(),
		ObjectID:    types.StringValue("non-existent-secret"),
		Description: types.StringNull(),
		Version:     types.StringNull(),
		CreatedAt:   types.StringNull(),
		DeletedAt:   types.StringNull(),
		Labels:      types.MapNull(types.StringType),
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for zero-match secret")
	}

	errMsg := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(errMsg, "Vault Secret Metadata Not Found") {
		t.Errorf("expected summary containing 'Vault Secret Metadata Not Found', got %q", errMsg)
	}
}

func TestSecretMetadataDataSource_Read_MultipleMatches(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Vault.ListObjectsFunc = func(ctx context.Context, req *vaultv1beta.ListObjectsRequest) (*vaultv1beta.ListObjectsResponse, error) {
		return &vaultv1beta.ListObjectsResponse{
			Objects: []*vaultv1beta.VaultObjectMetadata{
				{ObjectId: "duplicate-id", Version: "v1"},
				{ObjectId: "duplicate-id", Version: "v2"},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceSecretMetadata(t, srv)

	cfg := vault.SecretMetadataDataSourceModel{
		ID:          types.StringNull(),
		ObjectID:    types.StringValue("duplicate-id"),
		Description: types.StringNull(),
		Version:     types.StringNull(),
		CreatedAt:   types.StringNull(),
		DeletedAt:   types.StringNull(),
		Labels:      types.MapNull(types.StringType),
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for duplicate match secrets")
	}

	errMsg := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(errMsg, "Multiple Vault Secret Objects Found") {
		t.Errorf("expected summary containing 'Multiple Vault Secret Objects Found', got %q", errMsg)
	}
}

func TestSecretMetadataDataSource_Read_SoftDeletedSecret_NotFound(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	srv.Vault.ListObjectsFunc = func(ctx context.Context, req *vaultv1beta.ListObjectsRequest) (*vaultv1beta.ListObjectsResponse, error) {
		return &vaultv1beta.ListObjectsResponse{
			Objects: []*vaultv1beta.VaultObjectMetadata{
				{
					ObjectId:  "deleted-secret-id",
					Version:   "v1",
					CreatedAt: timestamppb.Now(),
					DeletedAt: timestamppb.Now(), // Soft-deleted!
				},
			},
		}, nil
	}

	d, schemaResp := setupTestDataSourceSecretMetadata(t, srv)

	cfg := vault.SecretMetadataDataSourceModel{
		ID:          types.StringNull(),
		ObjectID:    types.StringValue("deleted-secret-id"),
		Description: types.StringNull(),
		Version:     types.StringNull(),
		CreatedAt:   types.StringNull(),
		DeletedAt:   types.StringNull(),
		Labels:      types.MapNull(types.StringType),
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw},
	}
	resp := &datasource.ReadResponse{
		State: tfsdk.State{Schema: schemaResp.Schema},
	}

	d.Read(ctx, req, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected error diagnostic for soft-deleted secret, but got success")
	}

	errMsg := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(errMsg, "Vault Secret Metadata Not Found") {
		t.Errorf("expected summary containing 'Vault Secret Metadata Not Found', got %q", errMsg)
	}
}
