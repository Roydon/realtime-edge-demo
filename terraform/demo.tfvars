# Example variable values, checked in deliberately: this file contains no
# secrets. The API token comes from the DIGITALOCEAN_TOKEN environment
# variable, and SSH key fingerprints are account-specific and left empty.
#
#   export DIGITALOCEAN_TOKEN=...
#   make tf-plan
#
# This repository never applies. See docs/DECISIONS.md.

project_name = "realtime-edge"
environment  = "demo"

region        = "fra1"
droplet_size  = "s-2vcpu-4gb"
droplet_count = 1

# Replace with a real administrative range before any apply. The default is a
# documentation-only block (RFC 5737) so a careless apply cannot expose SSH.
ssh_admin_cidrs = ["192.0.2.0/24"]

# Populate with fingerprints of keys already in the DigitalOcean account.
ssh_key_fingerprints = []

# Off for the WebSocket demo. Set both to true when a real SFU replaces the
# echo service - that single switch is the difference between this firewall
# and a media-capable one.
enable_media_ports = false
enable_turn        = false

enable_monitoring_volume  = true
monitoring_volume_size_gb = 20

extra_tags = {
  cost_center = "platform"
  owner       = "platform-team"
}
