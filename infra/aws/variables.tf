variable "aws_region" {
  type        = string
  description = "AWS region for every resource. us-east-1 is the cheapest common default."
  default     = "us-east-1"

  validation {
    condition     = can(regex("^[a-z]{2}-[a-z0-9-]+-[0-9]+$", var.aws_region))
    error_message = "aws_region must look like us-east-1."
  }
}

variable "project" {
  type        = string
  description = "Short name prefixed onto resource names. Keep the default unless you also update the deployer IAM policy."
  default     = "shipyard"

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{0,16}$", var.project))
    error_message = "project must be a short lowercase name so ALB and IAM names stay inside AWS limits."
  }
}

variable "environment" {
  type        = string
  description = "Value of the Environment tag. This stack is meant to be one cheap dev environment."
  default     = "dev"

  validation {
    condition     = can(regex("^[a-z0-9-]{1,16}$", var.environment))
    error_message = "environment must be a short lowercase tag value."
  }
}

variable "extra_tags" {
  type        = map(string)
  description = "Extra tags merged into the provider default_tags. Empty by default."
  default     = {}
}

variable "vpc_cidr" {
  type        = string
  description = "CIDR for the VPC. The two public subnets must fit inside it."
  default     = "10.0.0.0/16"
}

variable "public_subnet_cidrs" {
  type        = list(string)
  description = "Exactly two public subnet CIDRs, one per availability zone. There is no private subnet and no NAT gateway."
  default     = ["10.0.1.0/24", "10.0.2.0/24"]

  validation {
    condition     = length(var.public_subnet_cidrs) == 2
    error_message = "Provide exactly two public subnet CIDRs. The load balancer needs two availability zones, and this stack does not create a NAT gateway."
  }
}

variable "container_port" {
  type        = number
  description = "Port the app container listens on. Must match SHIPYARD_CONTAINER_PORT (default 8080)."
  default     = 8080

  validation {
    condition     = var.container_port >= 1 && var.container_port <= 65535
    error_message = "container_port must be from 1 to 65535."
  }
}

variable "health_check_path" {
  type        = string
  description = "HTTP path the target group polls on the task. examples/hello answers 200 on /."
  default     = "/"

  validation {
    condition     = startswith(var.health_check_path, "/") && !strcontains(var.health_check_path, " ")
    error_message = "health_check_path must be an absolute path."
  }
}

variable "app_names" {
  type        = list(string)
  description = "Apps that get a target group and an ALB listener rule. shipyardd creates the ECS service later. The default hello app matches examples/hello. Names must be at most 29 characters."
  default     = ["hello"]

  validation {
    condition = alltrue([
      for name in var.app_names :
      can(regex("^[a-z]([a-z0-9-]{0,27}[a-z0-9])?$", name)) && !strcontains(name, "--")
    ])
    error_message = "Each app name must be a lowercase slug of at most 29 characters, without a trailing hyphen or a double hyphen, so the target group name sy-<app> fits in 32 characters."
  }
}

variable "base_domain" {
  type        = string
  description = "Optional parent domain. Empty uses a path rule /apps/<app> on the ALB DNS name. A value uses a host rule <app>.<base_domain>. This stack does not create a DNS record or a certificate."
  default     = ""

  validation {
    condition     = var.base_domain == "" || can(regex("^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$", var.base_domain))
    error_message = "base_domain must be empty or a bare domain name such as example.com."
  }
}

variable "log_retention_days" {
  type        = number
  description = "CloudWatch log retention. 7 days is the low-cost default."
  default     = 7

  validation {
    condition     = contains([1, 3, 5, 7, 14, 30], var.log_retention_days)
    error_message = "log_retention_days must be a short CloudWatch retention value. Use 7 unless you mean to keep logs longer."
  }
}

variable "ecr_keep_images" {
  type        = number
  description = "How many images the ECR lifecycle policy keeps. Older images are expired."
  default     = 5

  validation {
    condition     = var.ecr_keep_images >= 1 && var.ecr_keep_images <= 50
    error_message = "ecr_keep_images must be from 1 to 50."
  }
}
