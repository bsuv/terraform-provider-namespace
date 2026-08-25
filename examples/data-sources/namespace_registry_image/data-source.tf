terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_registry_image" "app" {
  repository = "backend-service"
  reference  = "v1.4.2"
}

output "image_digest" {
  value = data.namespace_registry_image.app.digest
}
