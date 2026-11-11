resource "aws_lb" "this" {
  name                       = "${var.project}-alb"
  load_balancer_type         = "application"
  internal                   = false
  ip_address_type            = "ipv4"
  security_groups            = [aws_security_group.alb.id]
  subnets                    = aws_subnet.public[*].id
  enable_deletion_protection = false
  drop_invalid_header_fields = true
}

resource "aws_lb_target_group" "app" {
  for_each = local.apps

  name                 = each.value.target_group
  port                 = var.container_port
  protocol             = "HTTP"
  protocol_version     = "HTTP1"
  target_type          = "ip"
  vpc_id               = aws_vpc.this.id
  deregistration_delay = 10

  health_check {
    enabled             = true
    path                = var.health_check_path
    protocol            = "HTTP"
    matcher             = "200-399"
    interval            = 10
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
  }
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type = "fixed-response"

    fixed_response {
      content_type = "text/plain"
      message_body = "shipyard: no application for this request\n"
      status_code  = "404"
    }
  }
}

resource "aws_lb_listener_rule" "app" {
  for_each = local.apps

  listener_arn = aws_lb_listener.http.arn
  priority     = each.value.priority

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.app[each.key].arn
  }

  dynamic "condition" {
    for_each = var.base_domain == "" ? [1] : []

    content {
      path_pattern {
        values = ["/apps/${each.key}", "/apps/${each.key}/*"]
      }
    }
  }

  dynamic "condition" {
    for_each = var.base_domain == "" ? [] : [1]

    content {
      host_header {
        values = ["${each.key}.${var.base_domain}"]
      }
    }
  }
}

locals {
  apps = {
    for idx, name in var.app_names : name => {
      priority     = 100 + idx
      target_group = substr("sy-${name}", 0, 32)
    }
  }
}
