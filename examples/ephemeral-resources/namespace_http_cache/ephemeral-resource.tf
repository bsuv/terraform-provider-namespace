terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

ephemeral "namespace_http_cache" "artifacts" {
  name = "generic-http-cache"
  site = "us"
}
