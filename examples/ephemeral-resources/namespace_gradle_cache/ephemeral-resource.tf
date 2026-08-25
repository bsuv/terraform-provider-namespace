terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

ephemeral "namespace_gradle_cache" "gradle_build" {
  name = "main-ci-cache"
  site = "eu"
}
