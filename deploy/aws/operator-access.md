# Banhmi AWS operator access

`banhmi-operator` is a project-scoped IAM user for AWS MCP operations. The
[CloudFormation template](operator-access.json) creates the user and attaches the same managed
policy as its identity policy and permissions boundary. It creates no console password or key.

Provisioned on 2026-10-11 through AWS MCP in stack `banhmi-operator-access`, region
`ap-southeast-1`. Resource access expires at **2026-11-10 00:00 UTC**.
Verified the new identity through AWS MCP, allowed Banhmi ECS, RDS, and S3 reads, and denial of
resources outside the project scope. CloudFormation validation, Access Analyzer, and permission
simulations passed.

## Scope

| Resource | Allowed operations |
|---|---|
| Banhmi MCP and embedder ECS services | Describe service state. |
| Banhmi RDS instance | Describe instance state. |
| Banhmi MCP and pipeline ECR repositories | Read image metadata and layers. |
| Six Banhmi country data buckets | List, read, and write objects. No deletion. |
| Six Banhmi CloudFront distributions | Read tags and invalidation state; create invalidations. |

Resource grants require AWS MCP. The policy denies resource operations at the configured UTC
expiry and over insecure transport. `sts:GetCallerIdentity` can still return identity after
expiry because AWS does not require permission for that operation.

The operator cannot deploy services, change the shared host or database, administer IAM, read
SSM parameters, change DNS, read CloudFront origin configuration, or access the public-assets
bucket. Shared infrastructure mutations require a separately reviewed policy change. The operator
cannot change its own policy or permissions boundary.

## Provision and credentials

Use AWS MCP to validate the template and policy, simulate allowed and denied operations, and
create a CloudFormation change set. Supply the expiry, user name, and six distribution IDs as
parameters. Review the change set before executing it.

The runtime helper [aws-mcp-operator.py](../../tools/aws-mcp-operator.py) signs requests to the
managed AWS MCP endpoint. The helper reads credentials inside its process and does not print
credential values. It does not change the identity of an already-connected MCP integration.

Create an access key only after the stack succeeds. Use an existing authorized bootstrap
credential file without reading its contents into the conversation:

```sh
python3 tools/aws-mcp-operator.py \
  --credentials-file "$AWS_BOOTSTRAP_CREDENTIALS_FILE" \
  --install-key-for banhmi-operator \
  --write-credentials "$AWS_OPERATOR_CREDENTIALS_FILE"
```

The destination must be new. Its parent must be owned by the current user with mode `0700` and
must not contain symlinks. The helper creates the final parent if absent and writes the file
with mode `0600`. It never overwrites an existing file or retries key creation automatically.
If creation has an uncertain outcome, inspect IAM key metadata through the bootstrap identity
before retrying. Do not delete the empty sentinel and create another key blindly.

## Run operations

Write a script using the AWS MCP `call_boto3` API. Return selected non-secret fields only.
Finish with `result = {...}` followed by `result` as the final expression. Run:

```sh
python3 tools/aws-mcp-operator.py \
  --credentials-file "$AWS_OPERATOR_CREDENTIALS_FILE" \
  --script operation.py
```

The helper's current default remains an owner-only file in the current user's configuration
directory. Set `AWS_OPERATOR_CREDENTIALS_FILE` to that file or another owner-only path and pass it
explicitly. Never return secret values, authorization tokens, raw service configuration, or private
object contents into tool output. The helper's output redaction is a fallback, not permission to
retrieve secrets.

Verify resource access as the operator after provisioning; caller identity alone does not prove
the permissions work. Expiry does not remove the user or key. An authorized bootstrap identity
must disable and delete the key when access is retired. Use a separate GitHub OIDC role for
future CI releases instead of copying the operator credential into GitHub.
