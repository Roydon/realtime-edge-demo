terraform {
  required_version = ">= 1.6"

  required_providers {
    digitalocean = {
      source = "digitalocean/digitalocean"
      # Pinned to a minor range. An unpinned provider means a plan can change
      # because of something nobody in the team did, which is the opposite of
      # what infrastructure-as-code is for.
      version = "~> 2.43"
    }
  }

  # No backend block. This configuration is plan-only by design and never
  # holds state; see README.md. A real deployment would put state in DO Spaces
  # or Terraform Cloud with locking enabled, because two concurrent applies
  # against an unlocked state is how infrastructure gets corrupted.
}

provider "digitalocean" {
  # Read from the DIGITALOCEAN_TOKEN environment variable. The token is never
  # written into a .tfvars file, because tfvars end up committed.
  token = var.do_token
}
