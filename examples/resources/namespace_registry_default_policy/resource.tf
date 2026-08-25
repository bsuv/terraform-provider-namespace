terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

# Manages workspace-wide container registry image retention policy
resource "namespace_registry_default_policy" "default" {
  default_expiration     = "720h" # 30 days
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
