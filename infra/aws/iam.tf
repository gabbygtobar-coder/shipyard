data "aws_iam_policy_document" "ecs_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name               = "${var.project}-ecs-execution"
  description        = "Lets the ECS agent pull from ECR and write CloudWatch logs"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

data "aws_iam_policy_document" "execution" {
  statement {
    sid     = "ECRAuth"
    effect  = "Allow"
    actions = ["ecr:GetAuthorizationToken"]
    # GetAuthorizationToken does not accept a repository ARN.
    resources = ["*"]
  }

  statement {
    sid    = "ECRPull"
    effect = "Allow"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
    ]
    resources = [aws_ecr_repository.app.arn]
  }

  statement {
    sid    = "Logs"
    effect = "Allow"
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = ["${aws_cloudwatch_log_group.ecs.arn}:*"]
  }
}

resource "aws_iam_role_policy" "execution" {
  name   = "execution"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.execution.json
}

# The task role is what the app container can call. The hello app calls no
# AWS API, so this role has no policies. shipyardd uses the operator's keys,
# not this role.
resource "aws_iam_role" "task" {
  name               = "${var.project}-ecs-task"
  description        = "Application role with no AWS permissions"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}
