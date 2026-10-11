# Country modules

Status: Proposal. Not approved, not implemented.

## Purpose and authority

Rebuild the corpus system around a country-agnostic core and one module per country. Reuse useful v1 code, fixtures, and evidence. Do not turn migration into a series of v1 repairs.
Design and build Vietnam first. Early milestones contain only the core and Vietnam module, with retained v1 packages. After Vietnam's write path runs end to end on GitHub-hosted CI, migrate Malaysia, Singapore, Indonesia, Thailand, and Cambodia as compiled, unit-tested modules with state `migrated-disabled`. Their migration never blocks a Vietnam milestone. Their existing AWS services keep running unchanged.
Module state applies only to the new system. It says nothing about a country's existing service or corpus quality.

[AGENTS.md](../../AGENTS.md) governs work. The [architecture](../ARCHITECTURE.md), [delivery plan](../../PLAN.md), [country records](../design/jurisdictions/README.md), and [playbook](../design/jurisdictions/PLAYBOOK.md) describe v1 inputs.
The eight corpus specs own their respective contracts; this file supplies their country extension points. It changes no settled provider, residency, model, acceptance, or release decision.
Temporal Cloud orchestrates one AWS writer per country. Bulk embedding uses Hugging Face Jobs, with Runpod Flex as fallback. Google Cloud Vision performs OCR; S3 backs up approved public originals and OCR outputs.
Vietnam serving uses a small dedicated VNGCloud host and one query embedding model. No answer, generative, or reranker model runs in this system. Review starts with the mutual-TLS API; the console remains deferred.
No personal data leaves Vietnam. Public availability alone does not qualify a payload for export. The core applies the [review residency boundary](corpus-review.md) and [hosting controls](vngcloud-hosting.md) before any AWS, Temporal, OCR, or embedding handoff.

## Ownership

The core owns execution and evidence integrity. A module supplies deterministic country rules and versioned data. Modules return observations, candidate structures, assertions, and findings; they cannot accept evidence, authorize a reviewer, or promote a release.

| Core responsibility | Country module contribution |
|---|---|
| [Temporal workflows](corpus-workflows.md), queues, leases, retries, cancellation, admission epochs, batch reconciliation | Source inventory, bounded discovery adapters, source schedules, fetch politeness, backfill capabilities, and source-specific access requirements |
| [Conversion](document-conversion.md), byte adapters, `ConvertedDocument`, final block ownership, reversible edits, source maps, sandboxing, OCR execution | Script and language checks, encoding profiles, OCR language hints, normalization dictionaries, body grammar and source-tree interpretation |
| [Parts](document-parts.md), boundary reconciliation, artifact association, part targets, generic display model | Appendix/form labels, boundary grammar, numbering namespaces, attachment roles, and exact native citations |
| Quality reports, mandatory gates, quarantine, candidate comparison, fenced acceptance | Language-specific corruption signals, expected structure, additional findings, evaluation goldens and country floors; never weaker mandatory gates |
| [Database](corpus-database.md), schema, SQL, identity catalog, revisions, transactions, retention and export closure | Document-type taxonomy, document-number grammar, source authority mappings, subject taxonomy seeds and assignment rules |
| Relation evidence, target resolution, redline computation and effective-dated validity storage | Amendment/relation grammar, operator/target/recital/replacement spans, commencement rules, partial effects and unresolved conditions |
| [Releases and write path](corpus-write-path.md), build eligibility, manifests, signing, delivery, activation, rollback | Immutable module and policy digests; country qualification report |
| [Embedding jobs and retrieval](embedding-models.md), chunk mechanics, dense/lexical fusion, recipe compatibility | Lexical tokenization, abbreviation dictionaries, native context labels, query language gate, evaluated country retrieval settings |
| Serving, release pins, access control and the five MCP tools | MCP guide, tool descriptions, source labels, native citation rendering and gap wording |
| [Review API](corpus-review.md), mutual TLS, human authority, audit journal, decision application | Country-specific assertion kinds, evidence explanations and validation rules; no new approval powers |
| Website shell, routes, authentication, accessible components and evidence operations | Locale strings, labels, date presentation and source-link formatting; no country-specific route handlers |

The five tools remain `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`. Website and MCP use the same evidence service and query gate.
The conversion core extracts bytes for every document format. Country structure rules consume validated blocks. Source listing/detail parsers implement the `Decoder` contract below; they consume captured raw bytes only in the converter sandbox and never fetch.
The parts core calls country boundary rules before the conversion core assigns final body/part ownership. Spreadsheets remain searchable file parts without rich rendering. Presentations expose metadata and source links only.
Relations and validity rules consume evidence, not just a matched number. Missing or conflicting dates and targets remain unresolved. A module cannot promote source confidence `1` into legal correctness or treat partial repeal as whole-document repeal.

## Package boundaries and registration

**Decision:** Keep the established domain packages and add narrow core contracts plus country modules.
**Why:** A single large `internal/core` tree would rename useful code without improving the existing write/read separation.
**Rejected:** A country fork of the pipeline, runtime Go plugins, or a blanket package move before Vietnam works.
**Detail:** Use explicit constructors, one registry, and consumer-owned Go interfaces. The paths below are proposed homes unless marked retained.

| Path | Responsibility and imports |
|---|---|
| `internal/core/country` | Country codes, manifest schema, state, registry and typed admission errors. No database, Temporal SDK, source client or transport imports. |
| `internal/country/catalog` | The single compiled entry list and manifest validation. Imports only pure module roots. Both composition roots use this list. |
| `internal/country/<code>` | Pure descriptor, number/citation/language rules, guide and locale access. No I/O or `init` registration. |
| `internal/country/<code>/grammar` | Body, parts, amendment, validity and subject rules. Imports consumer contracts and neutral block/target types, never another country module. |
| `internal/country/<code>/sources/<source>` | Pure request planner and sandbox decoder. No network clients, stores, workflows or release code. Worker composition binds them separately. |
| `internal/country/<code>/corpus` | Lazy source factory bindings for the worker. The module root never imports this package. |
| `internal/corpus/model` | Neutral block, span, target, observation and assertion values from the sibling specs. No registry, runtime or persistence imports. |
| `internal/corpus/{source,conversion,parts,quality,relations,validity,subjects}` | New consumers and shared algorithms. Each `api` subpackage owns pure extension interfaces and uses the neutral values. Generic tree helpers belong in the core, not in a country module. |
| `internal/corpus/{run,blob,release,workflow/temporal,schematest}` | Retained core packages; adapt to the eight specs without losing fences, blob identity or release pins. |
| `internal/corpus/{store,embedding,review}` | Proposed persistent adapters, bulk-job ports and writer-side review application. Review API placement and identities follow [corpus review](corpus-review.md#review-api-first). |
| `internal/evidence/{api,service}`, `internal/app/serve` | Retained read contracts, common evidence operations and serving composition. No corpus worker or source adapter imports. |
| `internal/app/corpus`, `cmd/corpus-worker` | Proposed worker composition and executable under the write-path spec. Bind module sources to core activities. |
| `pkg/{rag,mcp,web,identity,oauthserver}`, `pkg/base/*` | Reuse shared runtime code at existing paths; extract country policy into modules. No rename-only prerequisite. |
| `sql/`, `pkg/store/*`, `sqlc.yaml` | Retained schema/query sources and generated data layer. Hosted generation only; modules own neither SQL nor migrations. |

Interfaces below are sketches, not implemented APIs. Define each behavioral interface at its named consumer. The registry stores a validated bundle of those interfaces; it does not duplicate them in a universal framework.
The registry imports only consumer `api` packages, never their implementations. Those APIs depend on neutral values, not the registry or each other. Use `internal/evidence/api` for read extension contracts and `pkg/rag/lexical/api` for tokenization. This keeps serving free of converter binaries and avoids import cycles.
The [review placement contract](corpus-review.md#application-fencing-and-residency) separates the AWS private agent API for queue/proposal/check operations from the Vietnam review approval API, service attestation bridge and human audit.
Agent principals are non-personal service identities. Human actors use opaque reviewer keys; the bridge issuer is a separate service principal. Human identities, account/grant epochs, private notes and key-to-person mappings stay in Vietnam under that contract.

```go
// internal/core/country: immutable configuration, assembled without I/O.
type Code string
type State string
const (
    Enabled State = "enabled"
    MigratedDisabled State = "migrated-disabled"
    Absent State = "absent"
)
type Entry struct {
    Code Code
    State State
    Manifest Manifest
    Rules RuleBundle
}
type Registry interface {
    Lookup(Code) (Entry, error)
    Entries() []Entry
    RequireEnabled(Code) (Entry, error)
    BindSources(Code, []source.Factory) error
    Freeze() error
}
// NewRegistry(entries []Entry) (Registry, error)
// catalog.Entries() []Entry is the only compiled country inventory.

// internal/corpus/source/api: country planning is pure, without transport.
type Planner interface {
    Listing(DiscoverRequest) (RequestPlan, error)
    Detail(DetailRequest) (RequestPlan, error)
    Artifact(ArtifactRequest) (RequestPlan, error)
}
type Decoder interface { // Country implementation runs only inside the sandbox.
    Listing(CapturedInput, DiscoverRequest) (DiscoveryPage, error)
    Detail(CapturedInput, DetailRequest) (DetailResult, error)
}
type Factory struct {
    SourceID string
    NewPlanner func() Planner
    NewDecoder func() Decoder
}
type NumberSearcher interface {
    PlanNumber(NumberRequest) (RequestPlan, error)
}
type Fetcher interface { // Core activity on corpus-fetch-v2; never parses content.
    Capture(context.Context, FetchRequest, SourcePolicy) (CaptureRef, error)
}
type DecoderSandbox interface { // Core supervisor dispatches corpus-converter-v2.
    Listing(context.Context, DecoderRef, CaptureRef, DiscoverRequest) (DiscoveryPage, error)
    Detail(context.Context, DecoderRef, CaptureRef, DetailRequest) (DetailResult, error)
}

// Consumer contracts: all methods below are deterministic and perform no I/O.
type IdentityRules interface { // internal/corpus/source/api
    Classify(Observation) (DocumentType, []Finding)
    ParseNumber(string, IdentityContext) (NumberMatch, []Finding)
}
type BodyRules interface { // internal/corpus/conversion/api
    Structure(ConvertedDocument, PartResult, SourceTree) (BodyTree, []Finding)
}
type PartRules interface { // internal/corpus/parts/api
    Identify(ConvertedDocument, ArtifactInventory) PartResult
    Cite(TargetPath) (Citation, error)
}
type PartResult struct {
    ClassifiedRanges []ClassifiedRange
    Namespaces []NamespaceBoundary
    RequiredArtifacts []ArtifactExpectation
    Exclusions []ExcludedRange
    ReferenceSpans []PartReference
    Findings []Finding
}
type RelationRules interface { // internal/corpus/relations/api
    Parse(RelationInput) ([]RelationAssertion, []Finding)
}
type ValidityRules interface { // internal/corpus/validity/api
    Resolve(ValidityInput) ([]ValidityAssertion, []Finding)
}
type LanguageRules interface { // internal/corpus/conversion/api
    Normalize(TextInput) (MappedText, []Finding)
    Inspect(ConvertedDocument) ([]LanguageMetric, []Finding)
}
type QueryRules interface { // internal/evidence/api
    ValidateQuery(string) (QueryDecision, error)
}
type LexicalRules interface { // pkg/rag/lexical/api
    Tokens(string) ([]string, error)
}
type SubjectRules interface { // internal/corpus/subjects/api
    Assign(SubjectInput) ([]SubjectAssertion, []Finding)
}
type PresentationRules interface { // internal/evidence/api
    Guide(Locale) (GuideText, error)
    Strings(Locale) (LocaleStrings, error)
    SourceURL(SourceReference) (string, error)
}
```

`RuleBundle` holds those typed implementations, never `any` or parser-name switches. Split it into reader and writer views so serving receives no mutation capability. In the sketch, `source` aliases `internal/corpus/source/api`; neutral input/output types come from `internal/corpus/model` or the read API.
Each module exports `Entry() country.Entry`; its worker-only `corpus` package exports `Factories() []source.Factory`. Both functions return immutable values without I/O. The catalog calls the former once; worker composition binds the latter once before `Freeze`.
Body and part citation rendering share a native-label grammar. `Cite` accepts document, body, appendix, form and table paths; it returns printed bytes plus a distinct matching key, never translated labels.

| Input/output type | Required fields and behavior |
|---|---|
| `Manifest` | Code, module version/digest, contract version, source descriptors, corpus/binding languages, OCR hints, taxonomy/dictionary/golden/floor references, guide/locales, supported grammar and capability versions. |
| `SourcePolicy` | Allowed origins and redirects, access/egress profile reference, minimum interval, concurrency, timeouts, source-attempt ceiling, byte/page caps, incremental/full-sweep intervals, timezone, overlap policy and permitted backfill modes. Limits are positive; zero retries means one attempt. Secrets are not fields. |
| `RequestPlan`, `FetchRequest` | Bounded requests with country/source, purpose, approved locator, method, headers/body references, parent capture, cursor, policy digest and byte/time budgets. Plans contain no network results or credentials; the core validates every initial and follow-up request. |
| `CaptureRef`, `CapturedInput`, `DecoderRef` | Immutable bronze reference with country/source, request/response metadata, actual bytes, SHA-256, media type and transport receipt. The supervisor verifies and mounts bounded capture bytes as `CapturedInput`, bound to the reference. `DecoderRef` pins country/source, decoder version and sandbox image; no remote URI resolution occurs inside a decoder. |
| `DiscoverRequest`, `DiscoveryPage` | Country/source, opaque cursor, mode, source slice, fixed time window, policy digest and budgets in; typed listing records/observations, capture references, follow-up requests, proposed next cursor, coverage and findings out. The core commits no cursor advancement on incomplete required slices. |
| `DetailRequest`, `DetailResult`, `Observation` | Opaque external ID and listing capture references in; typed source metadata, source-tree and relation observations, required-artifact inventory, follow-up requests, capture hashes and findings out. Each observation retains raw field spans before interpretation. |
| `ArtifactRequest` | Country/source, approved artifact reference, parent observation, role and caps. The planner emits a request; core capture returns `CaptureRef`. Document conversion consumes the captured file separately. |
| `NumberRequest`, `NumberMatch` | Raw number, type, issuer and title context; candidates retain exact number bytes, normalized key, namespace and ambiguity. No match differs from ambiguous match. Search results must recheck number equality. |
| `ConvertedDocument`, `MappedText` | Conversion's versioned blocks, source spans, raw/normalized hashes, offset maps and edit ledger. A transformation cannot return bare replacement text. |
| `PartResult` | Classified block ranges, namespace boundaries, required-artifact inventory (attachment expectations), exclusions with spans/reasons, reference spans with parsed selectors and resolution evidence, and part-gate findings. Retain artifact associations, native labels and unresolved regions. |
| `BodyTree`, `TargetPath` | Revision-local nodes, parent namespaces, ordered block ranges and exact native labels. Use the database's shared provision/part target type. |
| `RelationInput`, `ValidityInput` | Captured source assertions, exact acting/target spans, candidate identities, part/body trees, commencement evidence and explicit evaluation date. No implicit wall clock or whole-corpus scan. |
| Assertions and `Finding` | Kind, exact target/scope, evidence refs, authority, confidence basis, recipe digest and reason code; nullable dates and unresolved conditions. Core reconciliation and review determine acceptance. |
| `SubjectInput` | Pinned taxonomy, text targets, source classifications and bounded candidate rules. Output assignments retain rule/evidence and context role; subjects never change document identity. |
| `QueryDecision` | Accepted question in the module's corpus language or exact number, or rejection reason and localized guidance. Vietnam applies its stricter rule below. Only accepted text reaches the local embedder and corpus queries. |
| `GuideText`, `LocaleStrings`, `SourceReference` | Versioned tool guidance and complete keyed interface strings; country/source plus opaque source ID for pure URL formatting. Unsupported locales return an error; strings cannot inject HTML or define routes. |

The country module plans requests without I/O. The core fetch activity on `corpus-fetch-v2` executes them under source politeness rules, streams raw responses to bronze and returns immutable capture references with hashes. No country code both fetches and parses.
The core injects `Fetcher` and `DecoderSandbox` into its source controller. The sandbox supervisor selects the registered `NewDecoder` by `DecoderRef`, mounts verified captures and runs listing/detail decoding on `corpus-converter-v2`. Only the supervisor resolves storage references; the decoder receives no transport, credentials, database or network access.
The controller validates typed outputs and follow-up plans before fenced acceptance or another fetch. Follow-ups consume the same bounded source window; they cannot reset page, byte or attempt budgets. Block-only structure runs on `corpus-parser-v2` after sandbox validation.
Retries follow the [core stage retry contract](corpus-write-path.md#stage-contract): Temporal owns infrastructure retries; the ledger owns five source attempts over 24 hours, recorded `NextEligible`, preserved `Retry-After`, and bounded jittered backoff. No overlapping Temporal source retry or inner v1 source retry loop is allowed. A country module may only make pacing stricter or retries fewer; it never adds retries.
Module schedules are defaults, not timers. The core applies source policy plus stricter deployment limits, hashes the effective policy, and creates schedules only after admission. Partial results retain evidence but cannot claim complete discovery.
Parts returns `PartResult` before conversion's `BuildStructure` assigns final ownership. Conversion consumes classified ranges and namespaces to separate body/part/cell containers; it consumes exclusions, required artifacts, reference spans and findings for ownership and the document quality gate. References never claim a second block owner.
Persist all six fields with revision, recipe and source spans. Required artifacts and exclusions support completeness accounting; reference spans retain exact selectors, resolved targets or unresolved stubs for reference checks. Core validation cannot discard a blocking part finding or silently omit an expected attachment.
Omitted optional capabilities return an explicit unsupported result. Missing mandatory interfaces or empty required data fail registry construction. Methods return findings for uncertain evidence. Invalid inputs return an error where the signature permits one, otherwise a blocking contract finding; the core accepts no output from that call.

### One registry, three states

Both composition roots construct the same catalog. The writer attaches factories from each `<code>/corpus` package to the existing entries through `BindSources`, validates source IDs and freezes the registry. Serving uses the reader view and links no source factories.
`BindSources` is assembly, not a second inventory. It accepts only declared sources, rejects duplicate bindings and invokes no constructor. Writer startup requires complete bindings for enabled entries. CI checks factory coverage for every migrated entry.

| State | Compiled data and rules | New runtime behavior |
|---|---|---|
| `enabled` | Complete module and qualified policies | Eligible for admission. Work and serving still require the live gates below. |
| `migrated-disabled` | Complete v1 migration, fixtures and factories, including explicit capability gaps | Never instantiate live sources, create pools, schedule/start workflows, submit provider jobs, promote releases or expose any route/tool. Offline CI can invoke pure functions and fake transports. |
| `absent` | No module implementation; lookup returns `ErrCountryAbsent` | Reject before I/O. No fallback to Vietnam. |

Early milestones compile the core, Vietnam module and retained v1 packages. The other five countries are `absent` from the new registry until migration; retain their existing v1 checks. Keep Vietnam disabled during construction until its qualification gates pass.
After milestone 5, all six new modules compile and unit-test: `vn=enabled` after qualification and `my,id,sg,th,kh=migrated-disabled`. Unknown codes resolve to `absent`; no placeholder entries for future countries are needed.
Only an omitted country selects `vn`. A supplied unknown or disabled code returns a typed error. Public routing returns 404 for unavailable countries, including every MCP tool and website route. User headers cannot override trusted deployment routing.
Writer admission is the intersection of compiled state, owner-approved deployment allowlist, compatible manifest, and the live country admission epoch. Environment variables cannot enable a compiled disabled module.
The scheduler, manual CLI, workflow entry, child-start activity, provider submission, database acceptance and release activation enforce writer admission. Workflow code obtains admission through activities, never mutable registry reads during replay. The read router uses the local contract below.
Every mutating transaction checks the live epoch and fence. Disabling pauses schedules and revokes admission before draining work; stale queued tasks and late provider results cannot commit. A restored control database cannot revive old admission.
Serving also requires a verified active release and matching recipe, schema and runtime. Default configuration and a registered module never establish release readiness.

### Local read admission

Keep a Vietnam-local admission view in the [local control database](corpus-database.md#topology-decision), outside immutable corpus snapshots. The writer remains the authority for country admission and desired selection.
Each record binds country, admission epoch, allowed/revoked state, module/policy digests, ordered control sequence, predecessor digest, activation version and receipt digest. Keep a separate local emergency deny and restore fence; either can only restrict reads.
The writer emits authenticated ordered epoch/revocation records through a durable outbox. A background reconciler verifies issuer, country, digests and contiguous sequence, applies each record atomically to local control and acknowledges the applied sequence. Duplicate records are idempotent; stale records cannot reopen admission. A sequence gap closes admission until reconciled.
Bind activation records to their admission epoch. Apply an activation only after its epoch record and verified release are present; reject activation under a revoked epoch. A later enablement requires a new writer-authorized epoch and current readiness checks.
Every website and MCP request checks compiled state, local deployment allowlist, compatible manifest, the local admission view and restore fence before acquiring a release pin. Requests never contact the writer or send questions during reconciliation.
Revocation closes new reads and cancels in-flight requests and streams; check the epoch again before emitting evidence. Already delivered bytes cannot be recalled. Reopening cannot reuse revoked pins.
An uninterrupted, reconciled host may serve its last verified release while the writer is unavailable. Remote revocation takes effect when durably applied locally; disconnected delivery cannot promise immediate revocation. A Vietnam-local emergency deny closes admission without the writer and remains journaled until explicitly cleared under current authority.
Keep acknowledged control sequences, revocations and local deny records in a Vietnam recovery journal independent of the restored database. Any restore closes traffic before starting readers, regardless of restored `allowed` state.
Reconcile journal continuity, the latest writer admission and ordered activation records before clearing the restore fence, as [hosting recovery](vngcloud-hosting.md#accounts-backups-and-restore) requires. Missing continuity or an unavailable writer keeps restored serving closed. Reconciliation stays outside requests; restoring a release never grants admission.

## Country data and language

Under `internal/country/<code>/`, use `data/seeds/`, `data/dictionaries/`, `data/guide/` and `data/locales/` for reviewed text assets. Use `testdata/goldens/`, `testdata/fixtures/` and `testdata/floors.json` for synthetic fixtures, expected outputs and thresholds.
Large real-document fixtures stay in protected artifact storage. Track hash-pinned manifests under `testdata/real/`, with source URL, version, media type, expected findings and human judgment references. Do not commit raw captures or personal data.
Move existing seeds and goldens without changing their contents first. Compile data through `embed.FS`; the manifest hashes every file. Seed import uses core code and the country database; module construction performs no writes.
Operator changes create a new effective config digest under `config`, with reviewed provenance. Freeze module, policy, dictionaries, source descriptors, taxonomy, conversion and retrieval recipes for each attempt and release. In-flight work never observes a hot dictionary edit.

| Country | Migrated source IDs | Corpus language / OCR hint | Native structure to preserve |
|---|---|---|---|
| `vn` | `congbao`, `vbpl`, `vanban`, `sbv_hanoi` | Vietnamese / `vi` | Phần, Chương, Mục, Điều, Khoản, Điểm; Phụ lục, Biểu mẫu, Mẫu số |
| `my` | `agclom`, `bnm`, `sc` | English / `en` | Act sections, subsections, schedules and BNM policy paragraphs |
| `sg` | `sso`, `mas`, `pdpc`, `csa` | English / `en` | Act parts/divisions/sections and MAS notice paragraphs |
| `id` | `bpk`, `bi`, `ojk`, `ojkweb` | Indonesian / `id` | BAB, Bagian, Paragraf, Pasal, ayat, huruf |
| `th` | `ocs`, `bot`, `etda`, `sec` | Thai / `th` | หมวด, ส่วน, มาตรา, ข้อ; Thai digits and inserted suffixes |
| `kh` | `nbc`, `serc`, `cdc`, `odc` | English / `en` | Article and clause structure; English publications do not become binding Khmer originals |

These are migration inventories from the [country records](../design/jurisdictions/README.md), not new source qualifications. Preserve existing source conditions, including OJK access requirements. Do not activate sources merely because their adapter compiled.
Package names need two explicit mappings: `pkg/ingest/sbvhanoi` becomes `vn/sources/sbvhanoi`, retaining source ID `sbv_hanoi` ([client.go:26](../../pkg/ingest/sbvhanoi/client.go#L26)); `pkg/ingest/cdcgov` becomes `kh/sources/cdcgov`, retaining ID `cdc` ([client.go:23](../../pkg/ingest/cdcgov/client.go#L23)). Other source package names follow the IDs in the table.
Stored legal text stays Unicode NFC with native diacritics and script. Country normalization emits reversible edits; lexical folding creates a separate search projection. Thai combining marks remain intact. No module translates legal text or user questions.
Vietnam accepts Vietnamese questions with diacritics or a whole document number such as `50/2024/TT-NHNN`. Trim/NFC-normalize for validation, then test full number grammar before the language gate. A number inside an English question does not exempt that question.
Reject English and ambiguous unaccented prose before embedding or any corpus query. Version accepted abbreviations, mixed technical tokens and short-query cases in goldens. The gate uses deterministic rules and dictionaries, no language model or external detector. English locale selection changes interface text only.
Keep body, recital and replacement scopes distinct. Vietnam fixtures cover `116/ 2025/QH15`, National Assembly numbers without a hyphenated suffix, bare-date rejection, `theo`, `như sau:`, quotes across pages, omitted source subdivisions and duplicate form labels.
Migration preserves legacy validity behavior in explicit compatibility fixtures. New acceptance must not inherit an undocumented unknown-as-current default. Preserve uncertainty and require evidence for dates and legal effects under the core validity contract.

### Durable identity

Follow [database identity](corpus-database.md#identity-revisions-and-release-membership): `vn:doc:<uuid>` and `vn:doc:<uuid>:rev:<digest>`. Modules parse source numbers; the core identity catalog mints durable keys. Numbers, brands, domains and database names are not durable identity.
Qualify workflow IDs, source observations, blob namespaces, batch manifests, cache keys, review targets, evaluations and releases by country. Blob equality does not merge legal identity across countries.
Provisions and parts use revision-qualified target keys; chunks use a qualified chunk-set identity. Legacy mappings include country, baseline, entity kind and old ID. Ambiguous mappings stay ambiguous.
Keep one writer database per country and one immutable database per serving release. Check every external key against singleton database country metadata. Country-qualified keys do not require a country column on every evidence row.
Subject tags are versioned assignments within that corpus. Adding a subject cannot create another writer, copy a law or change its evidence key.

## V1 reuse map

Current-code references below establish source presence only. They do not assert fresh tests, real-document validation or deployed behavior. Keep means retain useful mechanics; adapt means change the contract; replace means retire the named runtime path after its callers migrate.

| V1 code and evidence | Action | New home and scope |
|---|---|---|
| `pkg/ingest/*`; [source.go:109](../../pkg/ingest/source.go#L109), [app.go:198](../../pkg/app/app.go#L198) | Adapt | Move each source's pure request planner, sandbox decoder and fixtures to its country's `sources/<source>`. Move network execution and shared types to `internal/corpus/source`; replace whole-feed slices with bounded decoded pages. Preserve source IDs, discovery files and provenance. |
| `pkg/fetch`; [fetch.go:40](../../pkg/fetch/fetch.go#L40) | Adapt | Retain transport mechanics in the core fetch activity with policy, pacing and capture receipts. Remove module environment reads, network execution and nested retry loops. |
| `pkg/extract` DOCX/HTML/quality; [docx.go:27](../../pkg/extract/docx.go#L27), [htmltext.go:24](../../pkg/extract/htmltext.go#L24), [extract.go:187](../../pkg/extract/extract.go#L187) | Adapt | `internal/corpus/conversion` adapters and `quality`; retain parsing knowledge and metrics, emit blocks/maps/reports. Move language markers into modules. Flat text is not the new interchange model. |
| `pkg/extract/fitz`, `pkg/extract/docai`; [fitz.go:47](../../pkg/extract/fitz/fitz.go#L47), [docai.go:278](../../pkg/extract/docai/docai.go#L278) | Adapt | Sandboxed MuPDF/LibreOffice adapters and Vision provider under conversion. Retain full Vision responses, geometry and recipe identity. The `docai` path names Vision code. |
| `pkg/extract/easyocr.go`, `sidecar.go`; [easyocr.go:37](../../pkg/extract/easyocr.go#L37), [sidecar.go:33](../../pkg/extract/sidecar.go#L33) | Replace runtime selection | Retain useful fixtures/comparison utilities outside production composition. Vision is the chosen provider; optional OCR experiments are not module defaults or launch gates. |
| `pkg/pipeline` parsers; [legalparse.go:38](../../pkg/pipeline/legalparse.go#L38), [malaysiaparse.go:32](../../pkg/pipeline/malaysiaparse.go#L32), [indonesiaparse.go:28](../../pkg/pipeline/indonesiaparse.go#L28), [singaporeparse.go:22](../../pkg/pipeline/singaporeparse.go#L22), [thailandparse.go:21](../../pkg/pipeline/thailandparse.go#L21), [cambodiaparse.go:20](../../pkg/pipeline/cambodiaparse.go#L20) | Adapt | Respective `grammar` packages, retaining goldens. Extract shared tree builders into the conversion core so Thai/Singapore/Cambodia need not import Malaysia. Wrap legacy text parsers only as a migration comparison. |
| `pkg/pipeline` relation/validity/source logic; [relation_evidence.go:362](../../pkg/pipeline/relation_evidence.go#L362), [resolve_refs.go:70](../../pkg/pipeline/resolve_refs.go#L70), [bnm_supersession.go:57](../../pkg/pipeline/bnm_supersession.go#L57) | Adapt | Country grammar owns number/operator rules and source-tree mapping; core relations/validity owns reconciliation. Preserve weak assertions without copying their old promotion shortcuts. |
| `pkg/pipeline` mutation and batch orchestration; [normalize_activities.go:420](../../pkg/pipeline/normalize_activities.go#L420), [index_activities.go:401](../../pkg/pipeline/index_activities.go#L401), [cmd/pipeline/main.go:528](../../cmd/pipeline/main.go#L528) | Replace orchestration; adapt pure work | `internal/corpus` activities, fenced store adapters and Temporal workflows. Reuse chunk/context computation, hashes and compatible caches. Never invoke legacy destructive/unfenced stages from new workers. |
| `pkg/rag/retrieve`, `pkg/rag/lexical`; [rrf.go:29](../../pkg/rag/retrieve/rrf.go#L29), [bm25.go:48](../../pkg/rag/lexical/bm25.go#L48), [tcc.go:19](../../pkg/rag/lexical/tcc.go#L19) | Keep and adapt | Retain shared retrieval/BM25 mechanics at their paths. Inject module tokenization, number grammar, abbreviations and native labels; move Thai segmentation rules to `th`. Pin release/config recipes. |
| `pkg/rag/embed/*`; [embed.go:24](../../pkg/rag/embed/embed.go#L24), [onnxembed.go:8](../../pkg/rag/embed/onnxembed/onnxembed.go#L8), [embed_script.py:31](../../pkg/rag/embed/sagebatch/embed_script.py#L31) | Adapt; replace bulk provider | Keep query interface/runtime mechanics and recipe-compatible formatting. Core embedding ports use Hugging Face Jobs with Runpod Flex fallback. Retain Kaggle for offline evaluation only; do not relabel SageMaker BGE vectors as Qwen. OpenVINO remains outside the selected runtime unless qualified. |
| `pkg/rag/ocr/kagglebatch`; [kagglebatch.go:1](../../pkg/rag/ocr/kagglebatch/kagglebatch.go#L1) | Replace production orchestration | Reuse bounded staging/receipt ideas under core OCR jobs. No Kaggle OCR production path; experimental helpers stay outside runtime selection. |
| `pkg/mcp`; [brief.go:10](../../pkg/mcp/brief.go#L10), [brief.go:25](../../pkg/mcp/brief.go#L25) | Keep and adapt | Keep five-tool transport. Move guides/tool copy into modules and compose through the evidence service. Replace Vietnam's translation-first copy in the new module only. |
| `pkg/web`; [web.go:29](../../pkg/web/web.go#L29) | Keep and adapt | Shared website shell at `pkg/web`; inject locale data and native citations. Use the same evidence operations as MCP. |
| `pkg/identity`, `pkg/oauthserver`; [manager.go:67](../../pkg/identity/manager.go#L67), [server.go:63](../../pkg/oauthserver/server.go#L63) | Keep | Shared auth packages, separate auth database. Country modules receive no accounts, tokens, private keys or audit identities. |
| `pkg/base/jurisdiction`; [jurisdiction.go:47](../../pkg/base/jurisdiction/jurisdiction.go#L47), [jurisdiction.go:173](../../pkg/base/jurisdiction/jurisdiction.go#L173) | Adapt and replace registry | New manifests and one catalog. Retain v1 compatibility descriptors until legacy callers retire; the new runtime cannot consult a second country registry. |
| `pkg/base/{config,db,dns,log,sourceurl}`; [config.go:32](../../pkg/base/config/config.go#L32), [db.go:25](../../pkg/base/db/db.go#L25), [dns.go:31](../../pkg/base/dns/dns.go#L31), [log.go:12](../../pkg/base/log/log.go#L12), [vbpl.go:11](../../pkg/base/sourceurl/vbpl.go#L11) | Keep and adapt | Retain shared config/pools/logging. Make DNS/egress explicit worker infrastructure. Move pure VBPL URL formatting into `vn`; reader code still imports no crawler. |
| `pkg/store/*`, `sql/*`; [sqlc.yaml:1](../../sqlc.yaml#L1) | Keep and extend | Retain generated layer packages and schema ownership. New persistent adapters use hosted-generated queries. Preserve append-only migrations and separate auth/corpus generation. |
| `internal/corpus/*`; [apply.go:44](../../internal/corpus/run/apply.go#L44), [blob.go:15](../../internal/corpus/blob/blob.go#L15), [workflow.go:24](../../internal/corpus/workflow/temporal/workflow.go#L24), [resolver.go:60](../../internal/corpus/release/resolver.go#L60) | Keep and adapt | Preserve run/blob/release contracts and schema tests. Replace the single-refresh pilot topology with the workflow spec; intake no longer requires a build. Source presence is not a working persistent adapter. |
| `internal/evidence/*`, `internal/app/serve`; [service.go:37](../../internal/evidence/service/service.go#L37), [app.go:15](../../internal/app/serve/app.go#L15) | Keep and adapt | Shared operations, contracts and composition stay. Add module admission and query gates before corpus access. No new-system fallback to a mutable legacy release resolver. |
| `pkg/app`, `cmd/*`; [app.go:202](../../pkg/app/app.go#L202), [cmd/pipeline/main.go:528](../../cmd/pipeline/main.go#L528) | Adapt | New corpus composition and thin start/status/cancel clients. Retain serving, embedder, seed and migration command duties. Legacy tools cannot write the new writer. |
| `pkg/scope`, `pkg/eval`; [scope.go:1](../../pkg/scope/scope.go#L1), [eval.go:109](../../pkg/eval/eval.go#L109) | Keep and adapt | Shared matching/evaluation mechanics; country data and citation matchers move into modules. Keep retrieval and external-agent workflow evaluation separate. |

Retain v1 entry points and adapters only for legacy compatibility while extraction proceeds. Pin existing AWS deployments and refresh tooling to their accepted v1 artifacts; moving source files does not authorize rebuilding or redeploying those services.
Remove compatibility code only after all callers and recovery paths have replacements. Preserve disabled-country code in the new module tree even when the old package path disappears.

## CI and country qualification

All execution runs on GitHub under the [hosted check matrix](../../instructions/verification.md). Missing inputs, skipped required cases and absent runs block their gate. Local design work performs no builds, tests, generators or service calls.

| Check | Enabled Vietnam | Migrated-disabled countries |
|---|---|---|
| Build, vet, short tests, formatting and dependency checks | Required | Required for every module and source factory, without state-based test skips or build tags that hide disabled code |
| Manifest, registry, seed, locale and guide completeness | Required | Required; incomplete new capabilities are explicit findings, not missing constructors |
| Fake-source, parser, citation, number, dictionary, language and validity fixtures | Required | Required; preserve v1 expectations and name known defects separately from new-contract expectations |
| Source fetch/capture and sandbox handoff | Hosted fixtures prove fetch never parses bodies, emits hash-bound captures, and sends listing/detail parsing only to `corpus-converter-v2`; follow-ups retain budgets, request counts/`NextEligible` survive worker loss, `Retry-After` persists, and sandbox network/metadata egress is denied | Same offline contract fixtures for every migrated decoder; fake endpoints belong to the core fetch lane, never the decoder |
| Parts output and ownership | Hosted fixtures prove all six `PartResult` fields survive acceptance; missing attachments, excluded ranges and unresolved references remain visible | Same synthetic contract checks without live intake |
| Import and side-effect checks | Serving imports no crawlers/workers; rules perform no I/O | Constructors have no I/O; denied country creates no pool, source client, schedule, provider job or public route |
| Disposable PostgreSQL, hosted generation and schema upgrades | Required when affected | Shared schema lanes still run; module migration cannot alter the legacy database or seed it implicitly |
| Temporal replay, fences, admission races and sandbox tests | Required for affected core paths | Synthetic rejection tests only: disabled-country starts, children, stale tasks, provider results and promotion all fail before effects |
| Local read admission | Hosted fixtures prove offline verified reads, ordered revocation, duplicate/out-of-order delivery, stale pins and denied reads after stale control restore; an unavailable writer cannot reopen restored serving | Synthetic unavailable-country denial only; no public serving |
| Real-document and new-contract retrieval evaluation | Required for affected behavior and every candidate release | Required for later qualification, not a dependency of Vietnam milestones; no live crawl, runtime pipeline or public server |
| Legacy baseline lane | Compare unchanged v1 goldens against the new core and record per-case results at each milestone; gate only the milestone claiming retrieval parity | Include every affected country through offline compatibility adapters over retained v1 packages or migrated modules; migration is not required to compare baselines |
| Deployment/release smoke | Authorized Vietnam rollout only, after qualification | Forbidden until separate enablement approval |

Offline evaluation uses core library functions, recorded captures and disposable databases, not a disabled country's workflow or serving composition. Fake HTTP servers exercise only core fetching; sandbox decoders receive captured bytes with all network egress denied. A fixture runner cannot become a production admission bypass.
Run unchanged v1 goldens against the new core in a separate legacy baseline lane using frozen legacy routing/preprocessing outside public serving. Record results, per-case differences and unavailable cases with reasons at each milestone, including affected six-country baselines throughout shared changes. Their results gate only the milestone that claims retrieval parity; they never require early migration of five modules. Other required build, unit, contract and integrity checks still gate their affected milestones.
The public-search lane applies the versioned Vietnamese gate and frozen expected gate-accepted cohort under [Vietnam evaluation](vietnam.md#evaluation-and-first-milestones). Report lane, routing policy, cases, exclusions and denominators separately. Language rejection cannot hide a retrieval failure or change the unchanged legacy suite.
Keep existing floors from [Makefile:68](../../Makefile#L68): recall / mean reciprocal rank / current-law precision / abstention are `vn .90/.66/.99/.95`, `my .92/.77/.99/.95`, `id .78/.60/.99/.98`, `sg .90/.75/.99/.90`, `th .86/.68/.99/.90`, `kh .90/.70/.99/.95`.
Move floors into the module data with a hosted parity check against the old commands, then make commands consume that single source. Freeze accepted goldens and snapshots. Never lower floors or change expected citations to make migration pass.
The conversion and parts hard gates remain additional requirements. A good retrieval score cannot hide wrong citations, omitted tables/forms, false binding authority or unsupported validity. Record exact commit, recipe, fixture hashes, case counts and per-case results.

### Enabling a migrated country later

1. Obtain owner approval for that country and scope. Inventory every source, language, authority exception, known defect and unsupported format. Keep the existing AWS service outside the change.
2. Finish new-contract capabilities using recorded fixtures. Qualify source access and politeness through separately authorized protected probes; a module probe is not a production workflow. Preserve raw captures and unresolved source gaps.
3. Have human reviewers check representative real documents across every admitted source/type/format, body and parts, amendment targets, dates, duplicate numbers and language edge cases. Meet conversion/parts hard gates and preserve exact native citations.
4. Reproduce the accepted frozen retrieval baseline and satisfy unchanged country floors. Evaluate changed recipes and new cases separately. Show direct provision hits, validity negatives and known gaps; obtain independent review of mutations and admission controls.
5. Produce a qualification manifest with module/config digests and exact hosted evidence. An isolated offline release-fixture harness may exercise publication/restore/read contracts with synthetic control state; it never activates a country or writes production state.
6. Review the compiled state change to `enabled` and provision an isolated country writer only under separate authorization. Keep production admission, schedules and public routing closed. Enablement is eligibility, not proof of a published corpus.
7. Admit bounded intake/build work through the core, create a candidate release and pass full release gates. Verify database closure, accepted revisions, parts, citations, vectors, runtime parity, delivery, rollback and restore fencing. Keep public routing closed until owner promotion and destination readiness.
8. Open public routing and source schedules only for the approved release and policy. Verify all five MCP tools, website language gates and country isolation. A failed gate leaves routing closed; return to disabled state by revoking admission and draining safely.

No rollout here moves or stops an existing AWS service. Any later cutover requires its own owner decision and evidence.

## Migration milestones

Each milestone is additive until its replacement passes the exact commit's required hosted checks. Keep compatibility wrappers out of new write paths. Record the separate legacy baseline lane at each milestone; only a retrieval-parity claim makes its results a gate. Missing evidence for a required gate blocks that milestone.

| Milestone | Deliverable | Evidence before dependent work |
|---|---|---|
| 1. Freeze v1 contracts | Capture package/source inventory, six seed/golden/floor manifests and accepted artifact references. Introduce neutral contracts and a side-effect-free registry. Compile the core, Vietnam module and retained v1 packages; no other new country module is required. | Existing required CI plus registry/import tests. Record baseline hashes, results and gaps in the separate legacy baseline lane; no retrieval-parity claim. |
| 2. Extract Vietnam rules | Move VN sources, number/structure/part/relation rules, dictionaries and guide into `vn`; introduce core helpers where reused. Keep VN disabled in the new registry. | Fixture parity, native citations, query rejection, source-policy bounds and compatibility imports. |
| 3. Build the Vietnam core path | Implement sibling database, conversion, intake, OCR, embedding, review API and release contracts using adapted v1 mechanics. Run Vietnam's write path end to end on GitHub-hosted CI. | Hosted generation, isolated persistence/fence/replay tests, sandbox handoffs and protected real-document evaluation. No v1 mutation calls in new workers. This exit permits milestone 5 to start. |
| 4. Qualify Vietnam | Complete Vietnam enablement, candidate release, retrieval parity and serving readiness gates. Build the dedicated deployment only under its own authority. | Gate the Vietnam retrieval-parity claim on the unchanged legacy baseline suite and public-search lane, as Vietnam milestone 5 requires. Require unchanged floors, new hard cases, matched single runtime, rollback/restore proof, independent review and owner promotion. |
| 5. Migrate five disabled modules | After milestone 3, move MY, SG, ID, TH and KH sources, grammars, data and tests, one country per slice. This branch does not block milestone 4 or any Vietnam milestone. Extract shared helpers without cross-country imports. | Full builds and offline unit/fixture parity for each slice. Require all six new modules only at this milestone's exit; all five other countries remain denied at every runtime boundary. Existing AWS artifacts stay pinned. |
| 6. Retire duplicate new-system wiring | Remove replaced registry switches and unused v1 wrappers once caller/recovery audits permit. Keep every disabled module compiled. | Import graph, recorded affected six-country baseline comparisons, admission negatives and exact required CI. Retrieval parity gates removal of any compatibility path whose replacement claims parity. No live country activation. |

The milestone numbers are local to each spec. This file's milestone 3 supplies the hosted end-to-end write-path evidence in [Vietnam milestone 5](vietnam.md#evaluation-and-first-milestones). This file's milestone 4 completes Vietnam's milestone 5 candidate-release parity and milestone 6 serving gates. This file's milestone 5 depends on the hosted end-to-end evidence; Vietnam never depends on the five other module migrations.

## Owner decisions and unresolved gates

The owner approved all three recommendations:

1. Keep the established domain paths; add `internal/core/country` and `internal/country/<code>` only.
2. The enablement contract is the compiled state, deployment allowlist, writer admission epoch and Vietnam-local admission view, with ordered revocation, local emergency denial and restore fencing.
3. Disabled countries use the offline compatibility harness: core library calls over frozen snapshots, with v1 adapters retained until migration.

Unresolved: protected fixture/snapshot availability; measured source schedules and fetch budgets; qualified query-language edge cases; new parts/validity gaps in migrated modules; persistent adapter and hosted-lane completion owned by sibling specs.
Legacy source defects and compatibility defaults remain named qualification gaps, not a v1 repair backlog or a claim of v2 readiness. Threshold and authority questions stay with the spec that owns them.
The paired implementation plan, documentation index updates and shared-rule reconciliation belong to the coordinating slice. This task writes only this spec. No independent review, hosted execution, real-document validation or deployment is claimed.
