package vault

import (
	"context"

	vaultv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/vault/v1beta"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

// ServiceClient defines the gRPC client operations required by Vault services.
// Note: DescribeObject is intentionally omitted from the interface to enforce metadata-only security.
type ServiceClient interface {
	ListObjects(ctx context.Context, req *vaultv1beta.ListObjectsRequest) (*vaultv1beta.ListObjectsResponse, error)
}

// DomainClient adapts *client.Client to ServiceClient applying RPC policies.
type DomainClient struct {
	client *client.Client
}

// NewDomainClient creates a new DomainClient wrapping the provider Client bundle.
func NewDomainClient(c *client.Client) *DomainClient {
	return &DomainClient{client: c}
}

// ListObjects retrieves secret metadata list with idempotent read retry policy.
func (d *DomainClient) ListObjects(ctx context.Context, req *vaultv1beta.ListObjectsRequest) (*vaultv1beta.ListObjectsResponse, error) {
	cli, err := d.client.VaultClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*vaultv1beta.ListObjectsResponse, error) {
		return cli.ListObjects(callCtx, req)
	})
}
