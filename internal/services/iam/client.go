package iam

import (
	"context"

	iamv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/iam/v1beta"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

// ServiceClient defines the gRPC client operations required by IAM services.
type ServiceClient interface {
	EnsureTenantGroup(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error)
	DescribeTenantGroup(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error)
	DeleteTenantGroup(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error)
	ListTenantMembers(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error)
	IssueTenantToken(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error)
	IssueIdToken(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error)
	IssueTenantClientCertificate(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error)
}

// DomainClient adapts *client.Client to ServiceClient applying RPC policies.
type DomainClient struct {
	client *client.Client
}

// NewDomainClient creates a new DomainClient wrapping the provider Client bundle.
func NewDomainClient(c *client.Client) *DomainClient {
	return &DomainClient{client: c}
}

// EnsureTenantGroup creates or updates a tenant group with fail-fast mutating deadline policy.
func (d *DomainClient) EnsureTenantGroup(ctx context.Context, req *iamv1beta.EnsureTenantGroupRequest) (*iamv1beta.TenantGroupResponse, error) {
	cli, err := d.client.TenantClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*iamv1beta.TenantGroupResponse, error) {
		return cli.EnsureTenantGroup(callCtx, req)
	})
}

// DescribeTenantGroup fetches tenant group details by name with idempotent read retry policy.
func (d *DomainClient) DescribeTenantGroup(ctx context.Context, req *iamv1beta.TenantGroupReference) (*iamv1beta.DescribeTenantGroupResponse, error) {
	cli, err := d.client.TenantClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*iamv1beta.DescribeTenantGroupResponse, error) {
		return cli.DescribeTenantGroup(callCtx, req)
	})
}

// DeleteTenantGroup deletes a tenant group by name with fail-fast mutating deadline policy.
func (d *DomainClient) DeleteTenantGroup(ctx context.Context, req *iamv1beta.TenantGroupReference) (*emptypb.Empty, error) {
	cli, err := d.client.TenantClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*emptypb.Empty, error) {
		return cli.DeleteTenantGroup(callCtx, req)
	})
}

// ListTenantMembers lists all members in the current workspace/tenant with idempotent read retry policy.
func (d *DomainClient) ListTenantMembers(ctx context.Context, req *iamv1beta.ListTenantMembersRequest) (*iamv1beta.ListTenantMembersResponse, error) {
	cli, err := d.client.TenantMembershipClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*iamv1beta.ListTenantMembersResponse, error) {
		return cli.ListTenantMembers(callCtx, req)
	})
}

// IssueTenantToken issues a short-lived tenant bearer token using partner credentials (30s deadline).
func (d *DomainClient) IssueTenantToken(ctx context.Context, req *iamv1beta.IssueTenantTokenRequest) (*iamv1beta.IssueTenantTokenResponse, error) {
	cli, err := d.client.TokenClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteTokenIssuance(ctx, func(callCtx context.Context) (*iamv1beta.IssueTenantTokenResponse, error) {
		return cli.IssueTenantToken(callCtx, req)
	})
}

// IssueIdToken issues a short-lived OIDC ID token using tenant federation credentials (30s deadline).
func (d *DomainClient) IssueIdToken(ctx context.Context, req *iamv1beta.IssueIdTokenRequest) (*iamv1beta.IssueIdTokenResponse, error) {
	cli, err := d.client.TokenClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteTokenIssuance(ctx, func(callCtx context.Context) (*iamv1beta.IssueIdTokenResponse, error) {
		return cli.IssueIdToken(callCtx, req)
	})
}

// IssueTenantClientCertificate issues an X.509 client certificate using trusted authority JWT (30s deadline).
func (d *DomainClient) IssueTenantClientCertificate(ctx context.Context, req *iamv1beta.IssueTenantClientCertificateRequest) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
	cli, err := d.client.TenantClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteTokenIssuance(ctx, func(callCtx context.Context) (*iamv1beta.IssueTenantClientCertificateResponse, error) {
		return cli.IssueTenantClientCertificate(callCtx, req)
	})
}
