package provider

import (
	"github.com/bsuv/terraform-provider-namespace/internal/services/compute"
	"github.com/bsuv/terraform-provider-namespace/internal/services/github"
	"github.com/bsuv/terraform-provider-namespace/internal/services/iam"
	"github.com/bsuv/terraform-provider-namespace/internal/services/registry"
	"github.com/bsuv/terraform-provider-namespace/internal/services/vault"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
)

func getDataSources() []func() datasource.DataSource {
	return []func() datasource.DataSource{
		github.NewRunnerProfileDataSource,
		github.NewRunnerProfilesDataSource,
		github.NewBaseImagesDataSource,
		iam.NewTenantGroupDataSource,
		iam.NewTenantMembersDataSource,
		compute.NewCacheVolumeSummaryDataSource,
		compute.NewPersistentVolumeDataSource,
		compute.NewBuilderConfigurationDataSource,
		compute.NewBuilderUsageDataSource,
		compute.NewNetworkConfigurationDataSource,
		registry.NewRegistryImageDataSource,
		registry.NewRegistryImagesDataSource,
		vault.NewSecretMetadataDataSource,
	}
}
