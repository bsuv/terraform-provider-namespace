terraform {
  required_version = ">= 1.11.0"
  required_providers {
    namespace = {
      source  = "bsuv/namespace"
      version = "~> 0.1"
    }
  }
}

# Example 1: Direct token authentication via ephemeral sensitive variable
variable "namespace_token" {
  type        = string
  description = "Namespace Cloud Bearer Token"
  sensitive   = true
  ephemeral   = true
}

# Primary global/default provider instance
provider "namespace" {
  token = var.namespace_token
}

# Example 2: Aliased provider for EU regional compute endpoint
provider "namespace" {
  alias            = "eu"
  token            = var.namespace_token
  compute_endpoint = "https://eu.compute.namespaceapis.com:443"
}

# Example 3: Aliased provider for US regional compute endpoint
provider "namespace" {
  alias            = "us"
  token            = var.namespace_token
  compute_endpoint = "https://us.compute.namespaceapis.com:443"
}
