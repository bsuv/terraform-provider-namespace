terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_network_configuration" "current" {}

output "network_id" {
  value = data.namespace_network_configuration.current.id
}
