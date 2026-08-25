package registry

import (
	"context"

	registryv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/registry/v1beta"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

// ServiceClient defines the gRPC client operations required by Container Registry services.
type ServiceClient interface {
	ShareImage(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error)
	UnshareImage(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error)
	ListSharedImages(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error)
	GetDefaultPolicy(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error)
	UpdateDefaultPolicy(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error)
	GetRepositoryPolicy(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error)
	UpdateRepositoryPolicy(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error)
	GetImage(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error)
	ListImages(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error)
}

// DomainClient adapts *client.Client to ServiceClient applying RPC policies.
type DomainClient struct {
	client *client.Client
}

// NewDomainClient creates a new DomainClient wrapping the provider Client bundle.
func NewDomainClient(c *client.Client) *DomainClient {
	return &DomainClient{client: c}
}

// ShareImage creates an external shared image reference with fail-fast mutating deadline policy.
func (d *DomainClient) ShareImage(ctx context.Context, req *registryv1beta.ShareImageRequest) (*registryv1beta.SharedImage, error) {
	cli, err := d.client.RegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*registryv1beta.SharedImage, error) {
		return cli.ShareImage(callCtx, req)
	})
}

// UnshareImage revokes an external shared image reference with fail-fast mutating deadline policy.
func (d *DomainClient) UnshareImage(ctx context.Context, req *registryv1beta.UnshareImageRequest) (*emptypb.Empty, error) {
	cli, err := d.client.RegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*emptypb.Empty, error) {
		return cli.UnshareImage(callCtx, req)
	})
}

// ListSharedImages lists active shared image references with idempotent read retry policy.
func (d *DomainClient) ListSharedImages(ctx context.Context, req *registryv1beta.ListSharedImagesRequest) (*registryv1beta.ListSharedImagesResponse, error) {
	cli, err := d.client.RegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*registryv1beta.ListSharedImagesResponse, error) {
		return cli.ListSharedImages(callCtx, req)
	})
}

// GetDefaultPolicy retrieves default retention policy with idempotent read retry policy.
func (d *DomainClient) GetDefaultPolicy(ctx context.Context, req *emptypb.Empty) (*registryv1beta.GetDefaultPolicyResponse, error) {
	cli, err := d.client.RegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*registryv1beta.GetDefaultPolicyResponse, error) {
		return cli.GetDefaultPolicy(callCtx, req)
	})
}

// UpdateDefaultPolicy updates default retention policy with fail-fast mutating deadline policy.
func (d *DomainClient) UpdateDefaultPolicy(ctx context.Context, req *registryv1beta.UpdateDefaultPolicyRequest) (*emptypb.Empty, error) {
	cli, err := d.client.RegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*emptypb.Empty, error) {
		return cli.UpdateDefaultPolicy(callCtx, req)
	})
}

// GetRepositoryPolicy retrieves a repository retention override with idempotent read retry policy.
func (d *DomainClient) GetRepositoryPolicy(ctx context.Context, req *registryv1beta.GetRepositoryPolicyRequest) (*registryv1beta.GetRepositoryPolicyResponse, error) {
	cli, err := d.client.RegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*registryv1beta.GetRepositoryPolicyResponse, error) {
		return cli.GetRepositoryPolicy(callCtx, req)
	})
}

// UpdateRepositoryPolicy updates a repository retention override with fail-fast mutating deadline policy.
func (d *DomainClient) UpdateRepositoryPolicy(ctx context.Context, req *registryv1beta.UpdateRepositoryPolicyRequest) (*emptypb.Empty, error) {
	cli, err := d.client.RegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*emptypb.Empty, error) {
		return cli.UpdateRepositoryPolicy(callCtx, req)
	})
}

// GetImage retrieves image details by tag or digest with idempotent read retry policy.
func (d *DomainClient) GetImage(ctx context.Context, req *registryv1beta.GetImageRequest) (*registryv1beta.GetImageResponse, error) {
	cli, err := d.client.RegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*registryv1beta.GetImageResponse, error) {
		return cli.GetImage(callCtx, req)
	})
}

// ListImages lists container images in a repository with the bounded paginated-read policy.
func (d *DomainClient) ListImages(ctx context.Context, req *registryv1beta.ListImagesRequest) (*registryv1beta.ListImagesResponse, error) {
	cli, err := d.client.RegistryClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecutePaginatedRead(ctx, func(callCtx context.Context) (*registryv1beta.ListImagesResponse, error) {
		return cli.ListImages(callCtx, req)
	})
}
