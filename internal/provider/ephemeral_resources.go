package provider

import (
	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/services/integrations"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
)

func getEphemeralResources() []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{
		iam.NewTenantTokenEphemeralResource,
		iam.NewIDTokenEphemeralResource,
		iam.NewClientCertificateEphemeralResource,
		integrations.NewBazelCacheEphemeralResource,
		integrations.NewGradleCacheEphemeralResource,
		integrations.NewHTTPCacheEphemeralResource,
	}
}
