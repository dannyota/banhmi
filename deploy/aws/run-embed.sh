#!/usr/bin/env bash
set -euo pipefail

# Launch a self-terminating EC2 instance that runs ONE compute-only pipeline
# stage against RDS from INSIDE the VPC, then terminates.
#
#   bash deploy/aws/run-embed.sh {vn|my|id} [stage-flag ...]
#   bash deploy/aws/run-embed.sh id -embed-all -lexindex
#
# Each stage flag becomes its own /pipeline invocation, chained with && so a
# failure stops the rest. -lexindex must follow -embed-all: hybrid retrieval
# needs the BM25 sparse vectors rebuilt after new chunks land.
#
# Why this exists (separate from run-pipeline.sh, which runs -run-all):
# the compute stages — normalize, index, embed, lexindex — never touch a source
# website. They only read and write RDS (embed also talks to Kaggle). Only the
# CRAWL stages (discover/fetch) need the maintainer's residential IP, because VN
# sources geo-lock datacenter IPs and BPK serves them a CAPTCHA.
#
# Running compute in the VPC instead of on the laptop:
#   - RDS is reached privately (the instance SG is already allowed on 5432), so
#     there is no security-group allowlist to add and no exposure to the
#     maintainer's IP rotating mid-run — which silently stalled a 90-minute
#     embed at 57% on 2026-07-13.
#   - The vectors (~375 MB for a full ID embed) never cross the laptop's mobile
#     connection.
#
# Requires: ec2:RunInstances + iam:PassRole for banhmi-pipeline-ec2.

REGION=ap-southeast-1
ACCOUNT=847564369858
INSTANCE_TYPE=m7i.large           # the pipeline image is x86, not Graviton
INSTANCE_PROFILE=banhmi-pipeline-ec2
SUBNET=subnet-0fa3911347a181f21   # same VPC/subnet as RDS + the ECS origin
SECURITY_GROUP=sg-04f4a769e0e6ddbbd # already allowed on RDS:5432 (SG-to-SG rule)
ROOT_SIZE=30

declare -A DB_MAP=( [vn]=banhmi [my]=laksa [id]=rendang )

CC="${1:-}"; shift || true
[[ -n "${DB_MAP[$CC]+x}" ]] || { echo "usage: $0 {vn|my|id} [stage-flag ...]" >&2; exit 1; }
STAGES=("$@")
[[ ${#STAGES[@]} -gt 0 ]] || STAGES=(-embed-all)
# Chain each stage as its own /pipeline run; && so a failure stops the rest.
RUN_CMD=""
for s in "${STAGES[@]}"; do
  [[ -n "$RUN_CMD" ]] && RUN_CMD="${RUN_CMD} && "
  RUN_CMD="${RUN_CMD}/pipeline ${s}"
done
DBNAME="${DB_MAP[$CC]}"
LOG_GROUP="/banhmi/pipeline-${CC}"
ECR_IMAGE="${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com/banhmi-pipeline:latest"

AMI=$(aws ssm get-parameter \
  --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 \
  --region "$REGION" --query 'Parameter.Value' --output text)

echo "jurisdiction : ${CC} -> db ${DBNAME}"
echo "stages       : ${RUN_CMD}"
echo "instance     : ${INSTANCE_TYPE} in ${SUBNET} (private path to RDS)"
echo "logs         : CloudWatch ${LOG_GROUP}"

USERDATA=$(cat <<USERDATA_EOF
#!/usr/bin/env bash
# Watchdog: terminate after 4h no matter what, so a wedged run cannot bill forever.
shutdown -h +240
trap 'shutdown -h now' EXIT
set -euo pipefail

dnf install -y docker
systemctl start docker

DB_PASSWORD=\$(aws ssm get-parameter --name /banhmi/db-password \
  --with-decryption --region ${REGION} --query 'Parameter.Value' --output text)
DB_HOST=\$(aws ssm get-parameter --name /banhmi/db-host \
  --region ${REGION} --query 'Parameter.Value' --output text)
KAGGLE_TOKEN=\$(aws ssm get-parameter --name /banhmi/kaggle-token \
  --with-decryption --region ${REGION} --query 'Parameter.Value' --output text)

aws ecr get-login-password --region ${REGION} \
  | docker login --username AWS --password-stdin ${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com
docker pull ${ECR_IMAGE}

# Compute-only: no Xvfb, no Chrome, no GCP/Document AI, no S3 file cache —
# none of the crawl or OCR machinery is reachable from these stages.
timeout 14400 docker run --rm \
  --log-driver=awslogs \
  --log-opt awslogs-region=${REGION} \
  --log-opt awslogs-group=${LOG_GROUP} \
  --log-opt awslogs-create-group=true \
  -e BANHMI_JURISDICTION=${CC} \
  -e BANHMI_DATABASE_HOST="\${DB_HOST}" \
  -e BANHMI_DATABASE_PORT=5432 \
  -e BANHMI_DATABASE_USER=banhmi \
  -e BANHMI_DATABASE_NAME=${DBNAME} \
  -e BANHMI_DATABASE_SSLMODE=require \
  -e BANHMI_DATABASE_PASSWORD="\${DB_PASSWORD}" \
  -e BANHMI_EMBED_ENGINE=kaggle \
  -e KAGGLE_API_TOKEN="\${KAGGLE_TOKEN}" \
  ${ECR_IMAGE} \
  sh -c '${RUN_CMD}' 

shutdown -h now
USERDATA_EOF
)

ID=$(aws ec2 run-instances \
  --region "$REGION" \
  --image-id "$AMI" \
  --instance-type "$INSTANCE_TYPE" \
  --subnet-id "$SUBNET" \
  --security-group-ids "$SECURITY_GROUP" \
  --iam-instance-profile "Name=${INSTANCE_PROFILE}" \
  --instance-initiated-shutdown-behavior terminate \
  --block-device-mappings "DeviceName=/dev/xvda,Ebs={VolumeSize=${ROOT_SIZE},VolumeType=gp3,DeleteOnTermination=true}" \
  --metadata-options "HttpTokens=required,HttpEndpoint=enabled" \
  --user-data "$USERDATA" \
  --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=banhmi-embed-${CC}}]" \
  --query 'Instances[0].InstanceId' --output text)

echo "instance     : ${ID} (self-terminates on completion)"
echo
echo "follow logs:  aws logs tail ${LOG_GROUP} --follow --region ${REGION}"
echo "kill early:   aws ec2 terminate-instances --instance-ids ${ID} --region ${REGION}"
