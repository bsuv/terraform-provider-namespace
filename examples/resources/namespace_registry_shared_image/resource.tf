terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

resource "namespace_registry_shared_image" "public_release" {
  repository = "my-org/app"
  digest     = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
  visibility = "PUBLIC"
  expires_at = "2026-12-31T23:59:59Z"
  suffix     = "v1-prod"
}

output "public_image_ref" {
  value = namespace_registry_shared_image.public_release.image_ref
}
