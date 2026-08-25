terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

ephemeral "namespace_bazel_cache" "ci_cache" {
  key  = "ci"
  site = "eu"
}
