terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

ephemeral "namespace_id_token" "aws_federation" {
  audience = "https://iam.amazonaws.com/oidc"
}
