# What the AWS stack costs

These numbers are an **estimate** from AWS public list prices for **US East (N. Virginia)**, `us-east-1`. They are not a quote and not a bill. AWS can change the prices, and tax is extra. The figures below were checked against the public pricing pages in November 2026:

- [Elastic Load Balancing pricing](https://aws.amazon.com/elasticloadbalancing/pricing/)
- [AWS Fargate pricing](https://aws.amazon.com/fargate/pricing/)
- [Amazon VPC pricing](https://aws.amazon.com/vpc/pricing/) (public IPv4 and NAT gateway)
- [Amazon ECR pricing](https://aws.amazon.com/ecr/pricing/)
- [Amazon CloudWatch pricing](https://aws.amazon.com/cloudwatch/pricing/)

A month here is 730 hours. The stack is one Application Load Balancer, two public subnets, no NAT gateway, and one small Fargate task that runs the whole month. Traffic is a demo: almost no requests.

**The Application Load Balancer is the main cost.** The hourly charge plus the two public IPv4 addresses AWS attaches to it (one per availability zone) is about $24 of the monthly total, even if no task is running. `shipyard stop` scales the task to zero and leaves the load balancer in place. `make aws-down` deletes the load balancer.

| Line | Public list price | This stack, all month |
| --- | --- | --- |
| Application Load Balancer, hours | $0.0225 per hour | $16.43 |
| Load balancer capacity (LCU), almost no traffic | $0.008 per LCU-hour | about $1, or about $5.84 if it averages 1 LCU |
| Public IPv4, two addresses on the load balancer | $0.005 per address-hour | $7.30 |
| Fargate, 0.25 vCPU | $0.04048 per vCPU-hour | $7.39 |
| Fargate, 0.5 GB memory | $0.004445 per GB-hour | $1.62 |
| Public IPv4, one address on the task | $0.005 per address-hour | $3.65 |
| ECR storage for a few small images | $0.10 per GB-month | under $1, often inside the free tier for a new account |
| CloudWatch Logs, 7-day retention | $0.50 per GB ingested after 5 GB free | about $0 for a demo |
| NAT gateway | $0.045 per hour, plus data | $0, because this stack does not create one |
| Data transfer out | first 100 GB per month free, then $0.09 per GB | $0 for a demo |
| **Total if left running all month** |  | **about $37 to $42 before tax** |

A four-hour session, followed by `make aws-down`, is about **$0.20** at the same rates. The monthly number is what you pay if the stack stays up.

## How to stay on the small task size

`shipyardd` defaults to 0.5 CPU and 256 MiB. Fargate cannot run that pair. The ECS runtime rounds up to the smallest legal size that covers the request, which is 0.5 vCPU and 1 GB, about $18 a month for the task instead of about $9. Before you deploy, set:

```bash
export SHIPYARD_CPUS=0.25
export SHIPYARD_MEMORY_BYTES=536870912
```

That is 0.25 vCPU and 512 MiB, the size in the table. 8 vCPU needs at least 16 GB on Fargate, so a large CPU setting rounds the memory up too.

## Why there is no NAT gateway

Tasks sit in public subnets and get a public IP so they can pull from ECR and write logs. A NAT gateway would be about $33 a month in one availability zone before any data charges, which is more than the task. The security group still allows inbound traffic only from the load balancer.

## The $5 budget is a tripwire

[docs/aws-setup.md](aws-setup.md) asks you to create a $5 monthly budget alert **before** `make aws-up`. This estimate is above $5, so the alert is expected if the stack is left up. The alert sends email. It does not stop the resources. `make aws-down` does.
