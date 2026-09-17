# --- credentials ---

variable "do_token" {
  description = "DigitalOcean API token. Supplied via the DIGITALOCEAN_TOKEN environment variable, never committed."
  type        = string
  sensitive   = true
  default     = null
}

# --- placement and sizing ---

variable "region" {
  description = "DigitalOcean region slug. For a real-time voice workload this is the single highest-impact choice in this file: it sets the propagation floor for every user, and no amount of tuning recovers a badly chosen region."
  type        = string
  default     = "fra1"

  validation {
    condition     = can(regex("^[a-z]{3}[0-9]$", var.region))
    error_message = "The region must be a DigitalOcean slug such as fra1, nyc3 or sgp1."
  }
}

variable "droplet_size" {
  description = "Droplet size slug. Media workloads are bandwidth- and CPU-bound long before they are memory-bound, so a general-purpose or CPU-optimised size is the right family."
  type        = string
  default     = "s-2vcpu-4gb"
}

variable "droplet_image" {
  description = "Base image slug."
  type        = string
  default     = "ubuntu-24-04-x64"
}

variable "droplet_count" {
  description = "Number of media nodes to provision."
  type        = number
  default     = 1

  validation {
    condition     = var.droplet_count >= 1 && var.droplet_count <= 10
    error_message = "droplet_count must be between 1 and 10; anything larger should go through a review rather than a variable default."
  }
}

# --- naming and tagging ---

variable "environment" {
  description = "Environment name, used in resource names and tags."
  type        = string
  default     = "demo"

  validation {
    condition     = contains(["demo", "staging", "production"], var.environment)
    error_message = "environment must be one of: demo, staging, production."
  }
}

variable "project_name" {
  description = "Short name used as the prefix for every resource created here."
  type        = string
  default     = "realtime-edge"
}

variable "extra_tags" {
  description = "Additional tags applied to every taggable resource. Tags are what make cost attribution and bulk operations possible later; adding them after the fact means touching every resource."
  type        = map(string)
  default     = {}
}

# --- access control ---

variable "ssh_admin_cidrs" {
  description = "CIDR blocks permitted to reach SSH. Defaults to a documentation-only range so that an unreviewed apply cannot open SSH to the internet."
  type        = list(string)
  default     = ["192.0.2.0/24"]

  validation {
    condition     = !contains(var.ssh_admin_cidrs, "0.0.0.0/0")
    error_message = "SSH must not be open to 0.0.0.0/0. Use a bastion, a VPN range, or an explicit office CIDR."
  }
}

variable "ssh_key_fingerprints" {
  description = "Fingerprints of SSH keys already uploaded to the DigitalOcean account. Password authentication is never enabled."
  type        = list(string)
  default     = []
}

# --- application ports ---

variable "https_port" {
  description = "Public TLS port for the edge."
  type        = number
  default     = 443
}

variable "http_port" {
  description = "Public plaintext port, served only to redirect to HTTPS and to answer ACME challenges."
  type        = number
  default     = 80
}

# --- real-time media ---
# These are off by default because the demo stack runs a WebSocket echo
# service, which needs none of them. They are here, wired and documented,
# because they are exactly what changes when the stand-in is replaced by a
# real SFU - see docs/DECISIONS.md.

variable "enable_media_ports" {
  description = "Open the UDP range and TURN ports an SFU requires. Leave false for the WebSocket-only demo."
  type        = bool
  default     = false
}

variable "media_udp_port_range" {
  description = "Inclusive UDP port range for WebRTC media. An SFU allocates one port per participant connection, so this range is a hard ceiling on concurrency for the node."
  type = object({
    from = number
    to   = number
  })
  default = {
    from = 50000
    to   = 60000
  }

  validation {
    condition     = var.media_udp_port_range.from < var.media_udp_port_range.to
    error_message = "media_udp_port_range.from must be lower than .to."
  }
}

variable "enable_turn" {
  description = "Open TURN ports. TURN is what carries media for the minority of clients behind symmetric NAT or restrictive corporate firewalls; without it those users connect and then hear silence."
  type        = bool
  default     = false
}

variable "enable_monitoring_volume" {
  description = "Attach a dedicated block volume for metric storage, so filling it cannot take the root filesystem - and the node - down with it."
  type        = bool
  default     = true
}

variable "monitoring_volume_size_gb" {
  description = "Size of the metrics volume in GB."
  type        = number
  default     = 20
}
