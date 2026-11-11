#!/usr/bin/env bash
# Delete ECS services shipyardd created so terraform destroy can remove the
# target groups they are attached to. Safe to run when nothing has been applied.
set -euo pipefail

cd "$(dirname "$0")"

if [[ ! -f terraform.tfstate ]]; then
  echo "no local terraform.tfstate; skipping ECS service cleanup"
  exit 0
fi

if ! command -v aws >/dev/null 2>&1; then
  echo "aws CLI v2 is required to delete ECS services before destroy" >&2
  exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
  echo "python3 is required to read the ECS service list" >&2
  exit 1
fi

cluster="$(terraform output -raw cluster_name)"
region="$(terraform output -raw aws_region)"
errfile="$(mktemp)"
trap 'rm -f "$errfile"' EXIT

token=""
page_n=0
while true; do
  page_n=$((page_n + 1))
  if [[ "$page_n" -gt 20 ]]; then
    echo "too many ECS service pages; stop and inspect the cluster" >&2
    exit 1
  fi
  args=(ecs list-services --no-paginate --cluster "$cluster" --region "$region" --output json)
  if [[ -n "$token" ]]; then
    args+=(--starting-token "$token")
  fi
  if ! page="$(aws "${args[@]}" 2>"$errfile")"; then
    if grep -q 'ClusterNotFoundException' "$errfile"; then
      echo "cluster ${cluster} is already gone"
      exit 0
    fi
    cat "$errfile" >&2
    exit 1
  fi
  while IFS= read -r arn; do
    [[ -z "$arn" ]] && continue
    name="${arn##*/}"
    echo "deleting ECS service ${name}"
    aws ecs delete-service --cluster "$cluster" --service "$name" --force --region "$region" >/dev/null
    aws ecs wait services-inactive --cluster "$cluster" --services "$name" --region "$region"
  done < <(printf '%s' "$page" | python3 -c 'import json,sys
data=json.load(sys.stdin)
for arn in data.get("serviceArns") or []:
    print(arn)
')
  token="$(printf '%s' "$page" | python3 -c 'import json,sys
data=json.load(sys.stdin)
print(data.get("nextToken") or data.get("NextToken") or "")
')"
  if [[ -z "$token" || "$token" == "None" ]]; then
    break
  fi
done
