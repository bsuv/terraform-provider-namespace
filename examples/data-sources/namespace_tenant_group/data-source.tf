terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_tenant_group" "prod" {
  name = "production-workspaces"
}

output "enrolled_tenants" {
  value = data.namespace_tenant_group.prod.enrolled_tenant_ids
}
