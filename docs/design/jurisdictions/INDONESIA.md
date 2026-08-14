# Indonesia jurisdiction (rendang)

**Status: LIVE at `rendang.danny.vn/mcp`; revived 2026-07-12.** The production corpus uses `bpk`,
`bi`, and `ojkweb`. It also uses `ojk` when `BANHMI_OJK_PROXY_URL` is set. The corpus was first
deployed on 2026-07-06, temporarily decommissioned on 2026-07-11, then rebuilt from source after the
revival.

## Country contract

| Item | Value |
|---|---|
| Jurisdiction code | `id` |
| Codename | `rendang` |
| Database | `rendang` |
| Corpus language | Indonesian |
| OCR language | `id` |
| Structure parser | `id-uu` → `ParseIndonesianUU` |
| Article and clause labels | `Pasal`, `ayat`, `huruf` |
| Long-leaf split label | `Alinea` |
| Scope seed | `deploy/seed/scope_term_id.csv` |
| Golden set | `deploy/eval/golden_id.json` |

The corpus stores Indonesian source text. English renditions published by OJK or BI are not indexed.
OJK supervises financial services, including banks; Bank Indonesia is the primary source for payment-
system regulation.

## Current source set

`buildIDSources` in `pkg/app/app.go` defines the runtime set.

| Source | Coverage | Discovery and detail contract | Access and runtime rule |
|---|---|---|---|
| `bpk` | 13 BPK `jenis` codes across 12 families: POJK, SEOJK, PBI, BSSN, LPS, PPATK, UU, PP, Perpres, PMK, Kominfo, and Komdigi | Sweeps `/Search?jenis=<code>&p=<page>`; detail pages supply dates, status, issuer, files, judicial-review notes, and typed `STATUS PERATURAN` relations | Cloudflare session minted by `CloudflareMinter`; always registered |
| `bi` | PBI and PADG only | Reads one full `/Web/DaftarPeraturan` HTML listing; fetches metadata from `/api/WebJDIH/GetDataWebPeraturan`; downloads through `/api/WebJDIH/DownloadFilePeraturan/<id>` | Direct Chrome-like transport; optional `BANHMI_BI_PROXY_URL`; always registered |
| `ojk` | POJK, SEOJK, and UU rows in OJK JDIH | DataTables-style listing POST; server-rendered detail pages supply status, relations, and per-file UUIDs | Registered only when `BANHMI_OJK_PROXY_URL` is set |
| `ojkweb` | Ten OJK website categories, including POJK, SEOJK, UU, PADK, PP, PMK, PPBI, SEBI, and Bapepam classifications | SharePoint form POSTs with ViewState pagination; detail pages supply dates, sector metadata, and PDF links | Always registered; direct by default, using `BANHMI_OJK_PROXY_URL` when set |

The four sources do not store identical `doc_type` and `doc_number` strings in Bronze. Silver identity
normalizes verbose Indonesian type names to short codes and folds common number forms, such as
`Peraturan Otoritas Jasa Keuangan Nomor 21 Tahun 2023` to `POJK 21/2023`. This makes BPK, BI, OJK,
and OJK website observations converge without claiming their raw metadata is identical.

## Source details

### BPK JDIH (`bpk`)

BPK discovery is sweep-only. It rejects a non-empty keyword because the site silently ignores unknown
filter names and OR-matches multi-word terms. Either behavior can admit an entire general-law listing
when the pipeline expects a server-filtered slice.

The 13 `jenis` values split into two scope rules:

- POJK, SEOJK, PBI, BSSN, LPS, and PPATK numbers carry strong issuer terms and can qualify on document
  identity.
- UU, PP, Perpres, PMK, Kominfo, and Komdigi cover many sectors and must match the Indonesian topical
  vocabulary.

A first run walks every page. Incremental runs request the years from `since.Year()-1` through the
current year. The listing year is only a discovery watermark; detail dates replace it during
normalization. BPK can backfill older years, so a periodic cursor reset is still needed for a full
rescan.

Listing cards and detail pages expose typed status relations. The `bpk` relation-backfill allowlist can
enqueue a relation target when the relation carries its BPK detail ID. `BERLAKU` and `TIDAK BERLAKU`
map to `in_force` and `expired`. Some newer detail pages still lack complete status blocks, so BPK
relations remain enrichment rather than a complete legal history.

### Bank Indonesia JDIH (`bi`)

BI discovery parses the full listing once and keeps only `JenisPeraturanID` 1 (PBI) and 2 (PADG).
SE Ekstern and UU are not part of this source implementation. The listing date is an enactment-date
watermark, not an update timestamp, so an incremental crawl can miss a retroactive metadata edit.

The detail API supplies number, title, issuer, dates, status, and PDF. Only forward `Mengubah` and
`Mencabut` fields become relations; reverse and general-related fields remain raw metadata. The API can
report a repealed document as `Berlaku`, so BI validity can remain wrong even when the relation fields
show a terminal state.

### OJK JDIH (`ojk`)

The OJK listing endpoint pages regulation types `06` (POJK), `09` (SEOJK), and `01` (UU). Detail pages
provide taxonomy, gazette metadata, `Status Peraturan`, relation groups, legal-basis links, and the
download UUIDs for main and attachment PDFs.

The source is conditional in application wiring because `jdih.ojk.go.id` requires the configured
Jakarta proxy in the reference pipeline. Without `BANHMI_OJK_PROXY_URL`, startup logs that `ojk` is
disabled and continues with BPK, BI, and `ojkweb`.

### OJK public website (`ojkweb`)

`ojkweb` is the always-enabled catalogue source. Each regulation type gets its own ASP.NET ViewState
chain, while numbered pages within a pager window run concurrently. The site exposes no reliable
incremental date filter, so every discovery call sweeps the configured categories.

The crawler canonicalizes POJK and SEOJK number forms before normalization. Detail pages add issue and
effective dates, sector and sub-sector text, and PDF links. They do not provide the structured relation
history available from OJK JDIH.

`jdih.komdigi.go.id` and `peraturan.go.id` are not source packages. Kominfo and Komdigi instruments
reach the corpus through BPK; the earlier direct-source spikes remain historical research, not current
runtime dependencies.

## Structure and citations

`ParseIndonesianUU` builds this tree:

`BAB → Bagian → Paragraf → Pasal → ayat → huruf`, plus `Lampiran` and a flat `Penjelasan` node.

The parser does not create an `angka` level. It:

- strips common BPK OCR page stamps and header fragments;
- accepts sequential Pasal numbers and repairs common OCR digit substitutions;
- supports documents without BAB headings and Roman-numbered amendment articles;
- suppresses quoted inner Pasal structure inside amendment instructions;
- parses Lampiran separately; and
- keeps Penjelasan as non-binding structure but excludes it from indexed chunks.

The original production-extractor validation on UU 27/2022 found Pasal 1–76 with no gap or duplicate
and 16 BAB headings. That result validates one flagship law, not every Indonesian layout.

The chunker uses Pasal as the article unit and descends through ayat and huruf. Native citations look
like `Pasal 26, ayat (1), huruf a`; BAB, Bagian, and Paragraf supply context. Lampiran is the appendix
unit. `Alinea N` means a mechanical split of an oversized leaf, not a parsed Indonesian provision
label.

## Validity and relations

| Source | Current behavior |
|---|---|
| `bpk` | Maps `BERLAKU` and `TIDAK BERLAKU`; promotes seeded active and passive status relations. |
| `bi` | Maps `BERLAKU` and `TIDAK BERLAKU`; promotes forward `Mengubah` and `Mencabut`. |
| `ojk` | Collects granular status and relation labels, but only seeded labels promote. |
| `ojkweb` | Supplies dates and files, not structured relations. Unknown validity defaults to `in_force`. |

Indonesia uses `RefCanonIDForms` to resolve verbose relation references against canonical short numbers.
The 2026-07-27 repair also recomputed stale `doc_ref.document_id` pointers after document IDs changed,
removed page-number false positives, and increased resolved relation references without re-indexing the
160,142 chunks.

## Corpus and retrieval status

The current accepted baseline in [`PLAN.md`](../../../PLAN.md#current-state) records:

| Metric | Value |
|---|---:|
| Corpus documents | 2,371 |
| Indexed documents | 2,340 |
| Chunks | 160,142 |
| Golden cases | 110 |
| Recall@8 | 79.8% |
| MRR@8 | 62.4% |
| Current-law precision | 100% |
| Abstention | 100% |

The document and chunk counts were verified live on 2026-08-14. The evaluation values are the
accepted local baseline recorded on 2026-07-25/26. Live counts may move after a corpus rebuild;
`corpus_status` is the runtime source of truth.

Production routes `rendang.danny.vn` to the shared six-jurisdiction MCP container. That process opens a
separate pool to the `rendang` database and uses the shared query-embedder service. The write path still
runs only the Indonesia pipeline and its proxy settings.

## Build history

- **2026-07-04:** Verified BPK and BI, added the Indonesia descriptor and seeds, and coded the Pasal
  parser and initial source set.
- **2026-07-06:** Deployed the first `rendang` corpus.
- **2026-07-11 to 2026-07-12:** Temporarily decommissioned the service, then revived it on the AWS
  read path.
- **2026-07-13:** Replaced BPK keyword slices with the 13-code sweep, added issuer-based scope terms,
  and moved the read path to the shared multi-jurisdiction MCP process.
- **2026-07-14 to 2026-07-16:** Added `ojkweb`, restored proxy-backed OJK JDIH, and aligned
  cross-source number and type identities.
- **2026-07-18 to 2026-07-19:** Rebuilt the corpus with cross-source deduplication and established the
  current 110-case evaluation set.
- **2026-07-27:** Repaired reference resolution, BPK relation parsing, Indonesian reference
  canonicalization, and BPK relation-target backfill.

## Open risks

- **OJK relation mappings:** `deploy/seed/relation_type.csv` lacks the forward OJK labels `Mencabut`,
  `Mengubah`, and `Mencabut sebagian`. Evidence with those labels does not promote to confirmed
  relations.
- **OJK partial validity:** the source preserves `Berlaku (Dicabut Sebagian)`, but the validity seed has
  no OJK-specific partial mapping. It can therefore be treated as current without a partial badge.
- **Proxy-dependent metadata:** without the Jakarta proxy, the richer OJK JDIH source is absent and the
  corpus depends on BPK plus `ojkweb` for OJK instruments.
- **Status completeness:** BPK status blocks can be absent, and BI status can contradict the terminal
  state. Validity is document-level and does not establish clause-level effect.
- **Incremental coverage:** BI can miss retroactive edits, BPK can backfill old years, and `ojkweb` has
  no reliable watermark. Periodic full sweeps remain necessary.
- **Retrieval quality:** the accepted 79.8% recall is lower than the other live corpora. The documented
  misses are mainly ranking failures at the correct document, not a reason to weaken citation-level
  goldens.
