# AWS read path setup

Step-by-step CLI commands to provision the banhmi MCP read path on AWS.
All commands use `ap-southeast-1` unless noted. Fill in `YOUR_*` placeholders before running.

## Prerequisites

- AWS CLI v2 configured with IAM credentials
- RDS instance already running in `ap-southeast-1a` (same VPC)
- ECR image pushed (ARM64, built from `Containerfile.ecs.onnx`)
- `origin.danny.vn` A record ready to point at the Elastic IP (create after step 3)

## 1. ACM certificate (us-east-1, idempotent)

CloudFront requires certs in `us-east-1`. DNS validation; costs nothing.

```bash
aws acm request-certificate \
  --region us-east-1 \
  --domain-name "*.danny.vn" \
  --validation-method DNS \
  --query 'CertificateArn' --output text
```

Add the CNAME validation record to DNS. Check status:

```bash
aws acm describe-certificate \
  --region us-east-1 \
  --certificate-arn ACM_CERT_ARN \
  --query 'Certificate.Status'
```

Wait for `ISSUED` (minutes to hours). **Idempotent** -- requesting again with the same domain returns the existing cert ARN.

## 2. CloudWatch log group (idempotent)

```bash
aws logs create-log-group \
  --log-group-name /ecs/banhmi-mcp \
  --region ap-southeast-1
```

**Idempotent** -- returns `ResourceAlreadyExistsException` if it exists.

## 3. Security group (idempotent create; additive rules)

```bash
# Create
SG_ID=$(aws ec2 create-security-group \
  --group-name banhmi-mcp-sg \
  --description "banhmi MCP ECS instance" \
  --vpc-id YOUR_VPC_ID \
  --query 'GroupId' --output text)

echo "Security group: $SG_ID"

# Get CloudFront prefix list ID
CF_PL=$(aws ec2 describe-managed-prefix-lists \
  --filters "Name=prefix-list-name,Values=com.amazonaws.global.cloudfront.origin-facing" \
  --query 'PrefixLists[0].PrefixListId' --output text)

echo "CloudFront prefix list: $CF_PL"

# Inbound: MCP ports from CloudFront only
aws ec2 authorize-security-group-ingress \
  --group-id "$SG_ID" \
  --ip-permissions \
    "IpProtocol=tcp,FromPort=8081,ToPort=8083,PrefixListIds=[{PrefixListId=$CF_PL,Description=CloudFront}]"

# Inbound: SSH from maintainer
aws ec2 authorize-security-group-ingress \
  --group-id "$SG_ID" \
  --protocol tcp --port 22 \
  --cidr YOUR_MAINTAINER_IP/32
```

**Idempotent** -- duplicate rules return `InvalidPermission.Duplicate`. Outbound all is the default.

## 4. Elastic IP (costs ~$3.60/mo once allocated)

```bash
EIP_ALLOC=$(aws ec2 allocate-address \
  --domain vpc \
  --query 'AllocationId' --output text)

EIP_ADDR=$(aws ec2 describe-addresses \
  --allocation-ids "$EIP_ALLOC" \
  --query 'Addresses[0].PublicIp' --output text)

echo "Elastic IP: $EIP_ADDR (allocation: $EIP_ALLOC)"
```

Create DNS: `origin.danny.vn` A record pointing to `$EIP_ADDR`.

**Not idempotent** -- each call allocates a new IP. Release with `aws ec2 release-address --allocation-id $EIP_ALLOC`.

## 5. EC2 instance (costs ~$25/mo on-demand)

```bash
# Find latest Amazon Linux 2023 ARM64 ECS-optimized AMI
AMI_ID=$(aws ssm get-parameters \
  --names /aws/service/ecs/optimized-ami/amazon-linux-2023/arm64/recommended/image_id \
  --query 'Parameters[0].Value' --output text)

echo "AMI: $AMI_ID"

INSTANCE_ID=$(aws ec2 run-instances \
  --image-id "$AMI_ID" \
  --instance-type t4g.medium \
  --key-name YOUR_KEY_PAIR_NAME \
  --security-group-ids "$SG_ID" \
  --subnet-id YOUR_SUBNET_ID_AP_SOUTHEAST_1A \
  --placement "AvailabilityZone=ap-southeast-1a" \
  --iam-instance-profile Name=ecsInstanceRole \
  --user-data '#!/bin/bash
echo ECS_CLUSTER=banhmi-mcp >> /etc/ecs/ecs.config' \
  --block-device-mappings '[{"DeviceName":"/dev/xvda","Ebs":{"VolumeSize":16,"VolumeType":"gp3"}}]' \
  --tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=banhmi-mcp}]' \
  --query 'Instances[0].InstanceId' --output text)

echo "Instance: $INSTANCE_ID"
```

Associate the Elastic IP:

```bash
aws ec2 associate-address \
  --instance-id "$INSTANCE_ID" \
  --allocation-id "$EIP_ALLOC"
```

**Not idempotent** -- each call launches a new instance. Terminate with `aws ec2 terminate-instances --instance-ids $INSTANCE_ID`.

## 6. ECS cluster (idempotent)

```bash
aws ecs create-cluster --cluster-name banhmi-mcp
```

Wait for the EC2 instance to register (1-2 minutes):

```bash
aws ecs list-container-instances --cluster banhmi-mcp
```

## 7. Secrets (SSM Parameter Store SecureStrings — free standard tier)

All injected secrets are SSM SecureString parameters (no Secrets Manager; it costs $0.40/secret/mo).

```bash
# DB password: the containers read discrete BANHMI_DATABASE_* envs — there is
# NO BANHMI_DATABASE_URL in the code. The password comes from the SSM
# SecureString /banhmi/db-password (see the task definition's secrets block);
# host/name are plain env vars. No db-url secrets needed.

# CloudFront origin secret — same value goes in create-distributions.sh
# (ORIGIN_VERIFY_SECRET) and reaches the containers as
# BANHMI_ORIGIN_VERIFY_SECRET (enforced server-side; comma-separate two
# values during rotation).
aws ssm put-parameter \
  --name /banhmi/origin-verify \
  --type SecureString \
  --value "$(openssl rand -hex 32)"
```

Update an existing parameter by adding `--overwrite`.

## 8. IAM roles (least privilege)

Two ECS roles, each scoped to the minimum actions needed.

### Execution role — ECS agent operations (pull images, inject secrets, write logs)

```bash
aws iam create-role \
  --role-name ecsTaskExecutionRole \
  --assume-role-policy-document '{
    "Version": "2012-10-17",
    "Statement": [{
      "Effect": "Allow",
      "Principal": {"Service": "ecs-tasks.amazonaws.com"},
      "Action": "sts:AssumeRole"
    }]
  }'

# ECR pull + CloudWatch Logs (managed policy)
aws iam attach-role-policy \
  --role-name ecsTaskExecutionRole \
  --policy-arn arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy

# SSM Parameter Store — scoped to banhmi parameters only
aws iam put-role-policy \
  --role-name ecsTaskExecutionRole \
  --policy-name banhmi-secrets \
  --policy-document '{
    "Version": "2012-10-17",
    "Statement": [{
      "Effect": "Allow",
      "Action": ["ssm:GetParameters"],
      "Resource": "arn:aws:ssm:ap-southeast-1:YOUR_ACCOUNT_ID:parameter/banhmi/*"
    }]
  }'
```

### EC2 instance role — ECS agent registration

The EC2 host needs `ecsInstanceRole` to register with the ECS cluster and pull
images. Uses the AWS-managed `AmazonEC2ContainerServiceforEC2Role` policy.

```bash
aws iam create-role \
  --role-name ecsInstanceRole \
  --assume-role-policy-document '{
    "Version": "2012-10-17",
    "Statement": [{
      "Effect": "Allow",
      "Principal": {"Service": "ec2.amazonaws.com"},
      "Action": "sts:AssumeRole"
    }]
  }'

aws iam attach-role-policy \
  --role-name ecsInstanceRole \
  --policy-arn arn:aws:iam::aws:policy/service-role/AmazonEC2ContainerServiceforEC2Role

aws iam create-instance-profile --instance-profile-name ecsInstanceRole
aws iam add-role-to-instance-profile \
  --instance-profile-name ecsInstanceRole \
  --role-name ecsInstanceRole
```

### No task role needed

The MCP containers access RDS via network (SCRAM-SHA-256 + TLS within the VPC).
They make no AWS SDK calls at runtime, so no `taskRoleArn` is set. If a future
feature needs S3 or other AWS services, add a scoped task role then.

## 9. ECR repository (idempotent)

```bash
aws ecr create-repository \
  --repository-name banhmi-mcp \
  --region ap-southeast-1 \
  --image-tag-mutability MUTABLE
```

## 10. ECS task + service

Before registering, replace `:latest` image tags in `ecs-task-definition.json` with a
specific version tag or `@sha256:` digest for reproducible deploys.

```bash
# Register task definition
aws ecs register-task-definition \
  --cli-input-json file://deploy/aws/ecs-task-definition.json

# Create service (1 task, no load balancer)
aws ecs create-service \
  --cluster banhmi-mcp \
  --service-name banhmi-mcp \
  --task-definition banhmi-mcp \
  --desired-count 1 \
  --launch-type EC2 \
  --deployment-configuration "minimumHealthyPercent=0,maximumPercent=100"
```

**Not idempotent** -- duplicate service name errors. Update with `aws ecs update-service`.

## 11. CloudFront distributions

Edit variables in `create-distributions.sh`, then run:

```bash
bash deploy/aws/create-distributions.sh
```

Wait for all 3 distributions to reach `Deployed` status:

```bash
aws cloudfront list-distributions \
  --query 'DistributionList.Items[?Comment!=`null`]|[?contains(Comment,`banhmi`)].{Id:Id,Domain:DomainName,Aliases:Aliases.Items[0],Status:Status}' \
  --output table
```

## 12. DNS updates

Create CNAME records pointing each domain to its CloudFront distribution domain:

| Record | Type | Value |
|--------|------|-------|
| `banhmi.danny.vn` | CNAME | `d1234example.cloudfront.net` |
| `laksa.danny.vn` | CNAME | `d5678example.cloudfront.net` |
| `rendang.danny.vn` | CNAME | `d9012example.cloudfront.net` |

Get the distribution domain names from step 11 output.

## 13. Smoke tests

```bash
# Health check (direct to origin, bypassing CloudFront)
curl -s -o /dev/null -w "%{http_code}" http://origin.danny.vn:8081/healthz
curl -s -o /dev/null -w "%{http_code}" http://origin.danny.vn:8082/healthz
curl -s -o /dev/null -w "%{http_code}" http://origin.danny.vn:8083/healthz

# Through CloudFront
curl -s https://banhmi.danny.vn/healthz
curl -s https://laksa.danny.vn/healthz
curl -s https://rendang.danny.vn/healthz

# MCP corpus_status (POST, Streamable HTTP)
curl -s -X POST https://banhmi.danny.vn/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"corpus_status","arguments":{}}}'

# Verify origin header enforcement (should be rejected without header)
curl -s -o /dev/null -w "%{http_code}" \
  --resolve "origin.danny.vn:8081:$(dig +short origin.danny.vn)" \
  http://origin.danny.vn:8081/mcp
```

## v0.4.0 cutover — embedder split (two ECS services)

Prereq (historical — CodeBuild retired 2026-08-02, project deleted; build locally instead, see
below): one build pushes both images into the `banhmi-mcp` ECR repo: `latest`/`<sha>` (slim MCP,
`Containerfile.ecs.server`) and `embedder-latest`/`embedder-<sha>` (`Containerfile.ecs.embedder`).
The last pre-split in-process image stays pinned by its sha tag for rollback.

**Current build method (local, ARM64 cross-build — needs `qemu-user-static`):**

```bash
aws ecr get-login-password | podman login --username AWS \
  --password-stdin YOUR_ACCOUNT_ID.dkr.ecr.ap-southeast-1.amazonaws.com
podman build --platform linux/arm64 -f Containerfile.ecs.server \
  -t YOUR_ACCOUNT_ID.dkr.ecr.ap-southeast-1.amazonaws.com/banhmi-mcp:<sha> .
podman push YOUR_ACCOUNT_ID.dkr.ecr.ap-southeast-1.amazonaws.com/banhmi-mcp:<sha>
```

**Pin every image by sha tag in task defs — never `:latest`** (2026-07-19 incident: the "rollback"
revision referenced `:latest`, which the split build had overwritten). **Validate the image before
any flip**: pull it, confirm the ELF interpreter exists in the image and go-fitz/MuPDF is statically
linked (`ldd` shows no mupdf; binary runs past init in a bare container).

```bash
# 1. Shared token (one-time)
aws ssm put-parameter --name /banhmi/embed-token --type SecureString --value "$(openssl rand -hex 32)"

# 2. Register both revisions (sha-pinned images; inert until the flips)
aws ecs register-task-definition --cli-input-json file://deploy/aws/ecs-task-definition.json
aws ecs register-task-definition --cli-input-json file://deploy/aws/ecs-task-definition-embedder.json

# 3. MCP FIRST, then embedder — the t4g.medium (3,829 MB registered) cannot hold the old
#    in-process task (2300 reserved) and the embedder (2400) at once; flipping MCP to the slim
#    revision frees the memory the embedder needs. The slim MCP crashloops harmlessly on its
#    embedder parity probe until the embedder is healthy (probe retries ~3 min; startPeriod 240 s).
aws ecs update-service --cluster banhmi --service banhmi-mcp --task-definition banhmi-mcp:<rev>
aws ecs update-service --cluster banhmi --service banhmi-embedder \
  --task-definition banhmi-embedder:<rev> --desired-count 1

# 4. Smoke: /healthz per jurisdiction + one MCP search through CloudFront (section 13)
```

Rollback (order matters on the 4 GB host): scale `banhmi-embedder` to `--desired-count 0` FIRST
(its reservation blocks the fat revision from placing), then `aws ecs update-service` the MCP
service to the sha-pinned pre-split revision (`banhmi-mcp:f3910556224b` image,
`BANHMI_EMBED_QUERY=onnx` env).

## Cost summary

| Component | Monthly | Notes |
|-----------|---------|-------|
| EC2 t4g.medium | ~$25 | on-demand; less with 1yr RI |
| Elastic IP | ~$3.60 | IPv4 pricing |
| EBS 16 GB gp3 | ~$1.28 | |
| CloudFront (3 dists) | ~$1-2 | low traffic |
| ECR | ~$0.10 | image storage |
| SSM Parameter Store | $0 | standard-tier SecureStrings |
| CloudWatch Logs | ~$0.50 | low volume |
| ACM cert | $0 | |
| **Total** | **~$57-58** | drops to ~$40 with RI |

## Rollback

1. **CloudFront** (safe): disable distribution, then delete. DNS CNAMEs become dangling (restore old CNAMEs first)
2. **ECS service** (safe): `aws ecs update-service --desired-count 0` stops tasks; `aws ecs delete-service` removes
3. **EC2** (destructive): `aws ec2 terminate-instances` -- instance and its EBS volume are destroyed
4. **Elastic IP** (safe): disassociate, then release -- the IP is lost
5. **Secrets** (safe): `aws ssm delete-parameter` removes SSM parameters (no recovery window — re-create from recorded values)
6. **Security group** (safe): delete after EC2 is terminated
7. **ACM cert** (safe): delete if no CloudFront distribution references it
8. ~~Full rollback to GCP~~ (retired 2026-07-12; GCP read path torn down)
