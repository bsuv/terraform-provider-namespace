package compute

import (
	"context"

	builderv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/builder/v1beta"
	computev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/compute/v1beta"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

// ServiceClient defines the gRPC client operations required by Compute, Storage, and Builder services.
type ServiceClient interface {
	GetVolumeTagSummary(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error)
	DescribePersistentVolume(ctx context.Context, req *computev1beta.DescribePersistentVolumeRequest) (*computev1beta.DescribePersistentVolumeResponse, error)
	GetBuilderConfiguration(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error)
	GetBuilderUsage(ctx context.Context, req *builderv1beta.GetBuilderUsageRequest) (*builderv1beta.GetBuilderUsageResponse, error)
	GetNetworkConfiguration(ctx context.Context, req *computev1beta.GetNetworkConfigurationRequest) (*computev1beta.GetNetworkConfigurationResponse, error)
}

// DomainClient adapts *client.Client to ServiceClient applying RPC policies.
type DomainClient struct {
	client *client.Client
}

// NewDomainClient creates a new DomainClient wrapping the provider Client bundle.
func NewDomainClient(c *client.Client) *DomainClient {
	return &DomainClient{client: c}
}

// GetVolumeTagSummary retrieves volume tag summary with idempotent read retry policy.
func (d *DomainClient) GetVolumeTagSummary(ctx context.Context, req *computev1beta.GetVolumeTagSummaryRequest) (*computev1beta.GetVolumeTagSummaryResponse, error) {
	cli, err := d.client.StorageClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*computev1beta.GetVolumeTagSummaryResponse, error) {
		return cli.GetVolumeTagSummary(callCtx, req)
	})
}

// DescribePersistentVolume describes a persistent volume with idempotent read retry policy.
func (d *DomainClient) DescribePersistentVolume(ctx context.Context, req *computev1beta.DescribePersistentVolumeRequest) (*computev1beta.DescribePersistentVolumeResponse, error) {
	cli, err := d.client.StorageClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*computev1beta.DescribePersistentVolumeResponse, error) {
		return cli.DescribePersistentVolume(callCtx, req)
	})
}

// GetBuilderConfiguration retrieves builder configuration with idempotent read retry policy.
func (d *DomainClient) GetBuilderConfiguration(ctx context.Context, req *builderv1beta.GetBuilderConfigurationRequest) (*builderv1beta.GetBuilderConfigurationResponse, error) {
	cli, err := d.client.BuilderClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*builderv1beta.GetBuilderConfigurationResponse, error) {
		return cli.GetBuilderConfiguration(callCtx, req)
	})
}

// GetBuilderUsage retrieves builder usage metrics with idempotent read retry policy.
func (d *DomainClient) GetBuilderUsage(ctx context.Context, req *builderv1beta.GetBuilderUsageRequest) (*builderv1beta.GetBuilderUsageResponse, error) {
	cli, err := d.client.BuilderClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*builderv1beta.GetBuilderUsageResponse, error) {
		return cli.GetBuilderUsage(callCtx, req)
	})
}

// GetNetworkConfiguration retrieves network egress configuration with idempotent read retry policy.
func (d *DomainClient) GetNetworkConfiguration(ctx context.Context, req *computev1beta.GetNetworkConfigurationRequest) (*computev1beta.GetNetworkConfigurationResponse, error) {
	cli, err := d.client.ComputeClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*computev1beta.GetNetworkConfigurationResponse, error) {
		return cli.GetNetworkConfiguration(callCtx, req)
	})
}
