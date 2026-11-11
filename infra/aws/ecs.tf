resource "aws_ecs_cluster" "this" {
  name = var.project

  # Container Insights is a separate charge. Leave it off for this stack.
  setting {
    name  = "containerInsights"
    value = "disabled"
  }
}
