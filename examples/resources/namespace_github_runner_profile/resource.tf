terraform {
  required_providers {
    namespace = {
      source = "bsuv/namespace"
    }
  }
}

resource "namespace_github_runner_profile" "high_perf_builder" {
  tag          = "ubuntu-24-04-large"
  os           = "ubuntu-24.04"
  description  = "High-performance build runner with persistent cache attachment"
  emoji        = "🚀"
  swap_size_mb = 2048

  builder_mode = "USE_REMOTE_BUILDER"
  access_level = "ACCESS_LEVEL_PERMISSIVE"

  experimental_features = [
    "nested-virtualization",
  ]

  instance_shape = {
    virtual_cpu      = 8
    memory_megabytes = 32768
    machine_arch     = "amd64"
    os               = "linux"
    selectors = [
      {
        name  = "purpose"
        value = "builder"
      }
    ]
  }

  cache_volume_settings = [
    {
      volume_tag                   = "build-cache"
      volume_size_gb               = 100
      volume_reserved_size_gb      = 10
      bundled_container_cache      = true
      bundled_buildkit_cache       = true
      bundled_git_mirror           = true
      bundled_runner_tools_cache   = true
      bundled_runner_actions_cache = true
      allow_commit_from_branch     = ["main", "release/*"]
    }
  ]

  custom_runner_image_spec = {
    ubuntu_spec = {
      apt_package = ["clang", "lld", "ninja-build"]
    }
    docker_spec = {
      dockerfile_content = "RUN curl -fsSL https://get.docker.com | sh"
    }
  }

  network_policy = {
    egress = "EGRESS_POLICY_DOMAIN_ALLOW_LIST"
    egress_domain_allow_list = [
      "github.com",
      "*.npmjs.org",
      "pypi.org",
    ]
    advisory = false
  }
}
