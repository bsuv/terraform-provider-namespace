package vault_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
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

// TestVault_Adversarial_ConcurrentStressAndStrictSecurity asserts that under concurrent load:
// 1. DescribeObject is NEVER called across 100 concurrent requests.
// 2. All 100 concurrent lookups resolve their respective secret metadata correctly.
func TestVault_Adversarial_ConcurrentStressAndStrictSecurity(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	createdTime := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	// Zero-tolerance guard on DescribeObject
	var describeCalled atomic.Bool
	srv.Vault.DescribeObjectFunc = func(ctx context.Context, req *vaultv1beta.DescribeObjectRequest) (*vaultv1beta.DescribeObjectResponse, error) {
		describeCalled.Store(true)
		return nil, status.Errorf(codes.PermissionDenied, "DescribeObject is strictly forbidden")
	}

	numSecrets := 100
	var objects []*vaultv1beta.VaultObjectMetadata
	for i := 0; i < numSecrets; i++ {
		objects = append(objects, &vaultv1beta.VaultObjectMetadata{
			ObjectId:    fmt.Sprintf("sec-%03d", i),
			Description: fmt.Sprintf("Secret description %d", i),
			Version:     fmt.Sprintf("v%d", i+1),
			CreatedAt:   timestamppb.New(createdTime),
			Labels: []*stdlib.Label{
				{Name: "index", Value: fmt.Sprintf("%d", i)},
			},
		})
	}

	srv.Vault.ListObjectsFunc = func(ctx context.Context, req *vaultv1beta.ListObjectsRequest) (*vaultv1beta.ListObjectsResponse, error) {
		return &vaultv1beta.ListObjectsResponse{
			Objects: objects,
		}, nil
	}

	ds, schemaResp := setupTestDataSourceSecretMetadata(t, srv)

	var wg sync.WaitGroup
	var successCount atomic.Int64

	for i := 0; i < numSecrets; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			targetID := fmt.Sprintf("sec-%03d", idx)
			cfg := vault.SecretMetadataDataSourceModel{
				ID:          types.StringNull(),
				ObjectID:    types.StringValue(targetID),
				Description: types.StringNull(),
				Version:     types.StringNull(),
				CreatedAt:   types.StringNull(),
				DeletedAt:   types.StringNull(),
				Labels:      types.MapNull(types.StringType),
			}
			st := tfsdk.State{Schema: schemaResp.Schema}
			_ = st.Set(ctx, &cfg)
			req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: st.Raw}}
			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			ds.Read(ctx, req, resp)

			if !resp.Diagnostics.HasError() {
				var state vault.SecretMetadataDataSourceModel
				if diags := resp.State.Get(ctx, &state); !diags.HasError() {
					if state.ObjectID.ValueString() == targetID && state.Version.ValueString() == fmt.Sprintf("v%d", idx+1) {
						successCount.Add(1)
					}
				}
			}
		}(i)
	}

	wg.Wait()

	if describeCalled.Load() {
		t.Fatal("CRITICAL SECURITY VIOLATION: DescribeObject was called during data source execution")
	}

	if successCount.Load() != int64(numSecrets) {
		t.Errorf("expected %d successful secret metadata lookups, got %d", numSecrets, successCount.Load())
	}
}
