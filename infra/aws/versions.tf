terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.100"
    }
  }

  # State stays in infra/aws/terraform.tfstate on this machine. That is the
  # default so one person can learn the stack without an S3 bucket. Do not
  # commit the state file. CI runs `terraform init -backend=false` and never
  # applies.
  #
  # A shared S3 backend is optional and is not wired in. The steps, and a
  # backend block you can paste in place of the local one, are in
  # docs/aws-setup.md under "Optional: remote state".
  backend "local" {
    path = "terraform.tfstate"
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = merge(
      {
        Project     = var.project
        Environment = var.environment
        ManagedBy   = "terraform"
        Application = "shipyard"
      },
      var.extra_tags,
    )
  }
}
