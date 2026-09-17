variable "name_prefix" {
  description = "Prefix for every resource name in this module."
  type        = string
}

variable "region" {
  type = string
}

variable "size" {
  type = string
}

variable "image" {
  type = string
}

variable "node_count" {
  type = number
}

variable "vpc_uuid" {
  type = string
}

variable "ssh_key_ids" {
  type = list(string)
}

variable "tags" {
  type = list(string)
}

variable "project_id" {
  type = string
}

variable "ssh_admin_cidrs" {
  type = list(string)
}

variable "http_port" {
  type = number
}

variable "https_port" {
  type = number
}

variable "enable_media_ports" {
  type = bool
}

variable "media_udp_port_range" {
  type = object({
    from = number
    to   = number
  })
}

variable "enable_turn" {
  type = bool
}

variable "enable_monitoring_volume" {
  type = bool
}

variable "monitoring_volume_size_gb" {
  type = number
}
