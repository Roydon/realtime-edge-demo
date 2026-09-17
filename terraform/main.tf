# Infrastructure for the real-time edge node.
#
# This configuration is plan-only in this repository: there is no backend, and
# CI runs fmt/validate but never apply. It exists to show the shape of the
# production intent, and to be the starting point for a real deployment rather
# than a toy.

locals {
  name_prefix = "${var.project_name}-${var.environment}"

  # Tags are the only reliable way to answer "what is this and who pays for
  # it?" three months later, so every taggable resource gets the same base set.
  base_tags = merge(
    {
      project     = var.project_name
      environment = var.environment
      managed_by  = "terraform"
      component   = "realtime-edge"
    },
    var.extra_tags,
  )

  # DigitalOcean tags are flat strings, not key-value pairs, so the map is
  # flattened into "key:value" form to keep it queryable.
  do_tags = [for k, v in local.base_tags : "${k}:${v}"]
}

# A project keeps these resources grouped in the DO console and, more usefully,
# gives billing a boundary that matches the service.
resource "digitalocean_project" "this" {
  name        = local.name_prefix
  description = "Real-time voice edge - ${var.environment}"
  purpose     = "Service or API"
  environment = var.environment == "production" ? "Production" : "Development"
}

resource "digitalocean_tag" "this" {
  for_each = toset(local.do_tags)
  name     = each.value
}

# A private network so that node-to-node traffic and any future database or
# cache never traverses the public interface.
resource "digitalocean_vpc" "this" {
  name     = "${local.name_prefix}-vpc"
  region   = var.region
  ip_range = "10.20.0.0/20"
}

module "edge_node" {
  source = "./modules/edge-node"

  name_prefix = local.name_prefix
  region      = var.region
  size        = var.droplet_size
  image       = var.droplet_image
  node_count  = var.droplet_count
  vpc_uuid    = digitalocean_vpc.this.id
  ssh_key_ids = var.ssh_key_fingerprints
  tags        = local.do_tags
  project_id  = digitalocean_project.this.id

  ssh_admin_cidrs = var.ssh_admin_cidrs
  http_port       = var.http_port
  https_port      = var.https_port

  enable_media_ports   = var.enable_media_ports
  media_udp_port_range = var.media_udp_port_range
  enable_turn          = var.enable_turn

  enable_monitoring_volume  = var.enable_monitoring_volume
  monitoring_volume_size_gb = var.monitoring_volume_size_gb

  depends_on = [digitalocean_tag.this]
}
