terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

data "namespace_vault_secret_metadata" "api_key" {
  object_id = "database_connection_key"
}

output "secret_version" {
  description = "Secret version without disclosing secret value"
  value       = data.namespace_vault_secret_metadata.api_key.version
}
