# Security and external systems

## Secrets and private data

- Never read secret values into conversation or tool output.
- Never commit credentials, tokens, API keys, private keys, secret values, internal hostnames, private
  account identifiers, or real document payloads.
- Resolve secrets at runtime through the configured secret provider or an approved ignored environment
  file. Keep secret values out of command arguments, logs, fixtures, examples, and generated files.
- Do not retrieve AWS Secrets Manager values. Use runtime references or the project's approved wrapper
  so plaintext does not enter the agent context.
- Never read, move, or track credential files in tool directories such as `.claude/`.
- User questions never go to a third-party processor. Public legal source documents may use approved
  cloud batch processing for OCR and bulk embedding.
- Keep local samples, dumps, benchmark artifacts, and OCR caches out of Git.
- Do not send repository source or corpus payloads to external services outside the authorized build,
  deployment, source, OCR, embedding, and collaboration workflows.

## External changes

- Keep deployment, production data mutation, releases, and cloud changes within the user's authorized
  scope. Existing authorization covers the normal steps needed to complete that work.
- Prefer infrastructure as code with reviewed change sets for cloud infrastructure changes.
- Never delete or replace a resource without explicit owner confirmation for that exact target.
  Use recoverable operations and verify the effect.
- Never touch other projects' resources in the shared account.
- Never test with production database credentials or connect pull request checks to production.
- For AWS work, prefer the AWS MCP Server when available. Read the applicable repository skills and
  current AWS documentation before acting. Use the
  [operator runbook](../deploy/aws/operator-access.md) for banhmi operations.
- Use the project operator identity on AWS and a non-root identity on VNGCloud, never an account
  root user. The AWS project operator has full `AdministratorAccess`; IAM no longer limits the
  operator, so repository rules are the guardrails: never read secret values, never delete or replace
  a resource without explicit owner confirmation for that exact target, never touch other projects'
  resources in the shared account, and prefer infrastructure as code with reviewed change sets.
- Resolve AWS secrets only at runtime through the approved mechanism. Never fetch Secrets Manager
  values into agent context, command output, logs, or files.
- Follow the current operator and deployment runbooks for authorized external work. Do not copy
  credentials or private machine setup into public documentation.
