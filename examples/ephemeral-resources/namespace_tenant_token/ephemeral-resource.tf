terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

variable "partner_token" {
  type        = string
  description = "Namespace Partner Token"
  sensitive   = true
  ephemeral   = true
}

provider "namespace" {
  token = var.partner_token
}

ephemeral "namespace_tenant_token" "ci" {
  tenant_id     = "tenant_corp_eng"
  actor_id      = "github-actions-builder"
  duration_secs = 3600

  policies = [
    {
      policy_type = "namespace.cloud.compute.v1beta.UsagePolicy"
      value = jsonencode({
        concurrency_limits = {
          max_cpu       = 8
          max_memory_mb = 16384
        }
        usage_limits = {
          builds = 50
        }
      })
    }
  ]
}
