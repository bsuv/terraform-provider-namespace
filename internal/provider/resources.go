package provider

import (
	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/hashicorp/terraform-plugin-framework/resource"
)

func getResources() []func() resource.Resource {
	return []func() resource.Resource{
		github.NewRunnerProfileResource,
		iam.NewTenantGroupResource,
		registry.NewSharedImageResource,
		registry.NewDefaultPolicyResource,
		registry.NewRepositoryPolicyResource,
	}
}
