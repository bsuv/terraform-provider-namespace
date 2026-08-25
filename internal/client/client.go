package client

import (
	"context"
	"errors"
	"fmt"

	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/builder/v1beta/builderv1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/compute/v1beta/computev1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/github/v1beta/githubv1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/iam/v1beta/iamv1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/integrations/bazel/v1beta/bazelv1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/integrations/gradle/v1beta/gradlev1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/integrations/httpcache/v1beta/httpcachev1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/registry/v1beta/registryv1betagrpc"
	"buf.build/gen/go/namespace/cloud/grpc/go/proto/namespace/cloud/vault/v1beta/vaultv1betagrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"namespacelabs.dev/integrations/api"
)

// Client is the primary client bundle supplying initialized domain service clients.
type Client struct {
	pool        *EndpointPool
	tokenSource api.TokenSource
}

// Config specifies initialization settings for the Client.
type Config struct {
	Token           string
	TokenFile       string
	GlobalEndpoint  string
	IAMEndpoint     string
	ComputeEndpoint string
	Version         string
	TokenSource     api.TokenSource                  // Optional override
	DialOptions     []grpc.DialOption                // Optional override
	Creds           credentials.TransportCredentials // Optional override (e.g. insecure for bufconn)
}

// New creates and initializes a Client.
func New(ctx context.Context, cfg Config) (*Client, error) {
	ts := cfg.TokenSource
	if ts == nil {
		resolvedTS, err := ResolveTokenSource(ctx, AuthConfig{
			Token:       cfg.Token,
			TokenFile:   cfg.TokenFile,
			IAMEndpoint: cfg.IAMEndpoint,
			Creds:       cfg.Creds,
			DialOptions: cfg.DialOptions,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to resolve authentication token: %w", err)
		}
		ts = resolvedTS
	}

	pool, err := NewEndpointPool(EndpointConfig{
		GlobalEndpoint:  cfg.GlobalEndpoint,
		IAMEndpoint:     cfg.IAMEndpoint,
		ComputeEndpoint: cfg.ComputeEndpoint,
		Version:         cfg.Version,
		TokenSource:     ts,
		DialOptions:     cfg.DialOptions,
		Creds:           cfg.Creds,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize endpoint pool: %w", err)
	}

	return &Client{
		pool:        pool,
		tokenSource: ts,
	}, nil
}

// Pool returns the underlying EndpointPool.
func (c *Client) Pool() *EndpointPool {
	return c.pool
}

// GlobalEndpoint returns the configured or resolved Global endpoint URL.
func (c *Client) GlobalEndpoint() string {
	if c.pool != nil {
		return c.pool.GlobalEndpoint()
	}
	return ""
}

// IAMEndpoint returns the configured or resolved IAM endpoint URL.
func (c *Client) IAMEndpoint() string {
	if c.pool != nil {
		return c.pool.IAMEndpoint()
	}
	return ""
}

// ComputeEndpoint returns the configured or resolved Compute endpoint URL.
func (c *Client) ComputeEndpoint() string {
	if c.pool != nil {
		return c.pool.ComputeEndpoint()
	}
	return ""
}

// Close closes all open connections and any closable token sources.
func (c *Client) Close() error {
	var errs []error
	if c.pool != nil {
		if err := c.pool.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if closer, ok := c.tokenSource.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ProfileClient returns a ProfileServiceClient connected to the Global endpoint.
func (c *Client) ProfileClient(ctx context.Context) (githubv1betagrpc.ProfileServiceClient, error) {
	conn, err := c.pool.GlobalConn(ctx)
	if err != nil {
		return nil, err
	}
	return githubv1betagrpc.NewProfileServiceClient(conn), nil
}

// ImageClient returns an ImageServiceClient connected to the Global endpoint.
func (c *Client) ImageClient(ctx context.Context) (githubv1betagrpc.ImageServiceClient, error) {
	conn, err := c.pool.GlobalConn(ctx)
	if err != nil {
		return nil, err
	}
	return githubv1betagrpc.NewImageServiceClient(conn), nil
}

// TenantClient returns a TenantServiceClient connected to the IAM endpoint.
func (c *Client) TenantClient(ctx context.Context) (iamv1betagrpc.TenantServiceClient, error) {
	conn, err := c.pool.IAMConn(ctx)
	if err != nil {
		return nil, err
	}
	return iamv1betagrpc.NewTenantServiceClient(conn), nil
}

// TenantMembershipClient returns a TenantMembershipServiceClient connected to the IAM endpoint.
func (c *Client) TenantMembershipClient(ctx context.Context) (iamv1betagrpc.TenantMembershipServiceClient, error) {
	conn, err := c.pool.IAMConn(ctx)
	if err != nil {
		return nil, err
	}
	return iamv1betagrpc.NewTenantMembershipServiceClient(conn), nil
}

// RegistryClient returns a ContainerRegistryServiceClient connected to the Global endpoint.
func (c *Client) RegistryClient(ctx context.Context) (registryv1betagrpc.ContainerRegistryServiceClient, error) {
	conn, err := c.pool.GlobalConn(ctx)
	if err != nil {
		return nil, err
	}
	return registryv1betagrpc.NewContainerRegistryServiceClient(conn), nil
}

// TokenClient returns a TokenServiceClient connected to the IAM endpoint.
func (c *Client) TokenClient(ctx context.Context) (iamv1betagrpc.TokenServiceClient, error) {
	conn, err := c.pool.IAMConn(ctx)
	if err != nil {
		return nil, err
	}
	return iamv1betagrpc.NewTokenServiceClient(conn), nil
}

// BazelCacheClient returns a BazelCacheServiceClient connected to the Global endpoint.
func (c *Client) BazelCacheClient(ctx context.Context) (bazelv1betagrpc.BazelCacheServiceClient, error) {
	conn, err := c.pool.GlobalConn(ctx)
	if err != nil {
		return nil, err
	}
	return bazelv1betagrpc.NewBazelCacheServiceClient(conn), nil
}

// GradleCacheClient returns a GradleCacheServiceClient connected to the Global endpoint.
func (c *Client) GradleCacheClient(ctx context.Context) (gradlev1betagrpc.GradleCacheServiceClient, error) {
	conn, err := c.pool.GlobalConn(ctx)
	if err != nil {
		return nil, err
	}
	return gradlev1betagrpc.NewGradleCacheServiceClient(conn), nil
}

// HttpCacheClient returns a HttpCacheServiceClient connected to the Global endpoint.
func (c *Client) HttpCacheClient(ctx context.Context) (httpcachev1betagrpc.HttpCacheServiceClient, error) {
	conn, err := c.pool.GlobalConn(ctx)
	if err != nil {
		return nil, err
	}
	return httpcachev1betagrpc.NewHttpCacheServiceClient(conn), nil
}

// StorageClient returns a StorageServiceClient connected to the Compute endpoint.
func (c *Client) StorageClient(ctx context.Context) (computev1betagrpc.StorageServiceClient, error) {
	conn, err := c.pool.ComputeConn(ctx)
	if err != nil {
		return nil, err
	}
	return computev1betagrpc.NewStorageServiceClient(conn), nil
}

// BuilderClient returns a BuilderServiceClient connected to the Compute endpoint.
func (c *Client) BuilderClient(ctx context.Context) (builderv1betagrpc.BuilderServiceClient, error) {
	conn, err := c.pool.ComputeConn(ctx)
	if err != nil {
		return nil, err
	}
	return builderv1betagrpc.NewBuilderServiceClient(conn), nil
}

// ComputeClient returns a ComputeServiceClient connected to the Compute endpoint.
func (c *Client) ComputeClient(ctx context.Context) (computev1betagrpc.ComputeServiceClient, error) {
	conn, err := c.pool.ComputeConn(ctx)
	if err != nil {
		return nil, err
	}
	return computev1betagrpc.NewComputeServiceClient(conn), nil
}

// VaultClient returns a VaultServiceClient connected to the Global endpoint.
func (c *Client) VaultClient(ctx context.Context) (vaultv1betagrpc.VaultServiceClient, error) {
	conn, err := c.pool.GlobalConn(ctx)
	if err != nil {
		return nil, err
	}
	return vaultv1betagrpc.NewVaultServiceClient(conn), nil
}
