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
        concurrency_limits = {
          max_cpu            = 128
          max_memory_mb      = 524288
          max_instance_count = 32
        }
        devbox_limits = {
          max_count = 10
        }
      })
    }
  ]
}
