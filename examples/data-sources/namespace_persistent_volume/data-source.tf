terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_persistent_volume" "runner_disk" {
  tag  = "runner-workspace-volume"
  site = "eu"
}

output "volume_id" {
  value = data.namespace_persistent_volume.runner_disk.id
}
