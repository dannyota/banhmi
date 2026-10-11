# ASEAN Banking MCP redesign proposal

Build one banking-regulation MCP for ASEAN, starting with Vietnam. Keep the current banking,
finance, and related technology scope. Serve evidence for the user's model to reason over.

Status: high-level proposal, 2026-10-11. Product changes require a separate implementation plan.
The redesign uses v0.4 as a planning label. Existing v0.4 tags require a distinct release number
before implementation. **ASEAN Banking MCP** and repository name **asean-banking-mcp** are working
names. The owner will select the new domain. The [tracuuquydinh.vn website proposal](WEBSITE.md)
supersedes the country expansion, the working name, and the VNGCloud transition: all services stay on
the current AWS stack until the owner decides otherwise. It keeps one-domain access and subject
organization.

The [corpus and workflow proposal](CORPUS-PIPELINE-REDESIGN.md) reviews schema revisions, bounded input
processing, Temporal coordination, read resource use, publication, and repository boundaries. It
defines the proposed technical sequence before subject filtering and hosting cutover.

## Product and corpus

One hostname serves the public site, signed-in evidence workspace, `/mcp`, and OAuth routes. Vietnam
is the first redesigned corpus. Country and subject become explicit query dimensions after the
Google-access release. The existing country corpora remain available until an authorized
authentication cutover.

Keep one corpus database per country. A law can belong to several subjects without copying its
text or creating another service. Use document and provision assignments so a broad statute
does not make every provision relevant to banking.

Proposed starting subjects:

- Banking institutions, licensing, and governance.
- Prudential requirements, supervision, and reporting.
- Lending, deposits, payments, and foreign exchange.
- Anti-money laundering and customer identification.
- Consumer protection and conduct.
- Data protection, cybersecurity, cloud, and electronic transactions affecting banking.
- Securities and other financial rules affecting banking activities.

Retain legacy material for review. Admit and publish redesigned coverage only when the document
or provision has recorded banking relevance. Treat unreviewed legacy material as a coverage gap.
Store the assignment method, evidence, confidence, and taxonomy version. Subject filters select
primary evidence; clearly marked amendment and validity context can cross subject boundaries.
Unknown assignments remain visible as coverage gaps.

Keep all five existing MCP tool names. Add compatible country and subject fields without hiding
source quality, missing coverage, or legal lifecycle status. Introduce durable evidence keys that
include the country and survive corpus rebuilds. Keep these keys independent of the brand.
Map each legacy document by country and old document ID because existing IDs are database-local.

## One domain and account control

The first release requires Google login for website evidence access and OAuth for hosted MCP access.
The first valid Google login creates the account. Use the canonical Google issuer and subject as the
identity. Require a verified email, retain only its current value for private operator lookup, and
never merge accounts by email. The [Google access design](AUTH.md) defines the routes, sessions,
preregistered public MCP clients, exact resource binding, token lifetimes, account state, and
fail-closed behavior.

The first release includes database block and unblock primitives through a private operator path. A
block increments the account authorization epoch and atomically revokes website and MCP credentials.
Every authenticated website and MCP request rechecks status and epoch. Unblocking restores no old
credential. The private admin website, configurable per-account quotas, usage displays, and temporary
suspension interface remain later work.

Retain IP, request, body, timeout, and concurrency limits. Record only the aggregate operational and
security events needed to run the service. Do not retain raw account query histories. Apply the same
authentication and origin checks at the service so a direct origin request cannot bypass the edge.

## Hosting and transition

VNGCloud becomes the serving target. Start with one measured serving deployment: reverse proxy,
Go service, shared query embedder, and PostgreSQL. Size the host from database and embedder memory
measurements. Run ingestion, extraction, and bulk embedding on separate disposable cloud workers.
Use encrypted persistent PostgreSQL storage and private encrypted object storage for source files
and backups. Test off-host recovery. The single-host design accepts downtime after host failure;
agree on recovery targets before cutover. Confirm provider capabilities before choosing exact
products and instance sizes.

Keep AWS while VNGCloud is prepared and verified. Maintain one authoritative writer for each
dataset. Deploy a validated corpus copy to VNGCloud before switching traffic. Keep authentication
state separate from replaceable corpus snapshots.

At the authentication cutover, close anonymous evidence access on old endpoints and show explicit
reconnection guidance. Do not assume MCP clients will follow a domain redirect or retain OAuth
grants across a domain change. The new domain is the canonical entry point.

Retain AWS for an agreed fallback period. A rollback must preserve the latest blocks, revocations,
and account state. Do not fail back automatically to stale identity data. Set the fallback expiry,
acceptable recovery time, and acceptable data loss before cutover.

Hosting the database in Vietnam does not by itself establish data residency. Map identity-provider
traffic, logs, backups, and external processing before making a residency claim.

## GitHub CI and releases

GitHub Actions is the target for builds, tests, integration checks, evaluations, and release
orchestration. Cloud workers are the target for corpus jobs.

The initial CI slice is complete. Read-only repository hygiene, the default Go build, `go vet`, and
`go test -short` passed for commit `add3886` in
[GitHub Actions run 38084176452](https://github.com/dannyota/banhmi/actions/runs/38084176452).
The Google-access source at `30b1e9f` also passed auth generation and migration checks, isolated
PostgreSQL integration, and synthetic browser checks in
[GitHub Actions run 38090442831](https://github.com/dannyota/banhmi/actions/runs/38090442831).
Corpus database integration, corpus generation and migration drift, live-source checks, evaluations,
ONNX-tagged builds, releases, and corpus refreshes remain future hosted work. Google access remains
unmerged and undeployed; real Google and named-client checks remain open.

Use three workflow families:

| Workflow | Responsibility |
|---|---|
| Pull request checks | Formatting, unit tests, generated-file drift, migration checks, integration and MCP contract checks, relevant country evaluations. |
| Release | Build immutable images, record provenance, scan artifacts, deploy a pinned release through a protected environment, run smoke checks. |
| Corpus refresh | Discover and process sources on cloud workers, evaluate a candidate corpus, publish a versioned snapshot, promote atomically. |

Keep an always-reported merge gate. Detect changed paths within jobs so skipped workflows cannot
leave required checks pending. Untrusted pull requests receive no production credentials.
Separate release privileges from refresh privileges. Use AWS OpenID Connect (OIDC) federation and
short-lived access where supported; limit VNGCloud credentials to the protected deployment environment. Prove credential
delivery without plaintext logs or artifacts before accepting the deployment path.

Reuse a workflow when an entire job repeats. Use a composite action for repeated setup steps.
Pin third-party actions, cancel superseded runs, and keep path selection inside jobs so required
checks always report. Extract shared workflows only when the common contract is clear. Enforce
required checks in GitHub repository rules as well as documenting them.

Promote a release bundle that pins the image digest, schema version, corpus snapshot, source-file
manifest, subject configuration, model and tokenizer, and evaluation results. Keep personal data
and credentials out of workflow artifacts. Restore and rollback checks must validate the bundle,
not only whether a container starts.

## Repository and agent work

Keep one Go module and shared evidence logic. Separate serving, corpus processing, identity,
administration, and infrastructure by package responsibility. Do not create a service per subject.
Keep cloud-specific deployment configuration outside the corpus and retrieval code.

Give each fact one documentation home: product scope, architecture, decisions, operations, or the
current delivery plan. Replace conflicting development instructions as part of adopting the new CI
process. Rename the repository and public branding after selecting the name; migrate configuration
names with compatibility rules. A brand rename never changes durable evidence keys.

Use bounded work assignments with explicit file ownership. Keep design, implementation, and
review ownership separate. Sol handles design and implementation at medium effort; an independent
Sol reviewer uses xhigh effort for plans, changes, and fix confirmation. The primary agent owns
coordination, verification, and Git operations. CI provides the execution evidence for reviews.

## Delivery order

1. Keep the implemented Google-only same-domain access slice separate from the corpus redesign.
   Hosted checks cover its landing page, signed-in Search and Document workspace, preregistered-client
   MCP OAuth, and private block primitives. Merge and deployment remain open.
2. Select the canonical domain and release number, configure Google and approved MCP clients, settle
   privacy and recovery policy, and verify real website and MCP flows before a separate cutover
   decision. Keep the current AWS deployment unchanged until then.
3. Follow the corpus and workflow proposal: add hosted corpus baselines, bounded processing, durable
   evidence keys, immutable revisions, and validated publication. Pilot Temporal on an isolated
   Vietnam build. Add subject metadata and validate the existing evidence and retrieval baselines
   before exposing subject filters.
4. Add the private admin website, configurable account quotas, usage views, and any later identity
   policy approved from measured operation.
5. Prepare VNGCloud and rehearse authentication recovery, backup restoration, corpus promotion, and
   rollback. Preserve one authoritative identity writer.
6. Move the canonical domain to VNGCloud, retain AWS for the agreed period, then retire AWS resources
   only after a separate retirement decision. Settle the product rename separately.

## Design basis

- [Current architecture](../ARCHITECTURE.md), [Google access design](AUTH.md), [OAuth storage
  design](OAUTH-STORAGE.md), and [schema design](SCHEMA.md) describe the current service and proposed
  changes. The access design supersedes the earlier multi-provider and separate-authentication-domain
  proposal.
- The [repository cleanup plan](../plans/repository-cleanup.md) defines the current contributor
  documentation and initial GitHub CI slice.
- [GitHub reusable workflows](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows)
  define the mechanism for sharing whole jobs.
- [VNGCloud vStorage S3 SDK documentation](https://docs.vngcloud.vn/vng-cloud-document/vstorage/object-storage/object-storage-hcm04/3rd-party-softwares/s3-sdk)
  provides the starting point for verifying object-storage integration.
