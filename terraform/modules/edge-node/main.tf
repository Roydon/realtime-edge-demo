# An edge node and the firewall in front of it.
#
# The firewall is the interesting part of this module. The ports it opens are
# the difference between a configuration that can host a WebSocket service and
# one that can host an SFU, and getting that wrong is the most common way a
# WebRTC deployment ends up with users who connect successfully and then hear
# nothing.

locals {
  # Cloud-init handles only what must be true before the node is first
  # reachable. Application deployment is the CD pipeline's job, not
  # Terraform's: mixing the two means a config change requires an infrastructure
  # apply, and rolling back an application becomes an infrastructure operation.
  user_data = <<-CLOUDINIT
    #cloud-config
    package_update: true
    package_upgrade: true
    packages:
      - docker.io
      - docker-compose-v2
      - fail2ban
      - unattended-upgrades

    write_files:
      - path: /etc/ssh/sshd_config.d/99-hardening.conf
        content: |
          PermitRootLogin no
          PasswordAuthentication no
          KbdInteractiveAuthentication no
          X11Forwarding no
          MaxAuthTries 3
      # An SFU opens a socket per participant. The default file-descriptor
      # limit is reached long before the machine runs out of capacity, and the
      # resulting failure looks like random connection drops rather than an
      # obvious limit being hit.
      - path: /etc/security/limits.d/99-realtime.conf
        content: |
          *  soft  nofile  65535
          *  hard  nofile  65535
      - path: /etc/sysctl.d/99-realtime.conf
        content: |
          # A larger accept queue absorbs reconnect storms, which is exactly
          # the traffic pattern after any network blip on a voice platform.
          net.core.somaxconn = 4096
          net.ipv4.tcp_max_syn_backlog = 8192
          # Media is UDP and bursty; the default socket buffers are sized for
          # neither.
          net.core.rmem_max = 16777216
          net.core.wmem_max = 16777216

    runcmd:
      - systemctl restart ssh
      - sysctl --system
      - systemctl enable --now docker fail2ban
  CLOUDINIT
}

resource "digitalocean_droplet" "this" {
  count = var.node_count

  name     = "${var.name_prefix}-node-${count.index + 1}"
  region   = var.region
  size     = var.size
  image    = var.image
  vpc_uuid = var.vpc_uuid
  ssh_keys = var.ssh_key_ids
  tags     = var.tags

  # Free, and the only way to see what a node was doing when it stopped
  # responding to the network.
  monitoring = true

  # Off by default on DigitalOcean and cheap to enable. The alternative is
  # discovering at restore time that nothing was ever being backed up.
  backups = true

  droplet_agent = true
  user_data     = local.user_data

  lifecycle {
    # Replacing a live media node drops every call on it. Image or size
    # changes must go through a deliberate blue/green rollout, not an
    # in-place apply that nobody noticed in the plan output.
    ignore_changes = [image, user_data]
  }
}

resource "digitalocean_volume" "metrics" {
  count = var.enable_monitoring_volume ? 1 : 0

  name                    = "${var.name_prefix}-metrics"
  region                  = var.region
  size                    = var.monitoring_volume_size_gb
  initial_filesystem_type = "ext4"
  description             = "Prometheus TSDB storage, kept off the root filesystem so metric growth cannot take the node down."
  tags                    = var.tags
}

resource "digitalocean_volume_attachment" "metrics" {
  count = var.enable_monitoring_volume ? var.node_count : 0

  droplet_id = digitalocean_droplet.this[count.index].id
  volume_id  = digitalocean_volume.metrics[0].id
}

# A stable address that can be moved between nodes. During a blue/green
# rollout the address is reassigned rather than DNS being changed, which
# avoids waiting out a TTL while traffic is split.
resource "digitalocean_reserved_ip" "this" {
  count = var.node_count

  region     = var.region
  droplet_id = digitalocean_droplet.this[count.index].id
}

resource "digitalocean_firewall" "this" {
  name        = "${var.name_prefix}-fw"
  droplet_ids = digitalocean_droplet.this[*].id
  tags        = var.tags

  # --- SSH, restricted ---
  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.ssh_admin_cidrs
  }

  # --- public HTTP/HTTPS ---
  inbound_rule {
    protocol         = "tcp"
    port_range       = tostring(var.http_port)
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  inbound_rule {
    protocol         = "tcp"
    port_range       = tostring(var.https_port)
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  # --- WebRTC media ---
  # An SFU negotiates a UDP port per participant connection out of this range.
  # It has to be open to the world: the whole point is that arbitrary clients
  # on arbitrary networks send media here, so there is no source address to
  # restrict it to. The range size is a hard concurrency ceiling for the node.
  dynamic "inbound_rule" {
    for_each = var.enable_media_ports ? [1] : []
    content {
      protocol         = "udp"
      port_range       = "${var.media_udp_port_range.from}-${var.media_udp_port_range.to}"
      source_addresses = ["0.0.0.0/0", "::/0"]
    }
  }

  # TCP fallback on 443 for clients whose network blocks UDP entirely -
  # common on corporate and hotel wifi. Materially worse for media quality
  # than UDP, and still far better than a call that does not connect.
  dynamic "inbound_rule" {
    for_each = var.enable_media_ports ? [1] : []
    content {
      protocol         = "tcp"
      port_range       = "7881"
      source_addresses = ["0.0.0.0/0", "::/0"]
    }
  }

  # --- TURN ---
  # TURN relays media for clients behind symmetric NAT. It is a minority of
  # users and it is not optional: without it those users connect and then hear
  # silence, which is the single hardest WebRTC failure to diagnose from the
  # server side because every server-side metric looks healthy.
  dynamic "inbound_rule" {
    for_each = var.enable_turn ? [1] : []
    content {
      protocol         = "udp"
      port_range       = "3478"
      source_addresses = ["0.0.0.0/0", "::/0"]
    }
  }

  dynamic "inbound_rule" {
    for_each = var.enable_turn ? [1] : []
    content {
      protocol         = "tcp"
      port_range       = "5349"
      source_addresses = ["0.0.0.0/0", "::/0"]
    }
  }

  # --- internal scraping ---
  # Prometheus reaches the exporters over the VPC only. These ports are never
  # exposed publicly: /metrics reveals internal topology, and an unauthenticated
  # scrape endpoint on the public interface is a free reconnaissance feed.
  inbound_rule {
    protocol         = "tcp"
    port_range       = "9090-9200"
    source_addresses = ["10.20.0.0/20"]
  }

  # --- egress ---
  # Unrestricted outbound: the node needs package updates, ACME, container
  # registries and the platform's own APIs. Locking egress down is worthwhile
  # but belongs with an explicit allowlist and a proxy, not a half-measure here.
  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}

resource "digitalocean_project_resources" "this" {
  project = var.project_id
  resources = concat(
    digitalocean_droplet.this[*].urn,
    digitalocean_volume.metrics[*].urn,
  )
}
