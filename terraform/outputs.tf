output "node_public_ips" {
  description = "Public IPv4 addresses of the edge nodes. These are the addresses DNS points at."
  value       = module.edge_node.public_ips
}

output "node_private_ips" {
  description = "VPC-internal addresses, for node-to-node and monitoring traffic."
  value       = module.edge_node.private_ips
}

output "firewall_summary" {
  description = "Human-readable summary of the inbound rules actually applied, so a plan review does not require reading the whole resource graph."
  value       = module.edge_node.firewall_summary
}

output "vpc_cidr" {
  description = "CIDR of the private network."
  value       = digitalocean_vpc.this.ip_range
}
