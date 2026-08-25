terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_github_base_images" "linux" {
  labels = ["ubuntu-24.04"]
}

output "available_images" {
  value = [for img in data.namespace_github_base_images.linux.images : img.image_ref]
}
