terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

ephemeral "namespace_client_certificate" "tls_auth" {
  tenant_id     = "tenant_corp_eng"
  actor_id      = "workstation-alice"
  duration_secs = 7200
}
