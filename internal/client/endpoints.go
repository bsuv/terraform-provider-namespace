package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"strings"
	"sync"

	endpointutil "github.com/bsuv/terraform-provider-namespace/internal/endpoint"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"namespacelabs.dev/integrations/api"
)

const (
	// DefaultGlobalEndpoint is the documented public Global endpoint.
	DefaultGlobalEndpoint = "https://global.namespaceapis.com"
	// DefaultIAMEndpoint is the documented public IAM endpoint.
	DefaultIAMEndpoint = "https://iam.namespaceapis.com"
)

// EndpointPool manages gRPC connections for Global, IAM, and Compute domains.
type EndpointPool struct {
	mu sync.Mutex

	globalEndpoint  string
	iamEndpoint     string
	computeEndpoint string
	version         string

	tokenSource api.TokenSource
	dialOpts    []grpc.DialOption
	creds       credentials.TransportCredentials

	globalConn  *grpc.ClientConn
	iamConn     *grpc.ClientConn
	computeConn *grpc.ClientConn
}

// EndpointConfig specifies the connection parameters for the endpoint pool.
type EndpointConfig struct {
	GlobalEndpoint  string
	IAMEndpoint     string
	ComputeEndpoint string
	Version         string
	TokenSource     api.TokenSource
	DialOptions     []grpc.DialOption
	Creds           credentials.TransportCredentials // Optional, defaults to TLS
}

// NewEndpointPool initializes a new EndpointPool with resolved endpoints.
func NewEndpointPool(cfg EndpointConfig) (*EndpointPool, error) {
	globalEP := strings.TrimSpace(cfg.GlobalEndpoint)
	if globalEP == "" {
		if envGlobal := strings.TrimSpace(os.Getenv("NSC_GLOBAL_ENDPOINT")); envGlobal != "" {
			globalEP = envGlobal
		} else {
			globalEP = DefaultGlobalEndpoint
		}
	}
	if err := ValidateEndpoint(globalEP); err != nil {
		return nil, fmt.Errorf("invalid global endpoint: %w", err)
	}

	iamEP := strings.TrimSpace(cfg.IAMEndpoint)
	if iamEP == "" {
		if envIAM := strings.TrimSpace(os.Getenv("NSC_IAM_ENDPOINT")); envIAM != "" {
			iamEP = envIAM
		} else {
			iamEP = DefaultIAMEndpoint
		}
	}
	if err := ValidateEndpoint(iamEP); err != nil {
		return nil, fmt.Errorf("invalid IAM endpoint: %w", err)
	}

	computeEP := strings.TrimSpace(cfg.ComputeEndpoint)
	if computeEP == "" {
		computeEP = strings.TrimSpace(os.Getenv("NSC_ENDPOINT"))
	}
	if computeEP != "" {
		if err := ValidateEndpoint(computeEP); err != nil {
			return nil, fmt.Errorf("invalid compute endpoint: %w", err)
		}
	}

	creds := cfg.Creds
	if creds == nil {
		creds = credentials.NewTLS(&tls.Config{})
	}

	version := cfg.Version
	if version == "" {
		version = "dev"
	}

	return &EndpointPool{
		globalEndpoint:  globalEP,
		iamEndpoint:     iamEP,
		computeEndpoint: computeEP,
		version:         version,
		tokenSource:     cfg.TokenSource,
		dialOpts:        cfg.DialOptions,
		creds:           creds,
	}, nil
}

// ValidateEndpoint checks whether the endpoint format is valid (rejects plain http:// scheme, userinfo, queries, fragments).
func ValidateEndpoint(endpoint string) error {
	return endpointutil.Validate(endpoint)
}

// GlobalConn returns or creates the client connection to the Global endpoint.
func (p *EndpointPool) GlobalConn(ctx context.Context) (*grpc.ClientConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.globalConn != nil {
		return p.globalConn, nil
	}

	conn, err := p.dial(ctx, p.globalEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to global endpoint %s: %w", p.globalEndpoint, err)
	}
	p.globalConn = conn
	return conn, nil
}

// IAMConn returns or creates the client connection to the IAM endpoint.
func (p *EndpointPool) IAMConn(ctx context.Context) (*grpc.ClientConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.iamConn != nil {
		return p.iamConn, nil
	}

	conn, err := p.dial(ctx, p.iamEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to IAM endpoint %s: %w", p.iamEndpoint, err)
	}
	p.iamConn = conn
	return conn, nil
}

// ComputeConn returns or creates the client connection to the Compute endpoint.
func (p *EndpointPool) ComputeConn(ctx context.Context) (*grpc.ClientConn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.computeEndpoint == "" {
		return nil, fmt.Errorf("compute endpoint is not configured; specify compute_endpoint in provider configuration or set NSC_ENDPOINT")
	}

	if p.computeConn != nil {
		return p.computeConn, nil
	}

	conn, err := p.dial(ctx, p.computeEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to compute endpoint %s: %w", p.computeEndpoint, err)
	}
	p.computeConn = conn
	return conn, nil
}

func (p *EndpointPool) dial(ctx context.Context, endpoint string) (*grpc.ClientConn, error) {
	uaOption := grpc.WithUserAgent(fmt.Sprintf("terraform-provider-namespace/%s", p.version))
	opts := append([]grpc.DialOption{uaOption}, p.dialOpts...)

	return NewConnectionWithEndpointWithTransportCredentials(ctx, endpoint, p.tokenSource, p.creds, opts...)
}

// Close closes all open connections in the pool.
func (p *EndpointPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []string
	if p.globalConn != nil {
		if err := p.globalConn.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("global: %v", err))
		}
		p.globalConn = nil
	}
	if p.iamConn != nil {
		if err := p.iamConn.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("iam: %v", err))
		}
		p.iamConn = nil
	}
	if p.computeConn != nil {
		if err := p.computeConn.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("compute: %v", err))
		}
		p.computeConn = nil
	}

	if len(errs) > 0 {
		return fmt.Errorf("error closing connections: %s", strings.Join(errs, "; "))
	}
	return nil
}

// GlobalEndpoint returns the configured or resolved Global endpoint URL.
func (p *EndpointPool) GlobalEndpoint() string {
	return p.globalEndpoint
}

// IAMEndpoint returns the configured or resolved IAM endpoint URL.
func (p *EndpointPool) IAMEndpoint() string {
	return p.iamEndpoint
}

// ComputeEndpoint returns the configured or resolved Compute endpoint URL.
func (p *EndpointPool) ComputeEndpoint() string {
	return p.computeEndpoint
}
