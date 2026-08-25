terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

# Note: Managing tenant groups requires Partner Token credentials
resource "namespace_tenant_group" "production" {
  name = "production-workspaces"

  policies = [
    {
      policy = "namespace.cloud.compute.v1beta.UsagePolicy"
      value = jsonencode({
        max_cpus   = 128
        max_memory = 524288
      })
    }
  ]
}
