package integrations

import (
	"context"

	bazelv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/bazel/v1beta"
	gradlev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/gradle/v1beta"
	httpcachev1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/integrations/httpcache/v1beta"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

// ServiceClient defines the gRPC client operations required by integrations/cache services.
type ServiceClient interface {
	EnsureBazelCache(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error)
	EnsureGradleCache(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error)
	EnsureHttpCache(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error)
}

// DomainClient adapts *client.Client to ServiceClient applying RPC policies.
type DomainClient struct {
	client *client.Client
}

// NewDomainClient creates a new DomainClient wrapping the provider Client bundle.
func NewDomainClient(c *client.Client) *DomainClient {
	return &DomainClient{client: c}
}

// EnsureBazelCache ensures Bazel remote cache infrastructure with fail-fast mutating deadline policy (60s).
func (d *DomainClient) EnsureBazelCache(ctx context.Context, req *bazelv1beta.EnsureBazelCacheRequest) (*bazelv1beta.EnsureBazelCacheResponse, error) {
	cli, err := d.client.BazelCacheClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*bazelv1beta.EnsureBazelCacheResponse, error) {
		return cli.EnsureBazelCache(callCtx, req)
	})
}

// EnsureGradleCache ensures Gradle remote cache infrastructure with fail-fast mutating deadline policy (60s).
func (d *DomainClient) EnsureGradleCache(ctx context.Context, req *gradlev1beta.EnsureGradleCacheRequest) (*gradlev1beta.EnsureGradleCacheResponse, error) {
	cli, err := d.client.GradleCacheClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*gradlev1beta.EnsureGradleCacheResponse, error) {
		return cli.EnsureGradleCache(callCtx, req)
	})
}

// EnsureHttpCache ensures generic HTTP remote build cache infrastructure with fail-fast mutating deadline policy (60s).
func (d *DomainClient) EnsureHttpCache(ctx context.Context, req *httpcachev1beta.EnsureHttpCacheRequest) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
	cli, err := d.client.HttpCacheClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*httpcachev1beta.EnsureHttpCacheResponse, error) {
		return cli.EnsureHttpCache(callCtx, req)
	})
}
