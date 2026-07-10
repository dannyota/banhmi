#!/usr/bin/env bash
set -euo pipefail

# Launch a self-terminating EC2 pipeline instance for one country.
# The instance boots AL2023, pulls the pipeline image from ECR, runs
# cmd/pipeline -run-all, streams logs to CloudWatch, then terminates.
#
# Usage:
#   bash deploy/aws/run-pipeline.sh {vn|my|id} [-limit N]
#
# Requires: aws CLI with credentials that can ec2:RunInstances + iam:PassRole
# for the banhmi-pipeline-ec2 instance profile, plus ssm:GetParameter for the
# AL2023 AMI lookup.

# ── Per-country config ──────────────────────────────────────────────────────
declare -A REGION_MAP=( [vn]=ap-southeast-1   [my]=ap-southeast-5   [id]=ap-southeast-3 )
declare -A BUCKET_MAP=( [vn]=danny-banhmi-data-vn [my]=danny-banhmi-data-my [id]=danny-banhmi-data-id )
declare -A DB_MAP=(     [vn]=banhmi_q3        [my]=laksa_q3         [id]=rendang_q3 )
declare -A VOL_MAP=(    [vn]=gp2              [my]=gp3              [id]=gp3 )
# VN uses the Hanoi Local Zone fixed subnet; MY/ID discover a default-VPC subnet.
VN_SUBNET="subnet-02eb0b494c042f84a"
INSTANCE_TYPE="m7i.large"
INSTANCE_PROFILE="banhmi-pipeline-ec2"
SSM_REGION="ap-southeast-1"
ROOT_SIZE=40
# ────────────────────────────────────────────────────────────────────────────

usage() {
  echo "Usage: $0 {vn|my|id} [-limit N]" >&2
  exit 1
}

CC="${1:-}"
shift || true
[[ -n "$CC" ]] || usage
[[ -n "${REGION_MAP[$CC]+x}" ]] || usage

LIMIT=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -limit) LIMIT="$2"; shift 2 ;;
    *)      usage ;;
  esac
done

REGION="${REGION_MAP[$CC]}"
BUCKET="${BUCKET_MAP[$CC]}"
DBNAME="${DB_MAP[$CC]}"
VOLTYPE="${VOL_MAP[$CC]}"

# Resolve AWS account ID.
ACCOUNT=$(aws sts get-caller-identity --query Account --output text)

# Resolve the latest AL2023 x86_64 AMI in the target region.
AMI=$(aws ssm get-parameter \
  --name /aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64 \
  --region "$REGION" --query 'Parameter.Value' --output text)
echo "AMI: ${AMI} (${REGION})"

# Resolve subnet: VN uses the fixed LZ subnet; MY/ID pick a default-VPC subnet.
if [[ "$CC" == "vn" ]]; then
  SUBNET="$VN_SUBNET"
else
  SUBNET=$(aws ec2 describe-subnets \
    --region "$REGION" \
    --filters "Name=default-for-az,Values=true" \
    --query 'Subnets[0].SubnetId' --output text)
  if [[ -z "$SUBNET" || "$SUBNET" == "None" ]]; then
    echo "ERROR: no default-VPC subnet found in ${REGION}" >&2
    exit 1
  fi
fi
echo "Subnet: ${SUBNET}"

# Build the pipeline -run-all command, with optional -limit.
PIPELINE_CMD="/pipeline -run-all"
[[ -n "$LIMIT" ]] && PIPELINE_CMD="${PIPELINE_CMD} -limit ${LIMIT}"

ECR_IMAGE="${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com/banhmi-pipeline:latest"
LOG_GROUP="/banhmi/pipeline-${CC}"

# ── User-data script ────────────────────────────────────────────────────────
USERDATA=$(cat <<USERDATA_EOF
#!/usr/bin/env bash
# Fail-safe watchdog: terminate after 12 hours no matter what.
shutdown -h +720

# On any exit, ship the boot log to S3 for post-mortem (instance is about to
# vanish), then terminate. No secrets in the log: xtrace stays off.
trap 'aws s3 cp /var/log/cloud-init-output.log s3://${BUCKET}/debug/userdata-\$(date +%s).log --region ${SSM_REGION} || true; shutdown -h now' EXIT
set -euo pipefail

# ── Install docker ──────────────────────────────────────────────────────
echo "== installing docker =="
dnf install -y docker
systemctl start docker

# ── Read secrets from SSM (always from ap-southeast-1) ──────────────────
DB_PASSWORD=\$(aws ssm get-parameter --name /banhmi/db-password \
  --with-decryption --region ${SSM_REGION} --query 'Parameter.Value' --output text)
DB_HOST=\$(aws ssm get-parameter --name /banhmi/db-host \
  --region ${SSM_REGION} --query 'Parameter.Value' --output text)
KAGGLE_TOKEN=\$(aws ssm get-parameter --name /banhmi/kaggle-token \
  --with-decryption --region ${SSM_REGION} --query 'Parameter.Value' --output text)
DOCAI_PROC=\$(aws ssm get-parameter --name /banhmi/docai-processor \
  --region ${SSM_REGION} --query 'Parameter.Value' --output text)

# GCP SA key for Document AI + its GCS cache.
aws ssm get-parameter --name /banhmi/gcp-sa-key \
  --with-decryption --region ${SSM_REGION} --query 'Parameter.Value' --output text \
  > /root/gcp-sa.json
chmod 600 /root/gcp-sa.json

# ── ECR login + pull ────────────────────────────────────────────────────
echo "== pulling ${ECR_IMAGE} =="
aws ecr get-login-password --region ${REGION} \
  | docker login --username AWS --password-stdin ${ACCOUNT}.dkr.ecr.${REGION}.amazonaws.com
docker pull ${ECR_IMAGE}
echo "== starting pipeline (${CC} -> ${DBNAME}) =="

# ── Run pipeline ────────────────────────────────────────────────────────
# Xvfb for headed Chrome (WAF cookie minting). Start before the pipeline.
timeout 39600 docker run --rm \
  --log-driver=awslogs \
  --log-opt awslogs-region=${REGION} \
  --log-opt awslogs-group=${LOG_GROUP} \
  --log-opt awslogs-create-group=true \
  -v /root/gcp-sa.json:/gcp-sa.json:ro \
  -e GOOGLE_APPLICATION_CREDENTIALS=/gcp-sa.json \
  -e BANHMI_JURISDICTION=${CC} \
  -e BANHMI_DATABASE_HOST="\${DB_HOST}" \
  -e BANHMI_DATABASE_PORT=5432 \
  -e BANHMI_DATABASE_USER=banhmi \
  -e BANHMI_DATABASE_NAME=${DBNAME} \
  -e BANHMI_DATABASE_SSLMODE=require \
  -e BANHMI_DATABASE_PASSWORD="\${DB_PASSWORD}" \
  -e BANHMI_S3_DATA_BUCKET=${BUCKET} \
  -e BANHMI_OCR_ENGINE=documentai \
  -e BANHMI_DOCAI_PROCESSOR="\${DOCAI_PROC}" \
  -e BANHMI_DOCAI_BUCKET=danny-banhmi-docai \
  -e BANHMI_EMBED_ENGINE=kaggle \
  -e KAGGLE_API_TOKEN="\${KAGGLE_TOKEN}" \
  ${ECR_IMAGE} \
  sh -c 'Xvfb :99 -screen 0 1280x1024x24 -nolisten tcp & sleep 1 && DISPLAY=:99 ${PIPELINE_CMD}'

shutdown -h now
USERDATA_EOF
)

# ── Launch ──────────────────────────────────────────────────────────────────
INSTANCE_ID=$(aws ec2 run-instances \
  --region "$REGION" \
  --image-id "$AMI" \
  --instance-type "$INSTANCE_TYPE" \
  --subnet-id "$SUBNET" \
  --associate-public-ip-address \
  --iam-instance-profile "Name=${INSTANCE_PROFILE}" \
  --instance-initiated-shutdown-behavior terminate \
  --block-device-mappings "DeviceName=/dev/xvda,Ebs={VolumeSize=${ROOT_SIZE},VolumeType=${VOLTYPE},DeleteOnTermination=true}" \
  --tag-specifications "ResourceType=instance,Tags=[{Key=Name,Value=banhmi-pipeline-${CC}}]" \
  --user-data "$(echo "$USERDATA" | base64 -w0)" \
  --query 'Instances[0].InstanceId' --output text)

echo "Instance: ${INSTANCE_ID} (${REGION}, ${INSTANCE_TYPE})"
echo ""
echo "Tail logs:"
echo "  aws logs tail ${LOG_GROUP} --follow --region ${REGION}"
