terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_github_runner_profiles" "all" {}

output "all_profile_tags" {
  value = [for p in data.namespace_github_runner_profiles.all.profiles : p.tag]
}
