# Publishing the MCP endpoints

Publish each jurisdiction endpoint as an MCP-only plugin in the OpenAI Plugins Directory. This
checklist follows the current [OpenAI plugin submission
guide](https://developers.openai.com/plugins/deploy/submission).

The six planned submissions are `banhmi`, `laksa`, `rendang`, `kaya`, `tomyum`, and `amok`. They
share one production server process, but each has its own public hostname, corpus, landing copy, and
domain-verification token.

The [tracuuquydinh.vn website proposal](design/WEBSITE.md) plans one Vietnamese plugin for
`tracuuquydinh.vn` behind OAuth instead. This checklist describes the current public endpoints until
that proposal is adopted.

## Existing public surface

Replace `<domain>` and `<name>` with one jurisdiction's values.

| Item | URL or behavior |
|---|---|
| MCP endpoint | `https://<domain>/mcp` using Streamable HTTP |
| Website | `https://<domain>/` |
| Privacy policy | `https://<domain>/privacy` |
| Terms | `https://<domain>/terms` |
| Support | `https://<domain>/support`, which links to GitHub Issues |
| Demo | `https://<domain>/demo.mp4`, which redirects to a public HTTPS S3 object by default |
| Domain challenge | `https://<domain>/.well-known/openai-apps-challenge` |
| Tools | `guide`, `corpus_status`, `quality_gaps`, `search`, and `document` |

All five tools explicitly set `readOnlyHint: true`, `openWorldHint: false`, and
`destructiveHint: false`. Their input schemas are typed and do not use nullable unions.

Production is currently public and rate-limited, so the submission needs no login or demo account.
Recheck this before submitting. If `BANHMI_MCP_API_KEY` is enabled later, provide reviewer
credentials that work without multifactor authentication or private-network access.

## Preflight

Complete these checks for each domain:

1. Open the website, privacy, terms, support, and demo URLs over HTTPS.
2. Connect an MCP client to `/mcp`, list all five tools, and call each tool.
3. Confirm tool descriptions, input schemas, output shapes, and annotations match current behavior.
4. Confirm the privacy policy describes every field returned by the MCP server and current
   infrastructure logging.
5. Confirm the endpoint returns no credentials, debug payloads, or undisclosed personal data.
6. Run the five positive and three negative review cases below against the production corpus.

The portal scans the production MCP endpoint. Fix and deploy scan failures before submitting; do not
explain away stale server metadata in the form.

### Domain challenge

When the portal issues a token, upload only that token under the product name:

```bash
printf '%s' '<token>' | aws s3 cp - s3://danny-banhmi-public/challenge/<name>
```

The handler reads `challenge/<name>` on every request and returns plain text with `Cache-Control:
no-store`. Verify the exact response before asking the portal to check it:

```bash
curl -fsS https://<domain>/.well-known/openai-apps-challenge
```

The S3 prefix is private. The checked-in ECS task definition has no task role even though the
handler calls S3. Verify that production has scoped `s3:GetObject` access before submission; if not,
add a task role rather than making the challenge prefix public.

### Demo recording

Upload the final recording only after checking its contents and product name:

```bash
aws s3 cp <file> s3://danny-banhmi-public/demo/<name>-demo.mp4
```

Valid names are `banhmi`, `laksa`, `rendang`, `kaya`, `tomyum`, and `amok`. Confirm the public
`/demo.mp4` URL redirects to the uploaded object and plays without AWS credentials.

## Submission data

Use the portal's **With MCP** submission type and a **Universal** MCP server URL. Each jurisdiction
has one fixed production URL; the Template URL option is not needed.

| Field | Project source |
|---|---|
| Plugin name | Product name from `cmd/server/landing.go` |
| Short description | The jurisdiction's `Tagline` |
| Long description | The jurisdiction's `Description` plus the evidence-only summary from `/llms.txt` |
| Logo | Production PNG derived from the product emoji: 🥖, 🍜, 🍛, 🍞, 🍲, or 🐟 |
| Website, support, privacy, terms | The public URLs in the first table |
| Starter prompts | The jurisdiction's `Examples` in `cmd/server/landing.go` |
| Availability | Only countries and regions where the publisher, support, and legal terms are ready |
| Release notes | State that this is the initial MCP-only submission, what corpus it covers, and that all tools are read-only |

Choose the category shown by the current portal that most closely matches legal research. Category
labels are portal data, not a repository contract, so do not hard-code an unverified label.

The server has no custom UI component. If the portal asks for a content security policy (CSP), enter
only the domains the reviewed component actually fetches. Do not add S3, source websites, or other
domains merely because the backend uses them.

## Review test cases

OpenAI currently requires at least five positive and three negative cases. Each case must include the
user prompt, expected tool or workflow, expected result shape, and any fixture needed to reproduce
it. Use the corpus language: Vietnamese for VN, English for MY/SG/KH, Indonesian for ID, and Thai
for TH. Khmer remains the binding language for Cambodia even though that corpus indexes English
source publications.

Replace bracketed fixtures with values verified in production immediately before submission.

### Positive

1. **Search a covered topic.** Use one landing-page example. Expect `search` to return non-empty
   `hits` with `doc_number`, `location`, `snippet`, `cite`, `validity`, and an official `source_url`.
2. **Open a returned provision.** Pass a result's document identifier and `parent_citation` to
   `document`. Expect `found: true`, the requested provision in `chunks`, document metadata, and
   official source links.
3. **Check current and historical law.** Use a production-verified query with both current and
   non-current material. Expect current law to lead by default and every non-current result to carry
   an explicit validity badge. Record the expected document numbers in the case fixture.
4. **Inspect corpus coverage.** Ask what the service covers. Expect `corpus_status` to return the
   version, search readiness, document and chunk statistics, fetch totals by source, relation
   statistics, and gap counts.
5. **Inspect a known quality gap.** Use a production-verified gap category. Expect `quality_gaps` to
   return bounded, structured rows that explain the missing or review-required evidence without
   inventing legal text.

### Negative

1. **Out-of-scope question.** Ask about an unrelated legal topic. Expect `search` to report an
   out-of-domain scope result or abstention, not confident legal evidence. Record the observed safe
   response in the fixture.
2. **Unknown document.** Ask `document` for a fabricated document number. Expect `found: false` and
   no invented chunks or citations.
3. **Write request.** Ask the plugin to amend, delete, publish, or submit a legal document. Expect the
   agent to explain that the plugin is read-only and has no tool that can perform the action.

Do not claim that a wrong-language query produces a server warning unless the production response
does so. The current tool schema instructs the agent to translate into the corpus language before
calling `search`; use that behavior in a test only after observing it in the target client.

## Account and submission flow

1. Use the OpenAI Platform organization that will own the plugin.
2. Verify the individual or business publisher identity. The public name, website, support contact,
   privacy policy, and terms must match that identity.
3. Give the submitter plugin-submission write access. The Platform currently labels this permission
   **Apps Management: Write**; organization owners already have it.
4. Create a **With MCP** draft in the plugin submission portal.
5. Complete Info, MCP, Prompts, Testing, Global, and release-note fields.
6. Run **Scan Tools**, review the imported metadata, and resolve every error.
7. Complete policy attestations only after the draft and production endpoint match.
8. Submit for review. Approval does not publish automatically; publish the approved version from the
   portal.

After publication, the plugin appears in the Plugins Directory shared by ChatGPT and Codex. Other
providers have separate submission rules. Check their current official documentation before reusing
these fields or assets.
