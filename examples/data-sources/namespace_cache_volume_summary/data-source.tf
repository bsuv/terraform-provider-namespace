terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_cache_volume_summary" "build_cache" {
  tag = "ci-build-cache"
}

output "cache_volume_stats" {
  description = "Summary metrics for cache volume"
  value = {
    size_mb     = data.namespace_cache_volume_summary.build_cache.size_mb
    generations = length(data.namespace_cache_volume_summary.build_cache.generations)
  }
}
