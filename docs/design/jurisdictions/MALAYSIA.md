# Malaysia jurisdiction (laksa)

**Status: LIVE since 2026-06-22 at `laksa.danny.vn/mcp`.** Malaysia was the first country added after
Vietnam and established the shared [jurisdiction playbook](PLAYBOOK.md). The current production
corpus uses three source packages: `agclom`, `bnm`, and `sc`.

## Country contract

| Item | Value |
|---|---|
| Jurisdiction code | `my` |
| Codename | `laksa` |
| Database | `laksa` |
| Corpus language | English |
| OCR language | `en` |
| Structure parser | `my-act` → `ParseMalaysianAct` |
| Article citation | `Section` |
| Long-leaf split label | `Paragraph` |
| Scope seed | `deploy/seed/scope_term_my.csv` |
| Golden set | `deploy/eval/golden_my.json` |

AGC calls its English edition `BI` (*Bahasa Inggeris*). The crawler selects that edition and does not
fetch the BM edition. The corpus stores and searches English source text; banhmi does not translate it.

## Current source contracts

The source list is fixed in `buildMYSources` in `pkg/app/app.go`.

| Source | Discovery and files | Metadata, validity, and relations | Access |
|---|---|---|---|
| `agclom` | Pages through `POST /json-updated-2024.php`; selects the `BI` PDF from `doc2downloadgeneratepdf`; falls back to the current viewer PDF for older Acts | `GET /act-detail.php?act=<id>&lang=BI` supplies dates and a `Repealed by` signal. `POST /json-subsid-2024.php?act=<id>` supplies P.U. (A)/(B) relation targets. | Plain HTTPS with bounded retry; no browser session |
| `bnm` | Sweeps five BNM sector pages and reads direct `/documents/*.pdf` links from listing rows | Listing rows supply title, date, and type badge. There is no implemented per-document detail API or structured status field. Explicit “this policy document supersedes” clauses are parsed during normalization. | AWS WAF token minted in Chrome, then reused by `pkg/fetch.Client` |
| `sc` | Sweeps four technology and digital guideline pages; downloads `api/documentms/download.ashx?id=<GUID>` | The listing is the metadata surface. The crawler does not collect structured validity or supersession data. Duplicate links collapse by normalized title. | Plain HTTPS with bounded retry |

### AGC Laws of Malaysia (`agclom`)

`agclom` is the federal-Act backbone. Discovery emits every principal-Act row and lets the shared
scope matcher decide which titles belong in the corpus. Each document uses `Act <number>` and the AGC
Act ID as its source identity.

The detail parser records publication, royal-assent, and commencement dates when present. It maps an
Act to `REPEALED` only when the page contains `Repealed by`; all other Acts are `PRINCIPAL`. The seed
maps those codes to `expired` and `in_force`.

The subsidiary-legislation endpoint yields `pua` or `pub` edges with a P.U. number and English title.
Those edges promote as `subsidiary_legislation`. The crawler does not discover the P.U. instruments as
documents, so many relation targets remain unresolved.

### Bank Negara Malaysia (`bnm`)

Discovery sweeps these server-rendered pages:

- `/banking-islamic-banking`
- `/payment-systems`
- `/money-services-business`
- `/development-financial-institutions`
- `/digital-currencies`

The source emits a synthetic number, `BNM/<decoded PDF file stem>`, because the listing does not expose
a stable formal number for every policy document. The pipeline scope-matches the title and this number.

The source has no structured status code. The Malaysia descriptor therefore defaults unknown validity
to `in_force`. During normalization, a policy document that explicitly says “this policy document
supersedes” another in-corpus BNM document creates a `replaces` relation and expires the matched
predecessor. Missing clauses, unmatched titles, and documents outside the corpus remain unresolved.

### Securities Commission Malaysia (`sc`)

SC is limited to four sections: technology risk, digital assets, recognized markets, and digital
guidelines. The crawler applies this structural boundary first, then the shared Malaysia scope
vocabulary. It does not crawl the general capital-markets catalogue.

SC repeats the same PDF under different GUIDs. Discovery deduplicates by a normalized title slug and
stores `SC-GL/<TITLE-SLUG>` as the stable synthetic number.

## Structure and citations

`ParseMalaysianAct` is a deterministic parser with three paths:

| Document shape | Parsed hierarchy | Notes |
|---|---|---|
| AGC Act | Part → Chapter or Division → Section → Subsection → Paragraph | Cuts the front contents table at the enacting clause, accepts monotonic section numbers, and keeps Schedules flat. |
| BNM policy document | Part → numbered chapter stored as a section | Detects repeated `S/G N.N` markers. A chapter is cited as `Paragraph N`; appendices are stored as Schedules. |
| BNM letter or circular | Numbered paragraph stored as a section | Fallback for documents with at least three bare numbered paragraphs; otherwise indexing uses full-text fallback. |

AGC PDF marginal notes can share a line with a section number in go-fitz output. The parser splits those
lines before applying the monotonic filter. This restored complete section runs for the validated Act
set, including Acts 758 and 759. It does not recover the marginal note as the section heading.

The chunker uses Section as the article unit, descends through Subsection and Paragraph, adds Part and
Chapter context, and treats Schedule as the appendix unit. Normal citations look like `Section 5, (1),
(a)`. A bracketed ordinal such as `[2]` disambiguates repeated paths that would otherwise produce the
same citation. Mechanically split long leaves use `Paragraph N`.

## Corpus and retrieval status

The current accepted baseline in [`PLAN.md`](../../../PLAN.md#current-state) records:

| Metric | Value |
|---|---:|
| Documents | 109 |
| Chunks | 11,304 |
| Golden cases | 72 |
| Recall@8 | 94.3% |
| MRR@8 | 79.6% |
| Current-law precision | 100% |
| Abstention | 100% |

These are the production corpus counts and accepted local evaluation baseline recorded on
2026-07-25/26. Live counts may move after a corpus rebuild; `corpus_status` is the runtime source of
truth.

The MCP brief is English and names AGC Laws of Malaysia, BNM, and SC. Production routes the laksa
domain to the shared six-jurisdiction MCP container. That process opens a separate pool to the `laksa`
database and uses the shared query-embedder service; it does not run a Malaysia-only container.

## Build history

- **2026-06-21:** Verified the three fetch contracts, added the Malaysia descriptor and seeds, built
  all three source packages, and proved the Act parser on Financial Services Act 2013.
- **2026-06-22:** Built and deployed the first `laksa` corpus.
- **2026-07-04:** Hybrid dense plus BM25 retrieval became the production retrieval mode.
- **2026-07-13:** The read path moved from one container per country to one multi-jurisdiction MCP
  process.
- **2026-07-15 to 2026-07-16:** Fixed go-fitz marginal-note parsing, added BNM policy-document and
  letter parsing, stabilized SC identity, and expanded the corpus and golden set.
- **2026-07-19:** Query embedding moved to the shared `cmd/embedder` service used by all six sites.

## Open risks

- **English versus BM authority:** the corpus consistently selects the AGC English edition, but the
  prescribed or authoritative language can depend on the instrument. That legal determination is not
  encoded per Act.
- **BNM and SC validity:** neither crawler receives a structured current-status field. The default is
  `in_force`; only matched BNM supersession clauses correct a predecessor to `expired`.
- **P.U. targets:** AGC relations identify subsidiary instruments, but `agclom` does not ingest those
  instruments as documents and Malaysia has no relation-target backfill source.
- **Act headings:** numbered structure is validated, but AGC marginal-note section titles are not
  retained by the parser.
- **Issuer metadata:** the Malaysia corpus has no populated issuer facet. Search clients should omit
  an issuer filter until that metadata is backfilled.
