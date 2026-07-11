#!/usr/bin/env bash
set -euo pipefail

# Create both CloudFront distributions (VN, MY) from the template.
# Edit the variables below before running.

# ── Variables (edit these) ───────────────────────────────────────────────────
ACM_CERT_ARN="arn:aws:acm:us-east-1:YOUR_ACCOUNT_ID:certificate/YOUR_CERT_ID"
ORIGIN_VERIFY_SECRET="YOUR_SECRET_HERE"
TEMPLATE="$(dirname "$0")/cloudfront-config.json"
# ─────────────────────────────────────────────────────────────────────────────

declare -A DISTRIBUTIONS=(
  ["banhmi.danny.vn"]=8081
  ["laksa.danny.vn"]=8082
)

for DOMAIN in "${!DISTRIBUTIONS[@]}"; do
  PORT="${DISTRIBUTIONS[$DOMAIN]}"
  CALLER_REF="${DOMAIN}-$(date +%Y%m%d%H%M%S)"

  echo "Creating distribution: ${DOMAIN} -> origin.danny.vn:${PORT}"

  CONFIG=$(cat "$TEMPLATE" \
    | sed "s|\"DOMAIN\"|\"${DOMAIN}\"|g" \
    | sed "s|DOMAIN|${DOMAIN}|g" \
    | sed "s|\"ORIGIN_PORT\"|${PORT}|g" \
    | sed "s|ORIGIN_VERIFY_SECRET|${ORIGIN_VERIFY_SECRET}|g" \
    | sed "s|ACM_CERT_ARN|${ACM_CERT_ARN}|g" \
    | sed "s|DOMAIN-YYYYMMDD|${CALLER_REF}|g")

  # Remove _comment fields (not valid in CloudFront API input)
  CONFIG=$(echo "$CONFIG" | python3 -c "
import json, sys
d = json.load(sys.stdin)
d.pop('_comment', None)
json.dump(d, sys.stdout, indent=2)
")

  RESULT=$(aws cloudfront create-distribution \
    --distribution-config "$CONFIG" \
    --query 'Distribution.{Id:Id,Domain:DomainName,Status:Status}' \
    --output table)

  echo "$RESULT"
  echo ""
done

echo "Done. Next steps:"
echo "  1. Wait for all distributions to reach 'Deployed' status"
echo "  2. Create CNAME records: each domain -> its CloudFront distribution domain"
echo "  3. Verify with: curl -I https://<domain>/healthz"
