terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

# Manages repository-specific container registry image retention policy override
resource "namespace_registry_repository_policy" "frontend" {
  repository             = "my-org/frontend"
  expiration             = "168h" # 7 days
  expiration_enforcement = "EXPIRATION_ENFORCEMENT_ENFORCE"
}
