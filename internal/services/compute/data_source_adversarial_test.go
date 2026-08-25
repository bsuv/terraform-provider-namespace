package compute_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	builderv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/builder/v1beta"
	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"
	stdtime "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/stdlib/time"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/bsuv/terraform-provider-namespace/internal/services/compute"
	"github.com/bsuv/terraform-provider-namespace/internal/testutil"
)

// TestCompute_Adversarial_ConcurrentStress tests parallel reads across all 5 compute data sources concurrently.
func TestCompute_Adversarial_ConcurrentStress(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	createdTime := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	srv.Storage.GetVolumeTagSummaryFunc = func(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error) {
		return &computev1beta.GetVolumeTagSummaryResponse{
			Summary: &computev1beta.TagSummary{
				Tag:       req.GetTag(),
				SizeMb:    10240,
				CreatedAt: timestamppb.New(createdTime),
			},
		}, nil
	}

	srv.Storage.DescribePersistentVolumeFunc = func(ctx context.Context, req *computev1beta.DescribePersistentVolumeRequest) (*computev1beta.DescribePersistentVolumeResponse, error) {
		return &computev1beta.DescribePersistentVolumeResponse{
			Volume: &computev1beta.PersistentVolume{
				Id:        req.GetId(),
				Tag:       req.GetTag(),
				SizeMb:    20480,
				CreatedAt: timestamppb.New(createdTime),
			},
		}, nil
	}

	srv.Builder.GetBuilderConfigurationFunc = func(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error) {
		return &builderv1beta.GetBuilderConfigurationResponse{
			BuildkitEndpoint: "builder.namespace.so:443",
			CacheSizeMb:      51200,
		}, nil
	}

	srv.Builder.GetBuilderUsageFunc = func(ctx context.Context, req *builderv1beta.GetBuilderUsageRequest) (*builderv1beta.GetBuilderUsageResponse, error) {
		return &builderv1beta.GetBuilderUsageResponse{
			Total: &builderv1beta.GetBuilderUsageResponse_Usage{
				BuildCount: 100,
			},
		}, nil
	}

	srv.Compute.GetNetworkConfigurationFunc = func(ctx context.Context, req *computev1beta.GetNetworkConfigurationRequest) (*computev1beta.GetNetworkConfigurationResponse, error) {
		return &computev1beta.GetNetworkConfigurationResponse{
			Egress: &computev1beta.GetNetworkConfigurationResponse_Egress{
				EgressCidrs: []*computev1beta.GetNetworkConfigurationResponse_CIDR{
					{Cidr: "192.0.2.0/24", Kind: computev1beta.GetNetworkConfigurationResponse_CIDR_NAMESPACE_MANAGED},
				},
			},
		}, nil
	}

	dsCacheVol, schemaCacheVol := setupTestDataSourceCacheVolumeSummary(t, srv)
	dsPersistVol, schemaPersistVol := setupTestDataSourcePersistentVolume(t, srv)
	dsBuilderCfg, schemaBuilderCfg := setupTestDataSourceBuilderConfiguration(t, srv)
	dsBuilderUsage, schemaBuilderUsage := setupTestDataSourceBuilderUsage(t, srv)
	dsNetworkCfg, schemaNetworkCfg := setupTestDataSourceNetworkConfiguration(t, srv)

	var wg sync.WaitGroup
	workers := 50
	var successCount atomic.Int64

	for i := 0; i < workers; i++ {
		wg.Add(5)

		// 1. Cache Volume Summary
		go func(idx int) {
			defer wg.Done()
			cfg := compute.CacheVolumeSummaryDataSourceModel{
				ID:          types.StringNull(),
				Tag:         types.StringValue(fmt.Sprintf("tag-%d", idx)),
				SizeMb:      types.Int64Null(),
				CreatedAt:   types.StringNull(),
				Generations: nil,
			}
			st := tfsdk.State{Schema: schemaCacheVol.Schema}
			_ = st.Set(ctx, &cfg)
			req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaCacheVol.Schema, Raw: st.Raw}}
			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaCacheVol.Schema}}
			dsCacheVol.Read(ctx, req, resp)
			if !resp.Diagnostics.HasError() {
				successCount.Add(1)
			}
		}(i)

		// 2. Persistent Volume
		go func(idx int) {
			defer wg.Done()
			cfg := compute.PersistentVolumeDataSourceModel{
				ID:             types.StringValue(fmt.Sprintf("vol-%d", idx)),
				Tag:            types.StringNull(),
				Site:           types.StringNull(),
				SizeMb:         types.Int64Null(),
				CreatedAt:      types.StringNull(),
				DestroyedAt:    types.StringNull(),
				AttachedTo:     types.StringNull(),
				ReleasedAt:     types.StringNull(),
				ReleasedReason: types.StringNull(),
			}
			st := tfsdk.State{Schema: schemaPersistVol.Schema}
			_ = st.Set(ctx, &cfg)
			req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaPersistVol.Schema, Raw: st.Raw}}
			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaPersistVol.Schema}}
			dsPersistVol.Read(ctx, req, resp)
			if !resp.Diagnostics.HasError() {
				successCount.Add(1)
			}
		}(i)

		// 3. Builder Configuration
		go func(idx int) {
			defer wg.Done()
			cfg := compute.BuilderConfigurationDataSourceModel{
				ID:                   types.StringNull(),
				Platform:             types.StringValue("linux/amd64"),
				BuilderName:          types.StringValue(fmt.Sprintf("builder-%d", idx)),
				BuildkitEndpoint:     types.StringNull(),
				FullBuildkitEndpoint: types.StringNull(),
				ServerCaPem:          types.StringNull(),
				CacheSizeMb:          types.Int64Null(),
				Shape:                nil,
			}
			st := tfsdk.State{Schema: schemaBuilderCfg.Schema}
			_ = st.Set(ctx, &cfg)
			req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaBuilderCfg.Schema, Raw: st.Raw}}
			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaBuilderCfg.Schema}}
			dsBuilderCfg.Read(ctx, req, resp)
			if !resp.Diagnostics.HasError() {
				successCount.Add(1)
			}
		}(i)

		// 4. Builder Usage
		go func(idx int) {
			defer wg.Done()
			cfg := compute.BuilderUsageDataSourceModel{
				ID:           types.StringNull(),
				PeriodStart:  types.StringValue("2026-08-01"),
				PeriodEnd:    types.StringValue("2026-08-21"),
				Total:        nil,
				PendingTotal: nil,
				PerDay:       nil,
			}
			st := tfsdk.State{Schema: schemaBuilderUsage.Schema}
			_ = st.Set(ctx, &cfg)
			req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaBuilderUsage.Schema, Raw: st.Raw}}
			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaBuilderUsage.Schema}}
			dsBuilderUsage.Read(ctx, req, resp)
			if !resp.Diagnostics.HasError() {
				successCount.Add(1)
			}
		}(i)

		// 5. Network Configuration
		go func(idx int) {
			defer wg.Done()
			cfg := compute.NetworkConfigurationDataSourceModel{
				ID:          types.StringNull(),
				EgressCidrs: nil,
			}
			st := tfsdk.State{Schema: schemaNetworkCfg.Schema}
			_ = st.Set(ctx, &cfg)
			req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaNetworkCfg.Schema, Raw: st.Raw}}
			resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaNetworkCfg.Schema}}
			dsNetworkCfg.Read(ctx, req, resp)
			if !resp.Diagnostics.HasError() {
				successCount.Add(1)
			}
		}(i)
	}

	wg.Wait()

	expectedSuccesses := int64(workers * 5)
	if successCount.Load() != expectedSuccesses {
		t.Errorf("expected %d successes, got %d", expectedSuccesses, successCount.Load())
	}
}

// TestCompute_Adversarial_TransientRetryResilience verifies that idempotent read retry policy
// recovers from transient Unavailable errors.
func TestCompute_Adversarial_TransientRetryResilience(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var attempts atomic.Int32
	srv.Storage.GetVolumeTagSummaryFunc = func(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error) {
		count := attempts.Add(1)
		if count < 3 {
			return nil, status.Errorf(codes.Unavailable, "transient network glitch attempt %d", count)
		}
		return &computev1beta.GetVolumeTagSummaryResponse{
			Summary: &computev1beta.TagSummary{
				Tag:    "retry-tag",
				SizeMb: 512,
			},
		}, nil
	}

	ds, schemaResp := setupTestDataSourceCacheVolumeSummary(t, srv)

	cfg := compute.CacheVolumeSummaryDataSourceModel{
		ID:          types.StringNull(),
		Tag:         types.StringValue("retry-tag"),
		SizeMb:      types.Int64Null(),
		CreatedAt:   types.StringNull(),
		Generations: nil,
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

	ds.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected read errors after transient retry: %v", resp.Diagnostics)
	}

	if attempts.Load() != 3 {
		t.Errorf("expected exactly 3 attempts, got %d", attempts.Load())
	}
}

// TestCompute_Adversarial_BuilderUsage_ExtremeDates tests leap years and boundary dates.
func TestCompute_Adversarial_BuilderUsage_ExtremeDates(t *testing.T) {
	ctx := context.Background()
	srv := testutil.NewMockServer()

	var capturedStart, capturedEnd *stdtime.Date
	srv.Builder.GetBuilderUsageFunc = func(ctx context.Context, req *builderv1beta.GetBuilderUsageRequest) (*builderv1beta.GetBuilderUsageResponse, error) {
		capturedStart = req.GetPeriodStart()
		capturedEnd = req.GetPeriodEnd()
		return &builderv1beta.GetBuilderUsageResponse{
			Total: &builderv1beta.GetBuilderUsageResponse_Usage{
				BuildCount: 42,
			},
		}, nil
	}

	ds, schemaResp := setupTestDataSourceBuilderUsage(t, srv)

	// Leap year test: 2028-02-29
	cfg := compute.BuilderUsageDataSourceModel{
		ID:           types.StringNull(),
		PeriodStart:  types.StringValue("2028-02-01"),
		PeriodEnd:    types.StringValue("2028-02-29"),
		Total:        nil,
		PendingTotal: nil,
		PerDay:       nil,
	}

	configState := tfsdk.State{Schema: schemaResp.Schema}
	if diags := configState.Set(ctx, &cfg); diags.HasError() {
		t.Fatalf("failed to set config state: %v", diags)
	}

	req := datasource.ReadRequest{Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: configState.Raw}}
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: schemaResp.Schema}}

	ds.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected error for valid leap year date: %v", resp.Diagnostics)
	}

	if capturedStart.GetDay() != 1 || capturedEnd.GetDay() != 29 {
		t.Errorf("unexpected dates: start=%v, end=%v", capturedStart, capturedEnd)
	}
}
