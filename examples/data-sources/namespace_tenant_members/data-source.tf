terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_tenant_members" "current" {}

output "workspace_members" {
  value = [for m in data.namespace_tenant_members.current.members : m.email]
}
