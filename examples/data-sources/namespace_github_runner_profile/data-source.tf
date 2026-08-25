terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_github_runner_profile" "current" {
  profile_id = "prof_01h7abc123"
}

output "runner_tag" {
  value = data.namespace_github_runner_profile.current.tag
}
