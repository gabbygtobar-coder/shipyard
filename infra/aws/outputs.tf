output "aws_region" {
  description = "Region the stack was applied in."
  value       = var.aws_region
}

output "cluster_name" {
  description = "ECS cluster name. make aws-down deletes services in this cluster before terraform destroy."
  value       = aws_ecs_cluster.this.name
}

output "ecr_repository_url" {
  description = "Registry host and repository name, without a scheme. shipyardd pushes images here."
  value       = aws_ecr_repository.app.repository_url
}

output "execution_role_arn" {
  description = "Fargate task execution role. It can pull from this ECR repository and write logs."
  value       = aws_iam_role.execution.arn
}

output "task_role_arn" {
  description = "Role assumed by the app container. It has no AWS permissions."
  value       = aws_iam_role.task.arn
}

output "public_subnet_ids" {
  description = "Public subnet IDs where Fargate tasks run."
  value       = aws_subnet.public[*].id
}

output "task_security_group_id" {
  description = "Security group attached to Fargate tasks."
  value       = aws_security_group.tasks.id
}

output "alb_dns_name" {
  description = "Public DNS name of the load balancer. Path rules are http://<this>/apps/<app>/."
  value       = aws_lb.this.dns_name
}

output "alb_listener_arn" {
  description = "HTTP listener ARN. Listener rules forward each app to its target group."
  value       = aws_lb_listener.http.arn
}

output "target_group_arns" {
  description = "Target group ARN for each app in app_names."
  value       = { for name, group in aws_lb_target_group.app : name => group.arn }
}

output "log_group_name" {
  description = "CloudWatch log group for task output."
  value       = aws_cloudwatch_log_group.ecs.name
}

output "shipyard_env" {
  description = "Copy these exports into the shell that runs shipyardd. terraform output -raw shipyard_env"
  value = join("\n", concat([
    "export SHIPYARD_RUNTIME=ecs",
    "export SHIPYARD_AWS_REGION=${var.aws_region}",
    "export SHIPYARD_ECS_CLUSTER=${aws_ecs_cluster.this.name}",
    "export SHIPYARD_ECR_REPOSITORY=${aws_ecr_repository.app.repository_url}",
    "export SHIPYARD_ECS_EXECUTION_ROLE_ARN=${aws_iam_role.execution.arn}",
    "export SHIPYARD_ECS_TASK_ROLE_ARN=${aws_iam_role.task.arn}",
    "export SHIPYARD_ECS_SUBNETS=${join(",", aws_subnet.public[*].id)}",
    "export SHIPYARD_ECS_SECURITY_GROUPS=${aws_security_group.tasks.id}",
    "export SHIPYARD_ECS_LOG_GROUP=${aws_cloudwatch_log_group.ecs.name}",
    "export SHIPYARD_ALB_DNS_NAME=${aws_lb.this.dns_name}",
  ], var.base_domain == "" ? [] : ["export SHIPYARD_BASE_DOMAIN=${var.base_domain}"]))
}
