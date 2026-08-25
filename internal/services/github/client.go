package github

import (
	"context"

	githubv1beta "buf.build/gen/go/namespace/cloud/protocolbuffers/go/proto/namespace/cloud/github/v1beta"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/bsuv/terraform-provider-namespace/internal/client"
)

// ServiceClient defines the gRPC client operations required by GitHub services.
type ServiceClient interface {
	CreateProfile(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error)
	UpdateProfile(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error)
	ListProfiles(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error)
	DeleteProfile(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error)
	ListBaseImages(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error)
}

// DomainClient adapts *client.Client to ServiceClient applying RPC policies.
type DomainClient struct {
	client *client.Client
}

// NewDomainClient creates a new DomainClient wrapping the provider Client bundle.
func NewDomainClient(c *client.Client) *DomainClient {
	return &DomainClient{client: c}
}

// CreateProfile creates a runner profile with fail-fast mutating deadline policy.
func (d *DomainClient) CreateProfile(ctx context.Context, req *githubv1beta.CreateProfileRequest) (*githubv1beta.CreateProfileResponse, error) {
	cli, err := d.client.ProfileClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*githubv1beta.CreateProfileResponse, error) {
		return cli.CreateProfile(callCtx, req)
	})
}

// UpdateProfile updates an existing runner profile with fail-fast mutating deadline policy.
func (d *DomainClient) UpdateProfile(ctx context.Context, req *githubv1beta.UpdateProfileRequest) (*githubv1beta.UpdateProfileResponse, error) {
	cli, err := d.client.ProfileClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*githubv1beta.UpdateProfileResponse, error) {
		return cli.UpdateProfile(callCtx, req)
	})
}

// ListProfiles lists all runner profiles for the authenticated workspace with idempotent read retry policy.
func (d *DomainClient) ListProfiles(ctx context.Context, req *emptypb.Empty) (*githubv1beta.ListProfilesResponse, error) {
	cli, err := d.client.ProfileClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*githubv1beta.ListProfilesResponse, error) {
		return cli.ListProfiles(callCtx, req)
	})
}

// DeleteProfile deletes a runner profile by ID with fail-fast mutating deadline policy.
func (d *DomainClient) DeleteProfile(ctx context.Context, req *githubv1beta.DeleteProfileRequest) (*emptypb.Empty, error) {
	cli, err := d.client.ProfileClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteMutating(ctx, func(callCtx context.Context) (*emptypb.Empty, error) {
		return cli.DeleteProfile(callCtx, req)
	})
}

// ListBaseImages lists runner base images with idempotent read retry policy.
func (d *DomainClient) ListBaseImages(ctx context.Context, req *githubv1beta.ListBaseImagesRequest) (*githubv1beta.ListBaseImagesResponse, error) {
	cli, err := d.client.ImageClient(ctx)
	if err != nil {
		return nil, err
	}
	return client.ExecuteIdempotentRead(ctx, func(callCtx context.Context) (*githubv1beta.ListBaseImagesResponse, error) {
		return cli.ListBaseImages(callCtx, req)
	})
}
