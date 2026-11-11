# Set up AWS for Shipyard

This is the first time the AWS path is applied. You can read it before you have an account. Nothing in CI applies it, and the tests do not call AWS.

Local mode (`make run`, the default) does not need any of this. Come back when you want the example app on Fargate.

Do the steps in order. The load balancer is the expensive part, and it starts billing when `make aws-up` finishes. Read [cost.md](cost.md) first. The short version: about $37 to $42 if you leave the stack up for a month, and about $0.20 if you tear it down after an afternoon. `make aws-down` is how you stop the bill.

You will create:

- an AWS account, with MFA on the root user
- an IAM user (or IAM Identity Center access) that can build this stack and nothing broader than the services it uses
- a $5 budget alert
- a VPC with two public subnets and no NAT gateway
- an ECR repository that keeps the last 5 images
- an ECS cluster, a task execution role, and a task role with no permissions
- a CloudWatch log group kept for 7 days
- an Application Load Balancer with a path rule for the `hello` app

## 1. Create the account

1. Open [https://aws.amazon.com/](https://aws.amazon.com/) and choose **Create an AWS Account**.
2. Use an email address you control, a long password, and the account name `shipyard`.
3. Choose the free plan if AWS asks. The free plan does not make the load balancer free. The prices in [cost.md](cost.md) still apply.
4. Enter a payment method and confirm your identity. AWS requires both.
5. Pick **US East (N. Virginia)** when a region is offered. The Terraform default is `us-east-1`, which matches the cost table.

Sign in once as the **root user** for the next step, then stop using root for daily work.

## 2. Turn on MFA for the root user

1. Sign in as root. The root user is the email address of the account, not an IAM user.
2. Open the account menu (your name at the top right) and choose **Security credentials**.
3. Under **Multi-factor authentication (MFA)**, choose **Assign MFA device**.
4. Name it `root-mfa`. Choose **Authenticator app**.
5. Scan the QR code with an authenticator app on your phone and enter two codes in a row.
6. Sign out and sign back in. AWS should ask for the MFA code. If it does, root MFA is on.

Do not create access keys for the root user.

## 3. Create an IAM user

The user `shipyard-deploy` is the identity you put in `aws configure`. Terraform and `shipyardd` both use it. The app container does not. Its task role has no AWS permissions.

If your company uses **IAM Identity Center** (sometimes still called AWS SSO), skip the access key and do the short section at the end of this step instead.

1. In the console search box, open **IAM**.
2. Choose **Policies**, then **Create policy**, then the **JSON** tab.
3. Paste the contents of [`infra/aws/iam-deployer.json`](../infra/aws/iam-deployer.json).
4. Name the policy `ShipyardDeploy` and create it.

   The policy is not administrator access. It can manage VPC networking, the load balancer, ECS, ECR, logs, and the two `*-ecs-execution` and `*-ecs-task` roles in this account. Many of those APIs do not allow a tighter resource limit. The app still runs with an empty task role.

5. Choose **Users**, then **Create user**. Name it `shipyard-deploy`.
6. Attach the `ShipyardDeploy` policy directly. Do not attach `AdministratorAccess`.
7. Open the user, then **Security credentials**.
8. Turn on MFA for this user the same way as root. Use a different device name, such as `shipyard-deploy-mfa`.
9. Choose **Create access key**. Pick **Command Line Interface**. Confirm the warning. Copy the access key id and the secret. The secret is shown once.

### IAM Identity Center instead of an access key

Create a permission set whose policy is the same JSON, assign it to your user in this account, and install the AWS CLI. Then run `aws configure sso` and use the profile it writes. The rest of this guide is the same. Skip the access key.

## 4. Set a $5 budget alert

Do this before `make aws-up`.

1. In the console search box, open **Budgets** (under Billing and Cost Management). If AWS asks you to enable billing access, confirm it.
2. Choose **Create budget**.
3. Choose **Customize**, then **Cost budget**, or the template **Monthly cost budget** if that is what the page offers.
4. Name it `shipyard-5`. Period **Monthly**. Budget amount **5** USD.
5. Add an alert at **80% of actual** cost and another at **100% of actual** cost. Use your email address.
6. Create the budget.

The alert sends mail. It does not delete anything. The estimate in [cost.md](cost.md) is above $5, so you should expect mail if the stack stays up. When you get it, run `make aws-down` if you are done.

## 5. Install the tools

On the machine that will run Terraform and `shipyardd`:

- Terraform 1.5 or newer (`terraform version`)
- AWS CLI v2 (`aws --version`)
- Python 3, which `make aws-down` uses to read the service list (`python3 --version`)
- Docker, Git, and Go 1.22 or newer, the same as local mode

## 6. Configure the AWS CLI

```bash
aws configure
```

- Access key and secret: the `shipyard-deploy` keys from step 3
- Region: `us-east-1`
- Output: `json`

Check that you are that user, not root:

```bash
aws sts get-caller-identity
```

The ARN should contain `user/shipyard-deploy`. If it contains `:root`, stop and switch keys.

## 7. Create the stack

From the repository root:

```bash
make aws-up
```

Terraform downloads the AWS provider, shows a plan, and waits. Read the plan. You want a VPC, two public subnets, an internet gateway, no NAT gateway, one ECR repository, one ECS cluster, two IAM roles, one log group, one load balancer, and a target group plus a listener rule for `hello`. Type `yes`.

The load balancer takes a few minutes. When it finishes, billing for it has started.

Copy the environment into the shell you will use for `shipyardd`:

```bash
terraform -chdir=infra/aws output -raw shipyard_env
```

Paste those `export` lines into the terminal. Also pin the small Fargate size from the cost note:

```bash
export SHIPYARD_CPUS=0.25
export SHIPYARD_MEMORY_BYTES=536870912
```

`terraform.tfstate` is now in `infra/aws/`. It is gitignored. Do not delete it before `make aws-down`. Terraform uses it to know what to delete. It does not contain your access key.

## 8. Deploy the example

`examples/hello` is a tiny nginx page. Shipyard only builds a Dockerfile at the **root** of a GitHub repository, and in this repo that file is under `examples/hello/`. Put `Dockerfile` and `index.html` from `examples/hello` at the root of a new GitHub repository. A public repository is enough.

The default Terraform app name is `hello`. The app you create must use that name, because the listener rule and target group are `sy-hello`. Another name needs an entry in `app_names` and another `make aws-up`.

In the terminal where you pasted the exports:

```bash
make build
make run
```

`shipyardd` still listens on your machine. It builds with the local Docker daemon, pushes the image to ECR, and updates ECS. Leave it running.

In a second terminal:

```bash
./bin/shipyard apps create --name hello --repo https://github.com/<you>/<repo>
./bin/shipyard deploy hello --ref main
./bin/shipyard logs dep_1 --follow
```

`logs --follow` exits when the deployment is `running`. The first Fargate start often takes a few minutes. The log then shows a URL like:

```text
http://<alb-dns-name>/apps/hello/
```

Open that URL, or:

```bash
curl -fsS "http://$(terraform -chdir=infra/aws output -raw alb_dns_name)/apps/hello/"
```

You should see `hello from shipyard`.

### Roll back

Rollback does not build again. It points the ECS service at the previous task definition and waits until the target group is healthy.

```bash
./bin/shipyard releases hello
./bin/shipyard rollback hello
./bin/shipyard logs dep_2 --follow
```

### Stop the task, or stop the bill

```bash
./bin/shipyard stop hello
```

That scales the service to zero. The load balancer is still there and still costs money.

When you are finished, stop `shipyardd` and destroy the stack. This is the step that matters:

```bash
make aws-down
```

Type `yes`. The command deletes ECS services first, so the target group is not stuck, then runs `terraform destroy`. Wait until it says destroy is complete. In the console, **EC2 → Load Balancers** and **ECS → Clusters** should be empty.

If destroy fails because a service is still attached, run `make aws-down` again. Do not leave a failed destroy overnight.

## 9. Optional: remote state

The default backend is a local file, `infra/aws/terraform.tfstate`. That is the right choice for one person learning the stack. CI runs `terraform init -backend=false` and does not read this file.

Use S3 when more than one person runs apply. Create the bucket and the lock table yourself first. This Terraform config cannot create the bucket it stores state in.

1. Create an S3 bucket in `us-east-1` with versioning and default encryption. Block public access.
2. Create a DynamoDB table named `shipyard-tf-lock` with partition key `LockID` (string). On-demand capacity is enough.
3. In `infra/aws/versions.tf`, replace the `backend "local"` block with:

```hcl
backend "s3" {
  bucket         = "your-shipyard-tfstate"
  key            = "shipyard/terraform.tfstate"
  region         = "us-east-1"
  encrypt        = true
  dynamodb_table = "shipyard-tf-lock"
}
```

The same text is in [`infra/aws/backend.s3.example.hcl`](../infra/aws/backend.s3.example.hcl), which Terraform does not load.

4. Move the existing state:

```bash
terraform -chdir=infra/aws init -migrate-state
```

`make aws-down` only looks for a local state file before it deletes services. After you move state, delete services yourself (`aws ecs list-services` and `aws ecs delete-service --force`) before `terraform destroy`, or copy the state back locally.

## If something fails

- **AccessDenied** during apply: the error names the missing action. Add that action to `ShipyardDeploy` and run `make aws-up` again.
- **`target group sy-hello was not found`**: the app name is not in `app_names`. The default list is `hello`. Add the name in `infra/aws/terraform.tfvars` (see `terraform.tfvars.example`) and apply again. Names must be at most 29 characters.
- **The page is the load balancer's 404**: the path is `/apps/hello/`, with the trailing slash. The host rule is used only when `base_domain` is set. This stack does not create a DNS name or a TLS certificate.
- **Docker login**: `shipyardd` passes the ECR password on stdin, not on the command line. Docker still stores the token in your local Docker config. It expires. The next deploy logs in again.
