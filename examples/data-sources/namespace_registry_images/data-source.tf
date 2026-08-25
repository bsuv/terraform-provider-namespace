terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_registry_images" "backend" {
  repository = "backend-service"
}

output "image_count" {
  value = length(data.namespace_registry_images.backend.images)
}
