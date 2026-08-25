terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_builder_configuration" "amd64" {
  platform = "linux/amd64"
}

output "builder_endpoint" {
  value = data.namespace_builder_configuration.amd64.buildkit_endpoint
}
