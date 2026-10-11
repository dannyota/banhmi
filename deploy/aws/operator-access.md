# Banhmi AWS operator access

`banhmi-operator` uses AWS managed `AdministratorAccess` through the AWS CLI and AWS MCP.
The [CloudFormation template](operator-access.json) manages stack `banhmi-operator-access`
in `ap-southeast-1`. It creates no console password or access key.
The user has no permissions boundary or expiry.

## Scope and guardrails

IAM no longer limits the operator to project resources or selected actions.
Repository rules are the guardrails:

- Never read secret values. Do not call `secretsmanager:GetSecretValue` or
  `secretsmanager:BatchGetSecretValue`, decrypt SSM SecureStrings, or use KMS to decrypt secrets.
- Never delete or replace a resource without explicit owner confirmation for that exact target.
- Prefer infrastructure as code. Review change sets before applying infrastructure changes.
- Never touch resources of other projects in the same account.
- Never create or expose access keys during routine operations. Key rotation requires owner
  authorization and the secure installation procedure below. Never print key values.
- Never use the account root user for routine work.

## Credentials

The existing key lives in an owner-only env file outside the repository.
`AWS_OPERATOR_CREDENTIALS_FILE` names that file. Keep the file at mode `0600`.
An AWS CLI profile uses a `credential_process` helper outside the repository to read the file
at runtime. `AWS_OPERATOR_PROFILE` names that profile. The project's AWS MCP server uses
that profile too. The helper emits credentials only to the consuming process.
Do not run the credential helper directly or capture its output in logs or conversation.
Keep credential values out of repository files, command arguments, and tool output.

## Update the stack

Use an authorized administrator profile, named by `AWS_ADMIN_PROFILE`.
Run these commands from the repository root after independent review:

```sh
aws cloudformation validate-template --profile "$AWS_ADMIN_PROFILE" \
  --region ap-southeast-1 --template-body file://deploy/aws/operator-access.json
aws cloudformation create-change-set --profile "$AWS_ADMIN_PROFILE" \
  --region ap-southeast-1 --stack-name banhmi-operator-access \
  --change-set-name "$AWS_OPERATOR_CHANGE_SET" --change-set-type UPDATE \
  --template-body file://deploy/aws/operator-access.json \
  --parameters ParameterKey=UserName,UsePreviousValue=true --capabilities CAPABILITY_NAMED_IAM
aws cloudformation wait change-set-create-complete --profile "$AWS_ADMIN_PROFILE" \
  --region ap-southeast-1 --stack-name banhmi-operator-access \
  --change-set-name "$AWS_OPERATOR_CHANGE_SET"
aws cloudformation describe-change-set --profile "$AWS_ADMIN_PROFILE" \
  --region ap-southeast-1 --stack-name banhmi-operator-access \
  --change-set-name "$AWS_OPERATOR_CHANGE_SET"
```

Set `AWS_OPERATOR_CHANGE_SET` to a unique change set name before running the commands.
Review for `Modify` on `OperatorUser` with `Replacement: False` and `Remove` on `OperatorPolicy`.
Stop if the change set adds or replaces a user or changes any other resource.
Keep `UserName` at its existing value, `banhmi-operator`. The logical ID stays `OperatorUser`.
The update preserves the user and existing access key. Removing the policy requires owner
confirmation for `OperatorPolicy` in this stack before execution.

```sh
aws cloudformation execute-change-set --profile "$AWS_ADMIN_PROFILE" \
  --region ap-southeast-1 --stack-name banhmi-operator-access \
  --change-set-name "$AWS_OPERATOR_CHANGE_SET"
aws cloudformation wait stack-update-complete --profile "$AWS_ADMIN_PROFILE" \
  --region ap-southeast-1 --stack-name banhmi-operator-access
```

Record the `OperatorPolicy` ARN before execution. A removed resource can fail to delete while the
stack still reaches `UPDATE_COMPLETE`, so after the update require a `DELETE_COMPLETE` stack event
for `OperatorPolicy`, confirm `iam:GetPolicy` on the recorded ARN returns `NoSuchEntity`, and
confirm `iam:GetUser` shows no `PermissionsBoundary`.

Verify as the operator through the CLI profile and the project's AWS MCP server.
Check `sts:GetCallerIdentity`, then one non-secret read from each service:
`ecs:DescribeServices` for a Banhmi service, `rds:DescribeDBInstances` for the Banhmi database,
`s3:GetBucketLocation` for a Banhmi bucket, and `cloudfront:ListTagsForResource` for a Banhmi
distribution. Run `iam:ListAttachedUserPolicies` and `iam:ListUserPolicies` for `banhmi-operator`.
Confirm `AdministratorAccess` is attached. Return selected non-secret fields only.
Caller identity alone does not prove resource access.

## Run operations with the repository helper

[aws-mcp-operator.py](../../tools/aws-mcp-operator.py) signs requests to the managed AWS MCP
endpoint. It reads the env file directly and does not change an already-connected MCP identity.
Write a script using AWS MCP's `call_boto3` API. Return selected non-secret fields only.
Finish with `result = {...}` and `result` as the final expression. Run:

```sh
python3 tools/aws-mcp-operator.py \
  --credentials-file "$AWS_OPERATOR_CREDENTIALS_FILE" --script operation.py
```

Never return secret values, tokens, raw service configuration, or private object contents.
Output redaction is a fallback, not permission to retrieve secrets.

## Key rotation and retirement

Rotate the key at least every 90 days. The owner must authorize the new key and confirm the
exact old key before disabling and deleting it. Use an authorized bootstrap env file without
reading its contents into conversation. Install the new key into a new destination:

```sh
python3 tools/aws-mcp-operator.py \
  --credentials-file "$AWS_BOOTSTRAP_CREDENTIALS_FILE" \
  --install-key-for banhmi-operator \
  --write-credentials "$AWS_OPERATOR_NEW_CREDENTIALS_FILE"
```

The destination must be outside the repository and must not exist. Its parent must be owned
by the current user with mode `0700`. The destination path must not contain symlinks.
The helper creates the final parent if absent and writes the file with mode `0600`.
It never overwrites an existing file or retries key creation automatically.
If creation has an uncertain outcome, inspect IAM key metadata through the bootstrap identity.
Do not delete the empty sentinel and create another key blindly.

Switch the env file used by the CLI credential helper and AWS MCP to the new key.
Refresh consuming sessions and verify identity and service reads. Then disable and delete the
old key through an authorized identity. Keep operator credentials out of GitHub; use OIDC for CI.
To retire access, confirm the exact key and stack or policy attachment with the owner.
Disable and delete the key, then delete the stack or detach `AdministratorAccess` from the user.
