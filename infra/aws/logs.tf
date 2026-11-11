resource "aws_cloudwatch_log_group" "ecs" {
  name              = "/${var.project}/ecs"
  retention_in_days = var.log_retention_days
}
