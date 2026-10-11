Proposal. Not approved, not implemented.

# Vietnam hosting for Tra cứu quy định

## Goal and limits

Run `tracuuquydinh.vn`, its website, `/mcp`, Google login, OAuth, Vietnam evidence, and one release-matched query runtime on VNGCloud in Vietnam.
Leave the five other country services on their existing AWS hostnames and deployment.
Keep the five tools: `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`.
Vietnam questions are Vietnamese with diacritics or a document number; the serving layer validates the question before any embedding or corpus query. English is interface text only.
Run no answer model, generative model, OCR, corpus worker, or bulk embedder here. No reranker anywhere; retrieval uses dense plus lexical fusion.

The owner's hosting decision supersedes the AWS-only transition wording in
[WEBSITE.md:139](../design/WEBSITE.md#L139) and [PLAN.md:12](../../PLAN.md#L12).
Other website and corpus decisions remain inputs; validity, relations, targets, keys, and subjects remain corpus work. The write path computes amendment differences in its relations and validity stage, the database stores them, and the website owns redline display.
Keep configuration and release names country-scoped without provisioning more countries.
The supplied operating pattern does not independently prove provider capability.

## Current state and evidence

Repository records describe earlier validation and deployment; no live service was probed. “Coded” means source exists.

| Fact and status | Repository evidence | Hosting consequence |
|---|---|---|
| Recorded AWS deployment uses one ARM64 ECS host, a loopback embedder, PostgreSQL 17 with pgvector, and six databases. | [PLAN.md:58](../../PLAN.md#L58), [ARCHITECTURE.md:184](../ARCHITECTURE.md#L184) | Build separate amd64 artifacts. Do not replace the shared AWS stack. |
| Recorded Vietnam corpus has 142,086 chunks and 3,974 documents. Evaluation baselines are separate from live counts. | [PLAN.md:89](../../PLAN.md#L89) | Use counts for estimates, then measure the delivered release. |
| Recorded Qwen3-Embedding-0.6B ONNX FP16 footprint is about 2.3 GB per process; an earlier burst measurement was about 23 MB per inference. | [PLAN.md:797](../../PLAN.md#L797), [PLAN.md:504](../../PLAN.md#L504) | Measure memory for one query runtime, including amd64 startup and steady-load peaks. |
| Authenticated composition opens a separate auth pool and explicitly selects Vietnam. | [internal/app/serve/hosted.go:65](../../internal/app/serve/hosted.go#L65) | Put auth in a separate database on the same PostgreSQL instance. |
| Auth generation, synthetic browser, and isolated database checks are recorded as passed; live Google and client checks remain open. | [docs/plans/google-access.md:155](../plans/google-access.md#L155), [PLAN.md:17](../../PLAN.md#L17) | Require live checks before cutover. Do not infer deployment from source. |
| Block increments the account epoch and revokes sessions and grant families transactionally. Unblock increments the epoch. | [sql/auth/schema.sql:195](../../sql/auth/schema.sql#L195) | Preserve account policy independently of corpus rollback and older auth backups. |
| HTTP source has host checks, IP and protocol limits, and a 1 MiB body cap. `/healthz` returns a constant success. | [cmd/server/middleware.go:18](../../cmd/server/middleware.go#L18), [cmd/server/hosted.go:16](../../cmd/server/hosted.go#L16) | Add dependency readiness. Liveness alone cannot open traffic. |
| Corpus release source has only static review. Hosted generation, durable adapters, activation, and corpus evaluation remain pending. | [docs/plans/corpus-redesign-source.md:460](../plans/corpus-redesign-source.md#L460) | Delivery below is a required interface, not an existing release service. |
| Serving currently supplies a legacy release resolver when no resolver is passed. | [internal/app/serve/app.go:62](../../internal/app/serve/app.go#L62), [internal/app/serve/hosted.go:88](../../internal/app/serve/hosted.go#L88) | A new endpoint must not pretend it has validated release activation until the sibling contract is implemented. |

## Chosen topology

Use one dedicated amd64 GreenNode vServer in HCM03-1A, Ho Chi Minh City, with Ubuntu 24.04, encrypted root, and a separate encrypted SSD data volume.
Confirm encryption on both volumes, snapshots, and replacement volumes before loading private data.
Reuse the existing VNGCloud deploy pattern: Podman containers managed by systemd, pinned images, and a versioned host bundle. Share the GreenNode account, billing, and CLI only; do not share the vServer or a VKS cluster with another project.

| Unit | Reachability | State and privileges |
|---|---|---|
| Caddy with Coraza, OWASP Core Rule Set, and CrowdSec remediation | Public TCP 80 and 443 | TLS state on encrypted storage; sole public application listener. |
| Go server | Loopback only, behind Caddy | Read-only corpus role, restricted auth runtime role, Google and OAuth secrets. |
| One query runtime | Loopback endpoint, Bearer authenticated | Read-only model assets; no database, cloud, or Google credentials. |
| PostgreSQL 17 with pinned pgvector | Unix socket only | Separate auth, durable Vietnam control, and release databases; no public or private TCP listener. |
| pgBackRest and maintenance units | Local socket and HCM04 bucket HTTPS | Backup credentials only in backup units; migrations use separate owner credentials. |
| CrowdSec local engine and host checks | Local only | Redacted local logs, local decisions, no central signal upload. |

Run application processes as distinct non-root users. Mount only each unit's required socket,
secret, and data paths. Drop capabilities, use read-only container roots, and limit writable temporary paths.
Only the edge receives the capability needed for privileged ports. Keep Podman control sockets private.
Mount the data volume before PostgreSQL, secrets, and application units start; a missing mount fails startup.
Put PostgreSQL data, WAL, logs, backup spool, model cache, and Podman secret storage on encrypted storage.
Use host networking for edge, Go, and embedder containers; explicitly bind Go and embedder to loopback.

Use the Unix socket with explicit database/user authentication and restrictive socket permissions.
The [corpus DSN builder:521](../../pkg/base/config/config.go#L521) accepts a configured host; prove socket operation in hosted CI.
Use the database contract: at most 16 corpus connections across releases, four auth and two control connections per server process; reserve eight operator connections per instance.
Pool tuning is required deployment work, not a claimed current configuration.

| Decision | Recommendation | Rejected option and reason |
|---|---|---|
| Service shape | Single host with systemd and Podman | Kubernetes or an initial multi-node cluster adds operations before measured need. |
| Database | Self-run PostgreSQL 17 and pgvector | Managed PostgreSQL remains unselected. Its required versions, extensions, and recovery features are unverified. |
| Edge | Local Caddy, Coraza, Core Rule Set, CrowdSec; no CDN | A foreign TLS proxy would process questions. GreenNode vWAF is an escalation after location and logging review. |
| Delivery | Immutable logical corpus release, staged before activation | Live cross-cloud SQL writes and bidirectional replication create writer and rollback ambiguity. |
| Recovery | Rebuild in Vietnam from backups | Automatic AWS failback would export new account data and risk stale authorization. |

Add release activation fencing, restore-safe account blocks, and model capacity checks to protect evidence, accounts, and model memory.
Local-only CrowdSec is a residency constraint on the chosen pattern, not a new security service.

## Workload and sizing

Owner decision 23 selects a dedicated small vServer and the quotes below, replacing shared hosting and the earlier large-host estimates. Size from measurement for PostgreSQL, one embedder, and the Go server; grow only from measurement.
Start from `a-general-4x8` (4 vCPU, 8 GiB), with `a1-standard-2x8` (2 vCPU, 8 GiB) as the cheaper fallback if measurement allows. Use a 30 GB SSD root and a 50 GB data SSD; original source files stay in object storage, not on the serving disk. Disk sizes are decimal GB.

The owner supplied these current AWS measurements; this design pass made no live checks:

| Measured baseline | Value and sizing limit |
|---|---|
| RDS `db.t4g.micro` | Six country corpora plus another service use about 11.3 GB of 20 GB; 1 GiB RAM and 3.5% average CPU. This is not a measurement of immutable Vietnam release copies. |
| Shared `t4g.medium` Vietnam serving | 2 vCPU, 4 GiB; reservations of 2.4 GB for the embedder and 300 MB for the MCP server. Reservations are not peak RSS measurements. |
| Vietnam S3 source bucket | 13.3 GB of source files. Keep source files in object storage; back up public legal originals and OCR outputs in AWS S3 under the [conversion decision](document-conversion.md#decisions). |

Acceptance workload: 2 requests per second for 30 minutes, bursts of 8, with concurrent PostgreSQL and application load. This is not a traffic forecast.
Measure the single runtime's CPU and memory reservation; use the embedding bake-off's 2 vCPU ceiling for comparison, not a fixed production allocation. The cheaper 2 vCPU host must also fit database, application, edge and maintenance load.
Start with two active and four queued inference requests for the single runtime. The coded per-process bounds are in [cmd/embedder/main.go:181](../../cmd/embedder/main.go#L181). Cap runtime threads separately from concurrency.
Size from `peak RSS of one runtime + PostgreSQL working set + app/edge/OS + maintenance allowance`, with at least 25% free headroom. Include startup and restore peaks; reserve no dual-runtime capacity.
Throttle backups and restores; index rebuilds during restore require a serving-load rehearsal or a fenced maintenance window. A failed capacity or latency check blocks launch or promotion until measurement supports an adjustment.

The coded dense column is `vector(1024)` with an HNSW index, and lexical data uses `sparsevec` ([sql/gold/schema.sql:20](../../sql/gold/schema.sql#L20)). At 142,086 vectors, `142086 × 1024 × 4` is about 0.58 GB of float values alone, excluding tuples, HNSW, text, lexical data, and revisions.
Measure each restored release, transfer archive, WAL/temp peak, auth, logs and metadata. Budget the active release plus at least two prior releases and a candidate; the 30-day window, pins and holds may require more copies.
Before each load, require free space for the candidate, transfer, measured peak WAL/temp work, and 25% of the whole data volume. Never delete retained evidence to make a release fit. Expand the 50 GB data SSD only when measurements require it.

Measure latency, CPU, queue rejection, RSS, SQL temporary bytes, and disk use on VNGCloud. Require p95 search below three seconds plus the [embedding latency gate, owner question 1](embedding-models.md#owner-questions), including its 500 ms short-query p95.
Require no out-of-memory failure, steady-load overload, unbounded queue, or auth starvation; report burst rejections with successful latency. Grow when measured latency fails, sustained CPU exceeds 70%, or free headroom falls below 25%; there is no automatic 8 vCPU launch tier.
Separate PostgreSQL onto a private Vietnam host only after measured I/O or recovery needs justify it. That later change requires TLS, new recovery proof, and an explicit owner decision.

## Infrastructure and deployment

Use an independent `deploy/vn/` OpenTofu root for network, subnet, security group, server, encrypted volumes, and floating IP.
Use `deploy/vn/host/` for cloud-init and host configuration, `edge/` for edge security, and `scripts/` for deployment, secrets, buckets, restore, and cutover.
Pin provider versions and hashes. Keep runtime secrets out of cloud-init, plans, outputs, and state.
Keep encrypted state and its backups in Vietnam; serialize owner applies with an explicit lock.
Use a tested backend lock if supported; otherwise use one owner-held apply lock and prohibit concurrent applies.
Backend compatibility and provider resource support are pre-apply checks, not assumed facts.

The owner deploys over SSH from Vietnam. A reviewed script must:

1. Verify the exact commit's required GitHub checks, image provenance, architecture, signatures,
   image digests, host-bundle hash, release manifest, and compatibility. Failure stops deployment.
2. Acquire the host deploy lock and persist a deploy fence on encrypted storage. Missing or unreadable fence state denies public traffic.
   Enforce the deploy fence in a host-firewall gate outside replaceable bundles; allow only owner SSH/probe source IPs. Drain in-flight requests.
3. Download immutable artifacts, enforce disk headroom, and verify an off-host pre-deploy backup.
4. Validate the staged host bundle, atomically switch its version pointer, and retain the last-known-good bundle. Keep the independent fence closed on any failure.
   Install secrets without tracing. Run auth migrations separately from corpus preparation; never restore auth to undo an application release.
5. Prepare the corpus and the single selected query runtime, with explicit dense-search availability for retained releases. An owner-authenticated probe checks dependencies while public admission remains closed.
   Check canonical HTTPS, Google, OAuth, and the five-tool contract through owner-allowlisted external probes.
6. Release the fence only on success, then check public `/readyz`. Failure reinstates the fence.
   Roll back only to an image compatible with current auth and the selected corpus/model bundle.

The fence survives a crash and reboot. Missing gate rules deny public ingress at boot; verify this independently of the host bundle.
Only verified success or explicit recovery clears it. Obtain TLS before deployment; the firewall fence temporarily pauses public HTTP-01 renewal.
Pre-download before fencing where possible. Host replacement accepts a maintenance window; reopening requires the selected runtime ready and retained-release availability reconciled.
Use additive auth migrations first. A destructive migration requires its own recovery plan.
Build amd64 images in GitHub CI. The existing server pins `GOARCH=arm64`, and the embedder pins ARM64 libraries
([server container:36](../../deploy/containerfiles/Containerfile.ecs.server#L36),
[embedder container:60](../../deploy/containerfiles/Containerfile.ecs.embedder#L60)).
Publish distinct amd64 artifacts without changing the running AWS image or task definitions.

## Corpus delivery contract

Corpus generation runs on AWS as the single writer per country dataset, orchestrated by Temporal Cloud; serving databases on VNGCloud and on AWS receive verified releases.
The [write-path decision](corpus-write-path.md#decisions) approves Temporal Cloud Developer, pay as you go with no minimum, and the isolated AWS writer. Existing AWS serving stays unchanged until opt-in.
The write path alone writes corpus evidence and desired release selection. Serving never repairs evidence.
The local importer applies the writer's ordered selection and reports its applied version; it cannot promote independently.
The auth runtime is the account writer, apart from scoped maintenance. Auth, release metadata, and evidence have separate roles and recovery boundaries.

| Required transfer item | Contract |
|---|---|
| Artifact | One PostgreSQL custom-format logical dump of the database design's serving projection per release. No auth, global roles, owner passwords, or arbitrary deployment scripts. |
| Manifest | Versioned JSON with country `vn`, immutable release ID, artifact bytes and SHA-256, schema/extension versions, restore-size estimate, all evidence/index/config recipe digests, model/tokenizer/dimensions, compatible images, evaluation evidence, counts, and accepted gaps. |
| Authenticity | Signed manifest from the approved publisher and trusted build identity. Hash every downloaded artifact. Do not equate a multipart object ETag with SHA-256. |
| Location | Private HCM04 release bucket, TLS transfer into a temporary path, then atomic local rename after verification. Resume only against the same digest. |
| Load | Create a fresh release database with a restricted loader. Restore with owner/ACL restoration disabled and preinstalled approved extensions. Reject unexpected schemas and objects. Never restore over the active database. |
| Validation | Database owner supplies the object allowlist, counts, referential/index checks, and compatible schema. Compare exact native citations and known gaps using real-document acceptance evidence. A valid signature does not prove legal accuracy. |
| Activation | Verify the writer's ordered activation record and expected applied version. Reject stale versions; apply retries idempotently. Check the bundle, pin each request, and acknowledge target/version/digest. Reconcile lost acknowledgements without issuing a new promotion. |
| Rollback | Apply a new writer-authorized ordered selection of a retained compatible bundle. Preserve current auth and client policy. Apply the single-runtime transition below and preserve retained evidence with explicit dense-search availability. Keep traffic fenced if no safe authorized selection exists. |
| Retention | Online retention is the active release plus at least two prior releases, kept for at least 30 days, plus any release under an explicit pin or hold. Apply this to databases, recipe artifacts, and referenced files together; a loaded old runtime is not required. Archive duration belongs to [database owner question 1](corpus-database.md#risks-and-owner-questions), which recommends one year. |

The write path owns promotion; hosting owns local readiness and application of ordered selections. The database owns selection storage and pins; do not invent a second catalog.
Only one embedding model runs on the serving host at a time, under the [embedding runtime policy](embedding-models.md#migration-and-rollout-slices).
Before replacing the runtime, re-embed every release that stays in dense search, or retire older releases from dense search.
Retired releases remain available through `document` and lexical search, with a visible gap. Retention, pins, and holds still protect their evidence and recipe artifacts.
Dense search requires a matching query runtime (same model, tokenizer, recipe and dimensions). No dual-runtime capacity is reserved.
Re-embedding creates new immutable releases and explicit predecessor mappings; old named releases never silently resolve to replacements.
Fence dense admission and drain in-flight dense requests before stopping the old runtime; start and verify the new runtime before reopening dense search. Reconcile availability through ordered activation records.
Runtime failure returns explicit dense-search unavailability, never another recipe or silent lexical-only fallback. Files alone do not establish dense readiness.
Freeze and attest the initial Vietnam export once; import only public corpus data from AWS. Never replicate accounts back to AWS. Preserve source URLs and file hashes.
If the document-parts download proposal is approved, use a private HCM04 artifact bucket and its same-origin web-session-authenticated route. Verify hashes, retain pinned bytes, and budget file bytes separately; never expose bucket URLs.
Hosting defines configurable positive limits: maximum artifact bytes, concurrent download streams per client and overall, and stream timeout.
Recommend 50 MiB (52,428,800 bytes) per artifact, two streams per client, eight overall, and 120 seconds total per stream. Key the client limit by authenticated account across sessions. Reject zero or negative configuration.
Serving checks the declared size before streaming and enforces actual bytes while streaming. Reject unknown size and oversized files before headers; never emit bytes beyond the declared size or cap.
Reject stream-slot exhaustion before transfer. Abort on timeout, size mismatch, or disconnect; release the slot on every exit. Oversized files retain their source link and availability gap.

## Accounts, backups, and restore

Keep Google identity records, web sessions, OAuth grants, audit data, and any later saved account state in the dedicated auth database on VNGCloud. Google provider tokens are not a corpus payload.
Use the canonical origin `https://tracuuquydinh.vn` and its `/mcp` resource.
The source derives issuer/resource from the origin ([pkg/oauthserver/config.go:43](../../pkg/oauthserver/config.go#L43)).

Use pgBackRest for the entire local PostgreSQL cluster: weekly full, daily differential,
continuous WAL archiving, weekly backup verification, and at least 30 days of recovery history.
Keep the predecessor full backup and all dependent WAL needed for that window.
Use a private HCM04 backup bucket, TLS, provider encryption, and client-side backup encryption.
Keep recovery keys in a separate owner-controlled encrypted recovery copy in Vietnam.
Back up the host bundle, encrypted secret recovery package, TLS state, and durable Vietnam control database: imported manifests, applied selection, pins, and local retention claims. Corpus snapshots exclude control state.
Do not restore an old host bundle's client allowlist or secret versions over current security policy.

pgBackRest documents S3-compatible repositories, encryption, and point-in-time recovery;
VNGCloud documents an HCM04 S3 interface. Their interoperability, signing, path style, multipart,
retention, and restore performance still need a drill. See [pgBackRest](https://pgbackrest.org/user-guide.html)
and [HCM04 integration](https://docs.vngcloud.vn/vng-cloud-document/vstorage/object-storage/object-storage-hcm04/3rd-party-softwares/s3-sdk/integrate-s3-sdk-with-vstorage).
Do not substitute volume snapshots for database backups. Whole-cluster restore is disaster recovery, never the corpus rollout mechanism.

Recommend a recovery point objective of five minutes for ordinary auth writes and a recovery time
objective of four hours. These are launch targets, not guarantees. Archive WAL at least every minute
under low traffic; alert at five minutes of archive lag and fence new auth writes if the limit persists.
There is no zero-loss promise for ordinary writes on one host. Corpus recovery uses an intact release.

After any restore, serving reconciles its active release with the writer's ordered activation records before it accepts traffic.
Keep public traffic and retention deletion fenced until the [database-owned catalog recovery contract](corpus-database.md#releases-delivery-and-retention) proves continuity beyond the recovered WAL. Persist both fences across restart.
Reconcile the latest writer-authorized selection, activated manifests, identity keys, pins, and acknowledged holds from recovery records independent of that backup. Never infer current selection from restored readiness alone.
Restore every retained database plus query-recipe artifacts; verify the selected runtime and each release's dense-search availability before reopening. Missing or unverifiable continuity keeps both fences closed; a restored deletion claim cannot authorize deletion.
Corpus recovery targets belong to [database owner question 3](corpus-database.md#risks-and-owner-questions); the five-minute ordinary-auth target does not permit loss of activated manifests or acknowledged holds.

An older backup must not re-enable an account or credential. Add these recovery controls:

1. Route blocks and unblocks through an owner-only wrapper. Fence auth/evidence and drain requests before appending any intent. Before committing a block, durably
   write an encrypted, signed block intent to a separate HCM04 security bucket. Include canonical
   issuer/subject, a monotonic sequence, and operation ID. Never use the restored numeric account ID alone.
2. Acknowledge and reopen only after journal and database agree. Keep auth/evidence fenced on failure or ambiguity until the intent is applied or an owner-authorized resolution is durable.
   An interrupted block remains blocked on recovery. Record unblocks too, but require owner
   confirmation before replaying an unblock after restore. Keep a complete checkpoint plus later intents.
3. Keep that journal outside the database restore lifecycle. Deny overwrite/delete to its routine writer;
   prove bucket version/retention protection before relying on it. Preserve block state for the account's
   lifetime and while any restorable backup can recreate it. Raw block SQL is emergency-only and must
   establish the same durable record before service reopens.
4. Restore behind the fence. Recover the latest available WAL, reconcile every block by issuer/subject,
   and retain a deny entry for identities absent from the restored backup, checked before registration.
   Missing or unverifiable journal continuity keeps login and token issuance closed for all users.
5. Revoke every restored browser session and OAuth grant family. Invalidate login, consent, code,
   access, and refresh state; increment account epochs. Rotate transaction and OAuth HMAC keys,
   accepting no old HMAC key after disaster recovery. Require fresh login and client consent.
6. Verify a previously blocked identity cannot log in or register again, and every pre-restore
   credential fails. Clear the traffic fence only after these checks and catalog reconciliation. Enable retention deletion only after the database contract revalidates deletion claims. Apply current client policy.

The journal is recovery evidence, not a second live account database. Normal authorization reads
PostgreSQL. Journal reconciliation and missing-identity denial are required new auth work.
Revoking all restored credentials covers revocations newer than the recovered WAL without exporting
each token revocation. Merely incrementing epochs in a restored backup is insufficient for lost blocks.
Rehearse this procedure on a temporary HCM03 server before traffic, then quarterly. Fence the old host before giving the replacement the floating IP and write authority.
Destroy drill data and credentials after recording non-sensitive timings and pass/fail evidence.

## Security and residency

Use SSH keys and allowlisted owner IPs. Disable password and root SSH; give the deploy user a narrow sudo allowlist.
Give an agent a named, audited Linux identity and a separate VNGCloud read-only service account.
Read-only wrappers are the default; any write uses a named operation-specific identity.
Do not grant an agent deployment, secret-read, auth-row-read, or account-root privileges by default.
The VNGCloud root account is for exceptional account recovery, never routine work.
The [review API comes first](corpus-review.md#decisions), behind mutual TLS with a private CA; the owner reviews through AI agents. Keep its opaque reviewer keys and Vietnam audit journal. The human console is deferred on the same API, with human MFA and no human IP allowlist.

Use one OpenTofu service account with only the network/server/volume operations this root needs.
Use a separate service account per bucket and role: release fetch, backup, security journal, and state.
Policies name exact buckets and explicit actions, with no wildcard action or resource.
Separate backup retention deletion from routine backup writes where pgBackRest permits it.
Confirm provider action names and negative permission tests during owner apply; do not invent IAM syntax.

Store runtime secrets in root-owned mode 0400 files on the encrypted data volume.
Grant named units only their Podman secrets. Current auth source reads environment variables
([pkg/identity/config.go:12](../../pkg/identity/config.go#L12)); use Podman secret environment injection
for those fields, without values in unit files, command arguments, logs, or inspection artifacts.
Keep Podman secret persistence on encrypted storage too. Rotate Google, database, embedder, OAuth,
backup, and deployment credentials by replacing the named secret and recreating affected units.
Revoke prior credentials after validation. Keep old backup decryption keys through backup expiry.
Use a fenced reset for transaction-key rotation; invalidate in-flight login and consent transactions.
Ordinary OAuth HMAC overlap must respect token lifetimes; disaster recovery uses no overlap.

Preserve canonical Host at Caddy. Strip incoming forwarding and origin-verification headers,
then set the real peer IP and a local edge-to-app secret. Trust forwarded IPs only on the private
edge listener. Never use forwarded Host or a jurisdiction header to select a corpus.
Keep browser Origin/CSRF checks and bearer-only MCP admission. Test malicious Origin values,
cross-site fetch metadata, and missing-Origin server clients independently.
Start with the coded IP/protocol limits; add an initial per-account search limit of 2 requests/second,
burst 8, before inference. This quota is proposed work, and shared client NATs need load testing.
Keep the 1 MiB HTTP cap and 4,096-byte query cap; bound headers, timeouts, connections, and queues.
The query default is coded at [pkg/base/config/config.go:325](../../pkg/base/config/config.go#L325).
Block public access to embedder batch endpoints. Keep MCP streaming unbuffered at the edge.

Run Coraza rules in staging against OAuth, JSON-RPC, and streaming fixtures before blocking production.
Use narrow tested rule exclusions, never a blanket `/mcp` or OAuth bypass.
Disable WAF body/header audit capture, SQL parameter logging, core dumps, and request-body logging.
Log route templates, status, latency, and opaque request IDs. Strip query strings, cookies,
Authorization, callback parameters, email, and question text across Go, Caddy, WAF, and journald.
Keep restricted local IP security logs for seven days and redacted operational logs for 30 days.
Do not store query history. Use `Referrer-Policy: no-referrer` and self-hosted browser assets.
Set `BANHMI_DNS=system` with Vietnam resolvers; source otherwise falls back to public resolvers
([pkg/base/dns/dns.go:1](../../pkg/base/dns/dns.go#L1)).

CrowdSec normally can share attack signals with its central API. Disable online sharing,
console enrollment, and external notifications; verify egress. Ship reviewed rule updates in the bundle.
See [CrowdSec configuration](https://docs.crowdsec.net/docs/configuration/crowdsec_configuration/).
Allow runtime egress only for Google login, Vietnam storage/resolvers/time service, certificate lifecycle, and one scoped exception: the [review approval service](corpus-review.md), under its separate bridge identity, may reach pinned AWS review endpoints for public packets, signed approval attestations, and receipts. Query-serving processes never reach those endpoints. Fetch registries/provenance only during deploy.
Production startup must require literal loopback query endpoints and nonempty runtime tokens; reject remote endpoints. Hosted checks must prove external query egress fails.
No question may go to an external inference endpoint, telemetry service, or cloud batch processor.

| Data flow | Residency rule or remaining external transfer |
|---|---|
| Website questions and query vectors | Process in RAM on the Vietnam host. Do not persist or send to third parties. |
| Auth, audit, IP logs, secrets, backups, recovery journal | Store in Vietnam. Verify provider replication, support access, and recovery-key locations contractually. |
| Google login | The person signs in at Google. Our server sends the one-time code, PKCE verifier, registered redirect URI, and client credentials, and receives transient access and ID tokens. It keeps the account subject and verified email and discards provider tokens. No stored personal data leaves Vietnam. Never attach a question to state or callback URLs. |
| User's AI client and `/mcp` | A client the user connects may run abroad. The user starts that transfer. Before launch, the privacy notice must say so; the current notice names only Google and cloud infrastructure ([landing.go:451](../../cmd/server/landing.go#L451)). Our server returns only the public legal evidence the client requests. |
| GitHub CI, provenance, registries, uptime | Source, digests, build metadata, and anonymous health status cross borders. No live queries, auth dumps, tokens, logs, or private restore data enter CI. |
| Cloudflare DNS, registrar, Let's Encrypt | DNS names, resolver traffic, domain validation, certificate records, and operator contact metadata reach external services. DNS-only does not proxy HTTPS bodies. |
| Source OCR and bulk embedding | Google Cloud Vision handles OCR; Hugging Face Jobs handles bulk document embedding, pay as you go, with Runpod Flex as fallback. Kaggle and GitHub Actions free tiers serve evaluation and bake-offs only. Back up OCR outputs and originals in AWS S3, public legal text only. Never send account data or user questions. |
| Monitoring email | Use a Vietnam-hosted relay and mailbox; send only generic alerts. A foreign mail service needs an owner decision first. |

[Luật 91/2025/QH15, Điều 20](https://congbao.chinhphu.vn/detail/tai-ve?id=45578&slug=91-2025-qh15)
governs transfers of personal data abroad. Personal data at rest, in backups, in logs, and at the edge
stays in Vietnam with Vietnam providers, so the service makes no such transfer and files no
cross-border dossier. The flows above that reach foreign services carry no stored personal data or
are started by the user. Adding any processor outside Vietnam for personal data needs an owner
decision first.

## Domain, readiness, and monitoring

Use Cloudflare DNS-only A records; add AAAA only with equivalent IPv6 controls. The owner installs DNSSEC's DS record at the `.vn` registrar and verifies the chain.
Use Caddy and Let's Encrypt HTTP-01; keep TCP 80 reachable for the challenge and redirect other HTTP
requests to HTTPS. Use the staging CA first. Persist TLS state and alert on renewal failures.
See [DNSSEC](https://developers.cloudflare.com/dns/dnssec/) and
[HTTP-01](https://letsencrypt.org/docs/challenge-types/).

Add `/readyz`: bounded checks of auth/control connectivity, completed catalog recovery reconciliation, every retained release database, declared dense-search availability, and the single selected runtime's model/tokenizer/recipe/dimensions, and deploy fence. Return generic 200 or 503 without private details; a network-fenced host is unavailable.
Cache a cheap probe result briefly; do not run inference per public probe. Keep `/healthz` as liveness.
Five-minute host checks cover disk/inodes, memory, units, certificate expiry, WAL age, backup age,
clock drift, and journal checkpoint health. Send generic email alerts. A GitHub uptime workflow checks
only `/readyz`; treat scheduled CI as a secondary monitor, not a precise five-minute timer.

Register exact Google callbacks and approved MCP client redirects for the new origin.
Test real Google login and at least one named ChatGPT and Claude client before broad access.
Do not assume MCP clients follow redirects or carry OAuth grants across domains.
Recommend a 30-day reconnection notice at the old Vietnam website and explicit retired-service
responses for its MCP route after owner approval. Do not forward questions or tokens from AWS.
Keep the old Vietnam endpoint unchanged until that decision; keeping it active means Vietnam users
can still choose an AWS path, which the privacy notice must distinguish from the new service.
Never redirect or retire the five other country endpoints. Rollback of the new product stays in Vietnam.

## Cost estimate

The owner-supplied HCM03-1A quotes give a starting total of about **1.35 million VND per month**, including vStorage backups. Existing AWS costs remain separate. These are supplied quotes, not a fresh provider check.

| Item | Monthly VND | Basis |
|---|---:|---|
| `a-general-4x8`, 4 vCPU, 8 GiB | 953,568 | Quoted with VAT in HCM03-1A; starting host |
| `a1-standard-2x8`, 2 vCPU, 8 GiB | 800,352 | Cheaper fallback only if measurement allows; replaces the compute row above |
| 30 GB SSD root | 96,000 | Supplied quote |
| 50 GB data SSD | About 160,000 | Encryption adds no volume charge |
| Floating IP | About 120,000 | Supplied allowance |
| vStorage backups | Size from measured backups and WAL | Included in the approximate total; exact usage cost remains unmeasured |
| Starting total | About 1,350,000 | Starting compute, disks, floating IP and vStorage backups |

Use Gold initially for releases, WAL, and backups; reject an archive-only assumption until small-object WAL traffic, minimum retention, retrieval charges, and drills prove suitability.
Size objects from retained release dumps/blobs plus backup and WAL chains. Original source files stay in object storage. Measure storage, transfer and drill costs before growth; no fixed 300 GB object allowance or larger host tier is required.

## Interfaces

| Sibling | Required contract; internals stay with its owner |
|---|---|
| `docs/specs/corpus-write-path.md` | Publishes a signed validated artifact, manifest, and ordered activation record. Owns processing, placement, retries, promotion, and retention execution. Receives readiness/application receipts, never serving auth data. |
| `docs/specs/corpus-database.md` | Supplies dump/schema and extensions, loader permissions, restore sizes, activation/pins, retention, and catalog recovery of latest writer-authorized selection, activated manifests, identity keys, and acknowledged holds. Owns continuity proof and deletion-claim revalidation. Snapshots include first-class revisioned parts, the shared provision/part validity target `silver.evidence_target`, ordered blocks, table rows/cells/spans, and part-to-artifact byte spans. Auth and mutable control state stay outside snapshots. |
| `docs/specs/embedding-models.md` | Supplies amd64 artifacts, model/tokenizer/recipe/dimensions, bounded query interface, single-runtime readiness, and peak memory/CPU evidence. Owns latency question 1 and the single-runtime transition; hosting owns capacity. No remote query fallback or reranker. |
| `docs/specs/document-parts.md` | Supplies file-access policy, immutable asset manifest if needed, sizes, hashes, and allowed content types. Owns part identification, citation, and display from conversion's block model; conversion owns extraction from bytes. Do not expose a source bucket or add file serving without its approved contract. |

## Rollout and verification gates

Each slice produces reviewable source and GitHub-hosted evidence. No build, test, linter, generator, database command, or cloud operation ran for this revision.
Retain the existing read-only CI gate at [.github/workflows/reusable-checks.yml:6](../../.github/workflows/reusable-checks.yml#L6).
No pull request receives cloud credentials; use synthetic data for auth and restore checks.

| Slice | GitHub-hosted proof | Owner-run gate after CI |
|---|---|---|
| 1. Infrastructure contract | Pinned OpenTofu `fmt -check`, credential-free `init -backend=false` and `validate`; shell syntax/ShellCheck; policy fixtures reject wildcard IAM and public DB ports. | Confirm SKU, encryption, region, backup routing, quote, state backend, and scoped IAM. Review/apply the plan. |
| 2. Host and edge | Cloud-init/systemd/Caddy checks, redaction, amd64 provenance, atomic bundle/fence failures, remote-embedder rejection, and rejected language inputs reaching neither embedding nor corpus queries. | Install the bundle; prove socket isolation, SSH/firewall/TLS, fence survival across edge replacement, and denied external query egress. |
| 3. Auth and readiness | Isolated PostgreSQL checks for socket roles, migrations, readiness, failed/ambiguous block and unblock journaling, older-backup recovery, absent identities, and old-credential rejection. | Register Google/client settings, bootstrap auth in Vietnam, rehearse secret rotation and auth outage behavior. |
| 4. Corpus delivery | Synthetic bundles reject tampering, wrong schema/recipe, auth objects, absent runtimes/capacity proof, and interrupted activation. Prove re-embedded dense search and old named-release `document`/lexical access with a visible dense-search gap after promotion and rollback; holds beyond 30 days preserve evidence and recipe artifacts. Download cases cover declared/actual oversize, interruption, timeout, slot release, and per-client/overall exhaustion. | Check real-release citations/gaps; prove 25% headroom, embedding/search latency and auth availability at 2 requests per second, bursts of 8, with one runtime and PostgreSQL/application load; rehearse rollback and downloads. |
| 5. Recovery | Restore-script control flow, missing-key/journal/catalog-continuity failures, fence persistence, backup-retention and publisher-permission fixtures. Restore an older backup after a model-changing activation and a later acknowledged hold; prove latest writer-authorized selection, activated manifests, retained-release reads and lexical searches plus matching-runtime dense search where available, and deletion protection. | Repeat that sequence on a temporary Vietnam server. Prove missing continuity blocks traffic and retention deletion, latest blocks survive, stale credentials fail, and measured recovery meets auth targets and database owner question 3. |
| 6. Cutover | DNS/TLS and endpoint configuration checks, exact-origin/client fixtures, uptime workflow, unchanged AWS configuration diff. | Only after the restore drill: verify DNSSEC, lower TTL, pass external Google/MCP checks, open traffic, and monitor. Apply the separately approved old-Vietnam endpoint policy. |

Record exact commit, bundle/release digests, workflow runs, and owner smoke/drill results.
Infrastructure validation proves syntax and provider schema, not capacity, IAM enforcement, residency,
or deployed security. Require independent adversarial review of deployment and restore paths before implementation approval.
The integrating owner must link this proposal in the documentation index and reconcile superseded hosting/status wording. This task owns only this file.

## Risks and owner questions

Single-host failure stops all new-product services. HCM03 and HCM04 labels do not prove independent
disaster domains. Confirm physical placement and replication; a wider disaster may exceed four hours.
Volume encryption does not protect an already-compromised host. Backup deletion credentials and
recovery keys need separate protection. WAF false positives can break OAuth or MCP streaming.
Release retention and auth growth can exceed the disk estimate. Static CI cannot settle these risks.

1. Accept five-minute ordinary-auth recovery loss and four-hour recovery time? Recommend yes for launch,
   subject to the drill, with durable acknowledged blocks and fresh credentials after every auth restore.
2. Keep, redirect, or retire the old Vietnam AWS endpoint? Recommend a 30-day website notice,
   retire anonymous MCP evidence at the approved cutover, and require explicit reconnection.
3. Accept local log retention of seven days for IP security data and 30 days for redacted operations,
   with a Vietnam mail relay? Recommend these limits; agree separately on auth audit and deletion retention.

Unverified: quoted SKU availability at deployment, measured backup/transfer cost, encryption controls and key custody, HCM03/HCM04 failure-domain
separation, managed PostgreSQL suitability, S3/backend/IAM edge behavior, storage footprint and WAL rate,
amd64 model performance, production client compatibility, and live service state.
