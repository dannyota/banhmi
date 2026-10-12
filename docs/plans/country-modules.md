# Country modules implementation plan

Status: F1-F3 inventory-only foundation is merged and its required hosted checks pass.
M0 acceptance and later country migration remain open.
Paired spec: [Country modules](../specs/country-modules.md).
Canonical Vietnam plan: [Vietnam rebuild](vietnam.md).

## Goal and authority

Build the shared country seam with Vietnam, then migrate Malaysia, Singapore, Indonesia, Thailand, and Cambodia as compiled, offline-tested modules in state `migrated-disabled`. This plan owns country-module tasks, status, and evidence. The paired spec owns behavior and qualification contracts. The Vietnam plan owns the canonical M0-M6 order.

O1-O3 approve the package boundaries, admission contract, and offline compatibility harness. Foundation source implements only part of those decisions; see [progress](#evidence-and-progress). Vietnam is the only country eligible to reach `enabled` under this plan. Shared Vietnam work targets isolated VNGCloud writer and worker capacity under the Vietnam plan. No D task enables another country, admits evidence for it, runs its workflow, changes an existing AWS service, or delays Vietnam.

## Shared work mapped to Vietnam M0-M3

The shared core and Vietnam work executes only through the canonical tasks in the [Vietnam plan](vietnam.md). This table records the country-module dependency and adds no second sequence or acceptance gate.

| Canonical milestone | Country-module work supplied there | Country-module proof carried by that milestone |
|---|---|---|
| M0 | Neutral country contracts, side-effect-free registry, Vietnam skeleton, six-country v1 inventory, frozen seeds, goldens, floors, and snapshot manifests | Registry/import denial before I/O, manifest hashes, existing CI, and recorded legacy baseline gaps. Other new modules remain `absent`. |
| M1 | Shared persistence, workflow composition, VNGCloud capture and converter boundaries, conversion contracts, and pure Vietnam source planners/decoders | Hosted generation, source/capture contracts, converter sandbox denial, retry ownership, and no v1 mutation calls. |
| M2 | Vietnam body, part, identity, relation, validity, subject, language, and qualification rules on protected real evidence | Exact native citations, part ownership, legal targets and dates, source coverage, privacy, and named gaps. Other-country migration remains out of scope. |
| M3 | Shared release, retrieval, evaluation, publication, admission, and isolated VNGCloud writer boundaries with the Vietnam write path run end to end | Exact-commit B/C/G/I/Q/R/L/P/V evidence, including credential-free writer infrastructure contracts, unchanged Vietnam floors, release closure, rollback, and independent review. M3 exit permits D to start. |

M3 may automatically accept only clean first-pass Vietnam revisions that pass inherited deterministic gates. D needs M3 hosted end-to-end evidence because its modules remain disabled and cannot accept evidence or run workflows. D does not depend on the M5 review API. Disputed evidence, reprocessing, accepted gaps, and legal-meaning corrections await M5 human approval under the Vietnam plan.

## M0 foundation subplan

Scope: M0.1-M0.2 under the approved [layout](../design/repository-layout.md#delivery-and-agent-ownership).
This section refines those tasks; it adds no milestone or enablement decision.
Keep old callers, routing, parser selection, embedded loaders, goldens, and Makefile floors unchanged.
Do not implement M0.3-M0.4, M1 processing, source factories, deployments, or other-country modules here.
Use Go with the existing module and standard library. No dependency additions are needed.

### Staged construction

Use the governing spec's distinction between a partial declaration and a runtime entry.
M0 compiles a Vietnam declaration with real asset metadata and explicit pending capabilities, but no
runtime entry. Runtime VN is therefore `absent`, not `migrated-disabled`; all runtime admission fails.
The three states remain unchanged. `migrated-disabled` still requires a complete migration, mandatory
rules, data and factories. A partial or forged non-nil entry fails construction rather than silently
becoming absent. Missing parsers never return successful output, and empty factories never imply
completeness. M1-M2 offline tests call completed pure components directly without runtime admission.

### Ownership and dependencies

The primary assigns separate contracts/registry and country/assets worktrees under `.worktrees/`.
Name the contracts worker as the foundation integrator in both briefs. That worker alone owns catalog,
shared APIs, and shared configuration. Configuration changes are unnecessary in this slice.
The country worker owns only Vietnam assets and their tests. It never edits catalog registration.
The reviewer has separate read-only ownership; the author cannot review its own slice.
At most four jobs run, including review. The primary handles Git and inspects each handoff before
dependent work. Workers never commit, push, deploy, read credentials, or change cloud resources.

| Task | Owner and exact files | Dependency and handoff |
|---|---|---|
| F1 Neutral asset contracts | Integrator: `internal/corpus/model/{assets,assets_test}.go`; `internal/core/country/{manifest,manifest_test}.go` | Primary inspects signatures and manifest encoding before the frozen F2 handoff. No country or runtime imports. |
| F2 Versioned Vietnam assets and six-country inventory | Country worker: `internal/country/vn/{assets,assets_test}.go`, `data/manifest.json`, listed data copies, `testdata/{baseline-manifest,floors}.json`, listed golden copies | Depends on inspected F1. Hand embedded assets, computed digests, provenance, missing snapshots, and static diff to integrator. |
| F3 Registry, catalog, and partial descriptor | Integrator: `internal/core/country/{registry,registry_test,errors,errors_test}.go`; `internal/country/catalog/{catalog,catalog_test}.go`; `internal/country/vn/{entry,entry_test}.go` | Registry/errors depend on inspected F1 and may run beside F2. Catalog/VN integration waits for inspected F2. Freeze signatures before implementation. |

No consumer API file is owned yet. Do not create block, target, relation, validity, planner, or decoder types until
an actual consumer in its owning milestone needs them. M0 has no authorization to implement their
algorithms. F2 inventory records are data, never another runtime country catalog.

### F1 Neutral asset contracts

Use these exact initial types in `internal/corpus/model/assets.go`:

```go
type Digest string // Exactly 64 lowercase hexadecimal SHA-256 characters.
type AssetRef struct {
    Path string // Slash-separated relative path; no absolute path or traversal.
    SHA256 Digest
    ByteSize int64
}
func (d Digest) Validate() error
func (a AssetRef) Validate() error
```

Use these types in `internal/core/country/manifest.go`; references are `model.AssetRef`:

```go
type Code string
type State string // Only enabled, migrated-disabled, absent.
const (
    Enabled State = "enabled"
    MigratedDisabled State = "migrated-disabled"
    Absent State = "absent"
)
type SourceDescriptor struct { ID, Package, CorpusLanguage, BindingLanguage string }
type Capability struct { Name, Version, MissingReason string }
type Manifest struct {
    Code Code
    ModuleVersion, ContractVersion string
    Digest model.Digest
    CorpusLanguage, BindingLanguage, Timezone string
    OCRHints []string
    Sources []SourceDescriptor
    Capabilities []Capability
    Assets []model.AssetRef
}
type Descriptor struct { Manifest Manifest }
func (m Manifest) Validate() error
func (m Manifest) Clone() Manifest
func (m Manifest) ContentDigest() (model.Digest, error)
```

`Validate` checks structural integrity, not legal qualification or runtime readiness.
Reject duplicate paths, source IDs and capability names, malformed references, unknown states when
used in entries, and empty required identity/version/language fields. Missing capabilities have an
empty implementation version and a nonempty reason; reject a simultaneous version and missing reason.
This representation records incompleteness but grants no runtime-entry validation exception.
Missing assets have no fabricated references or hashes. `Declare().Descriptor.Manifest` and
`AssetManifest()` return the same frozen metadata; its digest identifies declared content only.

Define digest encoding version `1`: SHA-256 of compact JSON for the manifest with `Digest` omitted.
Use declared struct field order and sorted copies of sources by ID, capabilities by name, assets by
path, and OCR hints lexically. JSON uses exported field names with no version envelope; normalize nil
and empty lists to `[]`. Reject duplicates rather than deduplicating. Reject invalid UTF-8 and retain
all valid string bytes. No wall clock, map iteration, environment, or machine paths enter the digest.
`Validate` rejects a supplied digest unequal to `ContentDigest`. Clone all slices on input/output;
future map fields require deep copies. Existing blob/run digest types keep their current APIs.

- [x] Author `TestDigestValidation`, `TestAssetPathValidation`, `TestManifestDigestStable`,
  `TestManifestDigestChangesWithContent`, and `TestManifestCloneIndependent` first.
- [x] Assert empty/uppercase/nonhex digests, negative sizes, traversal, absolute and backslash paths
  fail. Zero-byte assets remain valid. Reordering accepted list input preserves the digest.
- [x] Assert changed version, source, capability, asset hash or size changes the digest; altered
  supplied digests fail. Mutating clone slices cannot change the original manifest.
- [x] Implement F1. The primary inspects the diff and freezes contracts before the F2 handoff.

### F2 Versioned assets and baseline inventory

Create these byte-identical copies from `deploy/seed/` under Vietnam `data/`:
`seeds/scope_term.csv` and
`dictionaries/{abbreviation_expand_vn,diacritic_restore_vn}.csv`.
Diacritic restoration is legacy comparison data only; never load it into a new query path.
Inventory shared issuer, discovery, relation, validity and runtime setting seeds at their existing
paths. Do not copy those shared policies as Vietnam-owned assets.
Copy `deploy/eval/golden.json` and `workflow_golden_vn.json` to
`testdata/goldens/{golden,workflow-golden-vn}.json` unchanged.
Keep legacy paths authoritative for old commands; duplication ends only in a later loader slice.
No guide/locales copy exists as reviewed standalone data. Record those capabilities as missing;
do not promote translation-first legacy copy into a new-system guide.

`data/manifest.json` uses schema version `1`, module version `m0-foundation-1`, country `vn`,
and entries with destination path, original repository path, byte size, SHA-256, and purpose.
Hash the exact copied bytes, without newline conversion. Include all copied data and golden files
in `Manifest.Assets`; store manifest metadata separately to avoid a self-hash cycle.
Use a private `embed.FS` containing the reviewed files, never an exported mutable filesystem handle.
Expose `AssetManifest() country.Manifest` and `Asset(path string) ([]byte, error)` in `assets.go`.
Return deep copies and reject paths outside the listed assets; assembly performs no filesystem I/O.
Private embedded bytes may be hashed and validated in memory, without registration or `init` effects.

`testdata/floors.json` contains `recall=.90`, `mrr=.66`, `current_law_precision=.99`,
and `abstention=.95`, with provenance `Makefile` target `eval-vn`.
`testdata/baseline-manifest.json` contains schema version `1` and one record per six-country code.
Each record names legacy registry/parser/source paths, scope seed, golden path, unchanged floors,
and snapshot availability. Hash only the explicitly named, tracked seed/golden/fixture inputs.
Use `golden.json` for VN and `golden_{my,id,sg,th,kh}.json` for the other countries.
The floor vectors are those in [exit evidence](#exit-evidence); keep each metric name explicit.
Record `sbvhanoi` to `sbv_hanoi` and `cdcgov` to `cdc` as compatibility mappings.
New direct `sbv` stays unqualified and unbound.

Snapshot records contain availability, reason, and optional protected reference/digest supplied through
an approved handoff. Without that handoff, record `missing` and no hash/reference. Do not search
personal directories, tool credentials, production databases, or cloud storage to fill the inventory.
A golden file is not a database snapshot. Baseline routing uses retained legacy adapters, including
legacy fallback and validity defaults; new query-gate expectations belong to a separate later suite.

- [x] Author `TestEmbeddedAssetsMatchManifest`, `TestAssetReturnsCopy`,
  `TestLegacyCopiesUnchanged`, `TestBaselineInventorySixCountries`, and `TestLegacyFloorsUnchanged`.
- [x] Assert hashes/sizes match every declared byte sequence; reject undeclared, missing, duplicate,
  traversal and corrupt assets. A caller's byte mutation cannot alter subsequent reads.
- [x] Assert six unique inventory codes, exact seed/golden paths, unchanged floor vectors, explicit
  missing snapshots, and no retrieval score or parity claim without a snapshot result.
- [x] Copy reviewed assets, write manifests, and inspect the diff. Keep all old files unchanged.
- [x] The primary inspects F2; hand assets to the integrator with every unavailable input named.

### F3 Registry integration

Pin the declaration types before implementation. Do not implement a runtime `vn.Entry()` or
`Factories()` in M0. Never use `any`, parser-name switches, or empty placeholder types.
Use this closed construction-stage API in `registry.go`:

```go
type Entry struct { Code Code; State State; Manifest Manifest }
type Declaration struct { Code Code; Descriptor Descriptor; Entry *Entry }
type Reader interface {
    Lookup(Code) (Entry, error)
    Entries() []Entry
    RequireEnabled(Code) (Entry, error)
}
type Registry interface { Reader; Freeze() error }
func NewRegistry([]Declaration) (Registry, error)
```

Reject every non-nil entry with typed `ErrEntryIncomplete`, including forged enabled and disabled
headers. M0 supplies no `RuleBundle`, `BindSources`, `source.Factory`, or source/grammar interfaces.
M1 source tasks own factory binding; M1-M2 consumer tasks own typed reader/writer bundles.
Those tasks replace blanket rejection with full completeness validation under independent review.
F1/F3's metadata-only deliverable satisfies revised M0.1 after its required hosted and adversarial
evidence passes. Full consumer bundles and factory binding are not M0.1 completion requirements.
Serving receives only `Reader`; construction owns `Registry`. Neither surface constructs a source.

`catalog.Declarations() []country.Declaration` is the only compiled inventory;
`vn.Declare() country.Declaration` returns independent metadata with a nil entry.
Nil-entry declarations are excluded from runtime `Entries()`. Lookup and admission return
`ErrCountryAbsent` for that code. Descriptor metadata has no runtime state, rules or factories.
Catalog calls no planner/decoder constructor. No `init` registration, pools, network, environment
reads, timers or workflow registration occurs in assembly.
Other absent countries have no module roots or fake declarations. Unknown codes return typed
`ErrCountryAbsent`. Define typed `ErrCountryDisabled` for the future complete-entry admission path;
M0 rejects supplied disabled headers with `ErrEntryIncomplete` during construction.
Use `errors.Is`/`errors.As` and preserve the rejected code. Only an omitted input chooses VN;
unknown explicit input cannot fall back. No existing `jurisdiction.Lookup`, `For`, or callers change.
In `errors.go`, define sentinel errors `ErrCountryAbsent`, `ErrCountryDisabled`, and
`ErrEntryIncomplete`; wrap them in `CountryError{Code Code, Cause error}` with
`Error() string` and `Unwrap() error`. Never collapse construction failure into absence.

Freeze locks declaration snapshots and never upgrades them. Repeated Freeze is idempotent; registry
input mutation cannot change the snapshot before or after Freeze. No deployment option can enable
incomplete VN. Later binding must validate declared source IDs, duplicates, country agreement and
coverage without calling constructors; it must reject mutation after Freeze. Record that gate as
deferred, without fake binding APIs or successful tests of missing code.

- [x] Author registry cases for invalid descriptors, rejected non-nil entry headers, typed absence,
  unknown explicit code and omitted VN. Guard pure assembly imports and `init` functions.
- [x] Author freeze cases for idempotence, input/output defensive copies, unchanged absence after
  Freeze, and reader view excluding Freeze. Full binding coverage remains a later test gate.
- [x] Author catalog cases proving one VN declaration, zero runtime entries, all six absent,
  default VN denied, partial/forged non-nil entry rejected, duplicate/mismatched codes rejected,
  unsupported declaration code rejected, no assembly effects,
  and reader imports excluding workers/crawlers. Existing routing/parser tests stay unchanged.
- [x] Implement after frozen contract handoff. Obtain one independent review of the integrated
  foundation, including admission, and independent fix confirmation before acceptance.
  Never count the worker's own inspection as review.

### Evidence and progress

F1-F3 are merged through [PR #8](https://github.com/dannyota/banhmi/pull/8). The required hosted
checks pass, including ordinary build/vet/short tests and the three security jobs. Remaining C lanes
and M0.3-M0.4 work still own final M0 acceptance;
F1-F3 do not complete whole M0 while M0.3-M0.4 and their hosted/settings gates remain pending.
No generation G is needed for these pure files. Full M0 exit evidence remains in the Vietnam plan.

Record the exact commit, workflow run, inputs and scope after hosted execution. Keep the integrated
independent review receipt and each task's open findings. Real snapshots and L comparisons remain missing
until supplied and run. M0 makes no legal-validation, retrieval-parity, deployment, or enablement claim.
Recording unavailable snapshots with reasons satisfies F2 and M0.2 inventory requirements;
snapshot availability is not an inventory completion gate.

| Foundation task | State | Evidence or remaining gate |
|---|---|---|
| F1 Neutral asset contracts | done | [Source and tests](../../internal/core/country/manifest.go) reviewed; required hosted checks below pass |
| F2 Versioned assets and inventory | done | [Assets](../../internal/country/vn/assets.go) and [inventory](../../internal/country/vn/testdata/baseline-manifest.json) reviewed; five copy hashes match; six missing snapshots explicitly recorded; hosted checks pass |
| F3 Registry integration | done | [Registry](../../internal/core/country/registry.go) and [catalog](../../internal/country/catalog/catalog.go) reviewed; admission and pure assembly tests pass in hosted CI |

The source review covered `abed75165fec6c279b826876066c401e2a132597`, including ownership, API
handoffs, unchanged legacy paths, copy hashes, and admission boundaries.
[PR run 38236753352](https://github.com/dannyota/banhmi/actions/runs/38236753352), attempt 1,
passed all eight jobs and both aggregates at head `7654108c8d981a538f0b01e90435831f848be78d`,
tested merge `01843a3aaa4252bb4362928734a34746991946c6`; PR #8 landed as
`11569dee65171279ae30fc8088194a7421ccca35`.
[Master run 38237880056](https://github.com/dannyota/banhmi/actions/runs/38237880056), attempt 1,
also passed all required jobs at `dfa35280fc71cd4af61d0e46f0ac4630c41b004c` after PR #9.
Inputs were the committed manifests/assets and synthetic tests, with disposable auth databases.
No local execution, protected snapshot comparison, runtime admission, or cloud change occurred.
Independent read-only review covered F1-F3, digest encoding, asset integrity, six-country inventory,
test boundaries, and adversarial admission cases. The review found no defects or required source fixes.
The plan review has no open findings. This completes the bounded foundation, not whole M0.

## Status

Update this table in D's integration change. Use only `not started`, `in progress`, or `done` with an evidence link.

| Task | State | Depends on | Evidence |
|---|---|---|---|
| D.1 Five disabled-country slices | not started | Vietnam M3 hosted end-to-end evidence | None |
| D.2 Admission and compatibility audit | not started | D.1 handoffs | None |

## D Disabled-country migration

Goal: Port five v1 modules after M3 hosted end-to-end evidence without opening their new runtime or delaying Vietnam.

1. **D.1 Five disjoint country slices.** Own `internal/country/{my,sg,id,th,kh}/`, one country per worker. Serialize `catalog` edits through the integrator.
   Reuse the full [country-modules reuse map](../specs/country-modules.md#v1-reuse-map): source packages in `pkg/ingest/`, country parsers [Malaysia](../../pkg/pipeline/malaysiaparse.go#L32), [Singapore](../../pkg/pipeline/singaporeparse.go#L22), [Indonesia](../../pkg/pipeline/indonesiaparse.go#L28), [Thailand](../../pkg/pipeline/thailandparse.go#L21), [Cambodia](../../pkg/pipeline/cambodiaparse.go#L20), and unchanged per-country seeds, goldens, and floors.
   Write pure roots, grammar, planners and sandbox decoders, source factories, locale and data manifests, and offline compatibility tests. Preserve the `cdcgov` package to `cdc` source mapping, source access conditions, native citations, languages, authority exceptions, and explicit capability gaps.
   All five entries finish as `migrated-disabled`. They never instantiate live sources, create pools, schedule or start workflows, submit provider jobs, promote releases, or expose routes or tools.
2. **D.2 Admission and compatibility audit.** Own new country contract fixtures and catalog integration after D.1 handoffs. Reuse the [v1 registry](../../pkg/base/jurisdiction/jurisdiction.go#L47) only in offline compatibility adapters.
   Write import, factory, and data completeness checks, shared helper extraction without cross-country imports, and disabled admission at CLI, workflow, child, provider, SQL, promotion, and read boundaries. Keep all six modules compiled and unit-tested without state-based skips.
   Remove wrappers only after caller and recovery audits. Any replacement that claims retrieval parity needs its own unchanged-floor gate.

Run at most four Codex jobs across this plan and the Vietnam plan. Use one country per D.1 worker within available slots. Reduce D concurrency first when Vietnam needs capacity. The integrator alone edits the catalog and shared helpers after inspecting country handoffs. Vietnam never waits for D reports or integration.

## Exit evidence

Require B/C and `vietnam-intake.yml` jobs `source-contracts` and `converter-sandbox` green for migrated decoders, using the exact hosted-check definitions in the [Vietnam plan](vietnam.md#hosted-evidence-contract). Offline tests prove source split, parser and citation contracts, no constructor effects, and denial of disabled starts, queued results, and routes before I/O.

Record L comparisons and unavailable cases for every affected country. L results do not gate D unless a replacement explicitly claims retrieval parity. Such a claim must meet the unchanged country floors from the spec: `vn .90/.66/.99/.95`, `my .92/.77/.99/.95`, `id .78/.60/.99/.98`, `sg .90/.75/.99/.90`, `th .86/.68/.99/.90`, and `kh .90/.70/.99/.95` for recall, mean reciprocal rank, current-law precision, and abstention. Never lower a floor or change an expected citation to pass migration.

Real-document evidence uses frozen v1 snapshots only. New-source qualification, new-format quality, and actual enablement require a separate country plan and explicit owner approval. Keep known v1 defects distinct from new-contract expectations. Record protected fixture or snapshot gaps, source access conditions, unsupported formats, new parts or validity gaps, persistent adapter gaps, and unavailable hosted lanes.

Independent adversarial review covers disabled-country admission and every touched shared mutation boundary. O1-O3 are settled. No D task crawls, provisions a writer, runs a country workflow, seeds a live database, exposes a route, changes an AWS service, or changes an existing country hostname or deployment.

