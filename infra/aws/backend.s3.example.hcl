# Optional S3 backend. This file is not loaded by Terraform.
# To switch off local state, replace the backend "local" block in versions.tf
# with the block below, create the bucket and lock table first, then run:
#   terraform init -migrate-state
#
# terraform {
#   backend "s3" {
#     bucket         = "your-shipyard-tfstate"
#     key            = "shipyard/terraform.tfstate"
#     region         = "us-east-1"
#     encrypt        = true
#     dynamodb_table = "shipyard-tf-lock"
#   }
# }
