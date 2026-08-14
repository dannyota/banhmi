# AWS read-path runbook

This runbook covers the public Model Context Protocol (MCP) read path in AWS Singapore
(`ap-southeast-1`). It does not provision the write pipeline.

## Current production shape

Production was verified on 2026-08-14:

| Component | Current configuration |
|---|---|
| Public ingress | Six CloudFront distributions with ACM TLS certificates |
| MCP | One `banhmi-mcp` ECS service serving `vn,my,id,sg,th,kh` on port 8081 |
| Query embedding | One `banhmi-embedder` ECS service on `127.0.0.1:8089` |
| ECS capacity | One ARM64 EC2 `t4g.medium` host |
| Database | One private RDS PostgreSQL 17 `db.t4g.micro` instance with six databases |
| Availability | EC2 and RDS run 24/7 |

The six former EventBridge Scheduler start and stop schedules are disabled. They are retained only
as rollback artifacts; they do not control normal availability.

## Checked-in file limits

Do not apply every file under `deploy/aws/` unchanged. Some files record earlier deployments:

- `create-distributions.sh`, `cloudfront-config.json`, and `security-group.json` cover the former
  three-domain, three-port layout. They do not add the current jurisdiction routing header.
- Both ECS task definitions contain account, RDS, and mutable image-tag placeholders.
- `ecs-task-definition.json` has no `taskRoleArn`, but the directory-verification handler reads
  `s3://danny-banhmi-public/challenge/*` at request time.
- `buildspec-mcp.yml` describes CodeBuild, which was retired on 2026-08-02.
- `run-pipeline.sh` and `run-embed.sh` cover only `vn`, `my`, and `id`. They are not the production
  read-path scheduler.

Create reviewed deployment copies before running AWS write commands. Pin images by immutable tag or
digest. Never deploy `:latest` or `:embedder-latest`.

## Prerequisites

- AWS CLI v2 with permission to manage ACM, CloudFront, EC2, ECS, ECR, IAM, RDS security groups,
  CloudWatch Logs, and Systems Manager Parameter Store.
- An existing VPC and subnet in `ap-southeast-1` with private network access to RDS.
- A private RDS PostgreSQL 17 instance containing the six databases: `banhmi`, `laksa`, `rendang`,
  `kaya`, `tomyum`, and `amok`.
- ARM64 MCP and embedder images pushed to ECR under immutable tags.
- A DNS zone for the six public names.

Record the account, network, image, and certificate identifiers before provisioning. The examples
below use explicit placeholders to prevent accidental writes to the wrong resource.

## 1. TLS certificate

CloudFront requires its ACM certificate in `us-east-1`.

```bash
aws acm request-certificate \
  --region us-east-1 \
  --domain-name "*.danny.vn" \
  --validation-method DNS \
  --query 'CertificateArn' \
  --output text
```

Add the returned DNS validation record, then check for `ISSUED`:

```bash
aws acm describe-certificate \
  --region us-east-1 \
  --certificate-arn ACM_CERT_ARN \
  --query 'Certificate.Status' \
  --output text
```

Requesting another certificate creates another resource. Check existing certificates first.

## 2. Log groups

Create one group for each ECS service. A duplicate create returns
`ResourceAlreadyExistsException`; that error needs no corrective action.

```bash
aws logs create-log-group \
  --region ap-southeast-1 \
  --log-group-name /ecs/banhmi-mcp

aws logs create-log-group \
  --region ap-southeast-1 \
  --log-group-name /ecs/banhmi-embedder
```

Set retention explicitly if logs must not be kept forever.

## 3. Network controls

Create one security group for the ECS host. Allow port 8081 only from the AWS-managed CloudFront
origin-facing prefix list. Port 8089 stays on loopback and needs no inbound rule.

```bash
CF_PREFIX_LIST_ID=$(aws ec2 describe-managed-prefix-lists \
  --region ap-southeast-1 \
  --filters "Name=prefix-list-name,Values=com.amazonaws.global.cloudfront.origin-facing" \
  --query 'PrefixLists[0].PrefixListId' \
  --output text)

aws ec2 authorize-security-group-ingress \
  --region ap-southeast-1 \
  --group-id MCP_SECURITY_GROUP_ID \
  --ip-permissions \
    "IpProtocol=tcp,FromPort=8081,ToPort=8081,PrefixListIds=[{PrefixListId=${CF_PREFIX_LIST_ID},Description=CloudFront}]"
```

The current Bottlerocket host does not use normal SSH administration. Use its approved Systems
Manager access path instead of opening port 22.

Allow the MCP security group to reach port 5432 on the RDS security group:

```bash
aws ec2 authorize-security-group-ingress \
  --region ap-southeast-1 \
  --group-id RDS_SECURITY_GROUP_ID \
  --protocol tcp \
  --port 5432 \
  --source-group MCP_SECURITY_GROUP_ID
```

Keep RDS private. Do not add `0.0.0.0/0` ingress for PostgreSQL.

## 4. ECS capacity

The current host is an ARM64 Bottlerocket `aws-ecs-2` image on `t4g.medium`. It needs the
`ecsInstanceRole` instance profile and Bottlerocket ECS settings that register it with the `banhmi`
cluster. Do not use the Amazon Linux `ECS_CLUSTER=...` user-data format for this image.

Use an Elastic IP because the six CloudFront distributions share the EC2 public DNS name derived from
that address. Allocate and launch only after checking for existing resources; both operations create
billable resources.

```bash
aws ec2 describe-addresses --region ap-southeast-1
aws ec2 describe-instances \
  --region ap-southeast-1 \
  --filters 'Name=tag:Name,Values=banhmi-mcp' \
  --query 'Reservations[].Instances[].{Id:InstanceId,State:State.Name,Type:InstanceType}'
```

After the host starts, associate its Elastic IP. Record the resulting EC2 public DNS name, then
confirm that the ECS container instance registers:

```bash
aws ecs create-cluster \
  --region ap-southeast-1 \
  --cluster-name banhmi

aws ecs list-container-instances \
  --region ap-southeast-1 \
  --cluster banhmi
```

## 5. Secrets

The task definitions expect three SecureString parameters:

```bash
aws ssm put-parameter \
  --region ap-southeast-1 \
  --name /banhmi/db-password \
  --type SecureString \
  --value 'DATABASE_PASSWORD'

aws ssm put-parameter \
  --region ap-southeast-1 \
  --name /banhmi/origin-verify \
  --type SecureString \
  --value "$(openssl rand -hex 32)"

aws ssm put-parameter \
  --region ap-southeast-1 \
  --name /banhmi/embed-token \
  --type SecureString \
  --value "$(openssl rand -hex 32)"
```

Add `--overwrite` only when rotating an existing parameter. During origin-secret rotation, the
server accepts comma-separated current and previous values. Remove the previous value after all six
CloudFront distributions use the new one.

## 6. IAM roles

The ECS task execution role needs:

- `AmazonECSTaskExecutionRolePolicy` for ECR pulls and CloudWatch log delivery;
- `ssm:GetParameters` for `arn:aws:ssm:ap-southeast-1:ACCOUNT_ID:parameter/banhmi/*`;
- `kms:Decrypt` when the SecureStrings use a customer-managed KMS key.

The EC2 instance profile needs `AmazonEC2ContainerServiceforEC2Role` so the host can join the ECS
cluster.

The MCP runtime also reads directory-verification tokens from
`s3://danny-banhmi-public/challenge/*`. If those routes are used, create a separate ECS task role
with scoped `s3:GetObject` access and add its ARN as `taskRoleArn` in the MCP task definition. Do not
put this permission on the execution role.

## 7. ECR and images

Create the repository once:

```bash
aws ecr describe-repositories \
  --region ap-southeast-1 \
  --repository-names banhmi-mcp \
  >/dev/null 2>&1 || \
aws ecr create-repository \
  --region ap-southeast-1 \
  --repository-name banhmi-mcp \
  --image-tag-mutability IMMUTABLE
```

Build locally from:

- `deploy/containerfiles/Containerfile.ecs.server` for the slim MCP image;
- `deploy/containerfiles/Containerfile.ecs.embedder` for the ARM64 query embedder.

See [the build-cache guide](../BUILD-CACHE.md) before setting `CACHE_BASE`. The cache bucket is
private, and the Containerfile cannot authenticate to S3 itself.

## 8. ECS task definitions and services

Before registration, make reviewed copies of both JSON task definitions and replace:

- `YOUR_ACCOUNT_ID`;
- `YOUR_RDS_ENDPOINT`;
- the mutable image tags with immutable tags or digests;
- `taskRoleArn` for the MCP service when S3 directory verification is required.

Keep these current settings:

| Setting | MCP | Embedder |
|---|---|---|
| Service | `banhmi-mcp` | `banhmi-embedder` |
| Network | host | host |
| Address | `:8081` | `127.0.0.1:8089` |
| Memory reservation | 300 MiB | 2400 MiB |
| Jurisdictions | `vn,my,id,sg,th,kh` | not applicable |
| Root filesystem | read-only | read-only |
| Linux capabilities | all dropped | all dropped |

Register both reviewed files:

```bash
aws ecs register-task-definition \
  --region ap-southeast-1 \
  --cli-input-json file://PATH_TO_REVIEWED_MCP_TASK_DEFINITION.json

aws ecs register-task-definition \
  --region ap-southeast-1 \
  --cli-input-json file://PATH_TO_REVIEWED_EMBEDDER_TASK_DEFINITION.json
```

For a new deployment, start the embedder first and wait for its health check. Then start the MCP
service. Set `minimumHealthyPercent=0` and `maximumPercent=100` for both services. Their fixed host
addresses prevent overlapping revisions; the host also lacks memory for two embedder tasks.

```bash
aws ecs create-service \
  --region ap-southeast-1 \
  --cluster banhmi \
  --service-name banhmi-embedder \
  --task-definition banhmi-embedder:REVISION \
  --desired-count 1 \
  --launch-type EC2 \
  --deployment-configuration minimumHealthyPercent=0,maximumPercent=100

aws ecs wait services-stable \
  --region ap-southeast-1 \
  --cluster banhmi \
  --services banhmi-embedder

aws ecs create-service \
  --region ap-southeast-1 \
  --cluster banhmi \
  --service-name banhmi-mcp \
  --task-definition banhmi-mcp:REVISION \
  --desired-count 1 \
  --launch-type EC2 \
  --deployment-configuration minimumHealthyPercent=0,maximumPercent=100
```

`create-service` fails when that service name already exists. Use `update-service` for an existing
deployment.

For an update, change one service at a time and wait for stability:

```bash
aws ecs update-service \
  --region ap-southeast-1 \
  --cluster banhmi \
  --service banhmi-embedder \
  --task-definition banhmi-embedder:REVISION \
  --deployment-configuration minimumHealthyPercent=0,maximumPercent=100

aws ecs wait services-stable \
  --region ap-southeast-1 \
  --cluster banhmi \
  --services banhmi-embedder

aws ecs update-service \
  --region ap-southeast-1 \
  --cluster banhmi \
  --service banhmi-mcp \
  --task-definition banhmi-mcp:REVISION \
  --deployment-configuration minimumHealthyPercent=0,maximumPercent=100

aws ecs wait services-stable \
  --region ap-southeast-1 \
  --cluster banhmi \
  --services banhmi-mcp
```

## 9. CloudFront and DNS

Each current distribution uses the same EC2 public DNS origin on port 8081, but injects a different
jurisdiction. It must also inject the origin-verification secret. Read the current origin hostname
from the running EC2 instance or the existing distributions; do not hard-code a dated hostname in
this runbook.

| Public name | `X-Banhmi-Jurisdiction` |
|---|---|
| `banhmi.danny.vn` | `vn` |
| `laksa.danny.vn` | `my` |
| `rendang.danny.vn` | `id` |
| `kaya.danny.vn` | `sg` |
| `tomyum.danny.vn` | `th` |
| `amok.danny.vn` | `kh` |

Required origin settings:

- the shared EC2 public DNS custom origin, HTTP port 8081, `http-only`;
- custom header `X-Origin-Verify` with the value from `/banhmi/origin-verify`;
- custom header `X-Banhmi-Jurisdiction` with the table value;
- all MCP HTTP methods allowed;
- caching disabled for MCP requests;
- the ACM certificate from `us-east-1` and TLS 1.2 or later.

Do not run `deploy/aws/create-distributions.sh` for this topology without first updating it for all
six domains, one origin port, and both custom headers.

Point each public CNAME at its own generated CloudFront distribution domain.

## 10. Verification

Check ECS state first:

```bash
aws ecs describe-services \
  --region ap-southeast-1 \
  --cluster banhmi \
  --services banhmi-mcp banhmi-embedder \
  --query 'services[].{Service:serviceName,Desired:desiredCount,Running:runningCount,Task:taskDefinition}'
```

Check all public health endpoints:

```bash
for host in banhmi laksa rendang kaya tomyum amok; do
  curl --fail --silent --show-error "https://${host}.danny.vn/healthz"
done
```

Call one MCP tool through each domain, not only `/healthz`:

```bash
for host in banhmi laksa rendang kaya tomyum amok; do
  curl --fail --silent --show-error \
    --request POST \
    --header 'Content-Type: application/json' \
    --data '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"corpus_status","arguments":{}}}' \
    "https://${host}.danny.vn/mcp"
done
```

Direct `/mcp` requests to the origin must be rejected without `X-Origin-Verify`. The direct
`/healthz` route is intentionally exempt for ECS health checks.

## 11. Availability

EC2 and RDS must remain running. Confirm that the retained schedules stay disabled:

```bash
for name in \
  banhmi-ec2-start banhmi-ec2-start-net banhmi-ec2-stop \
  banhmi-rds-start banhmi-rds-start-net banhmi-rds-stop; do
  aws scheduler get-schedule \
    --region ap-southeast-1 \
    --name "$name" \
    --query '{Name:Name,State:State}'
done
```

Every result should show `DISABLED`. Do not re-enable the old schedules without reviewing their
targets and expressions; their former daily windows conflict with 24/7 operation.

## 12. Recovery

### ECS task failure

1. Inspect service events with `aws ecs describe-services`.
2. Inspect `/ecs/banhmi-mcp` and `/ecs/banhmi-embedder` logs.
3. Confirm that the EC2 container instance is registered and has enough free memory.
4. Roll back to the last known immutable task-definition revision.
5. Run the public health and MCP checks again.

If the MCP service cannot reach the embedder, restore `banhmi-embedder` first. The MCP startup probe
waits about three minutes for the embedder before the task exits.

### EC2 host failure

Launch a replacement ARM64 ECS-optimized host in the same VPC, attach the ECS instance profile and
MCP security group, associate the origin Elastic IP, and wait for both services to reach their
desired count. Do not terminate the failed host until its EBS recovery value is known.

### Database failure

RDS automated backups are currently disabled. The recorded recovery source is the per-database S3
logical dump. Verify a dump's timestamp and restore it to a separate database before changing a
production database. The repository does not yet define one tested production rename or cutover
procedure, so database recovery requires an explicit reviewed plan.

### Pre-split image rollback

The old in-process image reserves more memory. Scale `banhmi-embedder` to zero before placing it on
the 4 GiB host. Use only the recorded immutable pre-split image tag and restore
`BANHMI_EMBED_QUERY=onnx`. This is an emergency rollback, not the current deployment shape.

## Cost estimate

These are planning estimates, not billing guarantees:

| Component | Approximate monthly cost | Notes |
|---|---:|---|
| EC2 `t4g.medium` | $25 | On-demand, 24/7 |
| RDS `db.t4g.micro` | $25 | PostgreSQL, 24/7 |
| Elastic IPv4 | $3.60 | One public origin address |
| EBS 16 GiB gp3 | $1.28 | Root volume |
| Six CloudFront distributions | $1-2 | Low traffic assumption |
| ECR and CloudWatch Logs | Less than $1 | Low storage and log volume assumption |
| SSM standard parameters and ACM | $0 | Standard tier and public certificate |
| **Estimated total** | **$57-58** | Before data transfer, taxes, or discounts |

Check the AWS bill and current regional pricing before using these estimates for a budget.

## Decommissioning

Decommission in dependency order:

1. Remove or replace the six public DNS records.
2. Disable each CloudFront distribution, wait for deployment, then delete it.
3. Scale both ECS services to zero, then delete them.
4. Terminate the EC2 instance only after preserving any needed EBS data.
5. Disassociate and release the Elastic IP; the address cannot be recovered afterward.
6. Delete SSM parameters only after recording any values still needed for rollback.
7. Remove security groups after all attached resources are gone.
8. Delete the ACM certificate only after CloudFront no longer references it.

RDS and its six databases are outside this read-path teardown. Delete them only under a separate,
verified data-retention and recovery plan.
