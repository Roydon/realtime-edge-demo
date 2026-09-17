output "public_ips" {
  description = "Reserved IPv4 addresses assigned to the nodes."
  value       = digitalocean_reserved_ip.this[*].ip_address
}

output "private_ips" {
  description = "VPC-internal addresses."
  value       = digitalocean_droplet.this[*].ipv4_address_private
}

output "droplet_ids" {
  value = digitalocean_droplet.this[*].id
}

# A flat summary of what the firewall actually opens, so reviewing a plan does
# not mean mentally evaluating the dynamic blocks above.
output "firewall_summary" {
  description = "The inbound rules this configuration applies, in readable form."
  value = compact([
    "tcp/22 from ${join(",", var.ssh_admin_cidrs)} (ssh, restricted)",
    "tcp/${var.http_port} from anywhere (redirect + ACME)",
    "tcp/${var.https_port} from anywhere (tls)",
    var.enable_media_ports ? "udp/${var.media_udp_port_range.from}-${var.media_udp_port_range.to} from anywhere (webrtc media)" : "",
    var.enable_media_ports ? "tcp/7881 from anywhere (media tcp fallback)" : "",
    var.enable_turn ? "udp/3478 + tcp/5349 from anywhere (turn)" : "",
    "tcp/9090-9200 from the vpc only (metrics scraping)",
  ])
}
