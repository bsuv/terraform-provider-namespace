terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_builder_usage" "monthly" {
  period_start = "2026-08-01"
  period_end   = "2026-08-21"
}

output "builder_unit_minutes" {
  value = data.namespace_builder_usage.monthly.total.build_unit_minutes
}
