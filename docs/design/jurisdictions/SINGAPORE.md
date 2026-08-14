# Singapore jurisdiction (`kaya`)

**Status: live since 2026-07-17** at `kaya.danny.vn/mcp`. The last recorded source verification was
2026-07-16. This document describes the shipped implementation. Shared rules are in
[`PLAYBOOK.md`](PLAYBOOK.md).

## Current corpus

The accepted production baseline in [`PLAN.md`](../../../PLAN.md#current-state) is:

| Metric | Value |
|---|---:|
| Documents | 292 |
| Chunks | 27,951 |
| Eval cases | 76 |
| Recall | 93.5% |
| Mean reciprocal rank (MRR) | 76.9% |
| In-force accuracy | 100% |
| Abstention accuracy | 98.7% |

The corpus is English-only and covers Singapore banking, financial regulation, and technology law.

## Sources and current contracts

The runtime wires four source IDs in [`pkg/app/app.go`](../../../pkg/app/app.go): `sso`, `mas`,
`pdpc`, and `csa`.

| Source | Discovery | Content path | Current limits |
|---|---|---|---|
| Singapore Statutes Online (`sso`) | A–Z Act pages at `/Browse/Act/Current/{letter}?PageSize=100` | Per-Act PDF at `/Act/{code}?ViewType=Pdf` | Acts only. Subsidiary legislation is not crawled. |
| Monetary Authority of Singapore (`mas`) | Solr `/api/v1/search`, queried separately for `Notices` and `Guidelines` | Detail-page scrape of `/-/media/` PDF links | Circulars are not queried. Some guidance has no parsed document number. |
| Personal Data Protection Commission (`pdpc`) | `/api/listing-api`, topic-filtered to Advisory Guidelines and Sector-Specific Guidelines | Detail-page asset UUIDs mapped to the Optical PDF CDN | Practical guidance and enforcement decisions are excluded by the topic filter. |
| Cyber Security Agency (`csa`) | `sitemap.xml`, filtered to four legislation and publication path prefixes | Isomer CDN PDF links from each page | The publications prefix is broad; the shared scope gate decides what is indexed. |

SSO and MAS use browser-shaped requests. MAS access needs `ChromeTransport` and a browser
User-Agent; no cookie minter is used. PDPC uses its JSON listing API and direct CDN files. CSA uses
ordinary HTTP with a non-empty User-Agent.

### Document identifiers and status

- SSO uses the Act code, such as `PSA2019` or `CA2018`, as `doc_number`.
- MAS recognizes legacy notice numbers and current families such as `FSM-N05`, `PSN01A`, and
  `SFA 04-N13`. It maps `[Cancelled]` in the title to `cancelled`.
- MAS guidance without a recognized number keeps an empty `doc_number`. Retrieval can find it by
  title, but number-based evaluation and document lookup cannot.
- PDPC and CSA usually rely on descriptive titles or source slugs rather than formal numbers.
- The source packages do not currently extract SSO consolidation dates, revised-edition history,
  or MAS supersession relations. Statusless documents follow Singapore's configured
  unknown-validity-as-in-force policy.

## Parser and citations

The `sg-act` parser is [`pkg/pipeline/singaporeparse.go`](../../../pkg/pipeline/singaporeparse.go).
It auto-detects two document shapes:

| Document shape | Parsed hierarchy | Citation examples |
|---|---|---|
| SSO Act | Part → Division → Section → Subsection → Paragraph → Subparagraph | `Part 2, Division 1, Section 47, (1), (a)` |
| MAS Notice or Guideline | Heading → numbered major paragraph → parenthesized items | `Definitions, Paragraph 4, (a)` |

For MAS dot notation such as `4.2`, the parser opens `Paragraph 4` and keeps `4.2` in that node's
content. It does not create a separately addressable `Paragraph 4.2` node.

The parser has three important guards:

1. Two or more `N.—` markers select the SSO Act path and prevent MAS-format misclassification.
2. The Schedule gate is case-sensitive. All-caps headings such as `FIFTH SCHEDULE` stop further
   section parsing, while prose such as `the First Schedule` does not.
3. `ARRANGEMENT OF SECTIONS` and `ARRANGEMENT OF PROVISIONS` tables are removed before the
   monotonic section filter runs.

Documents with no recognized structure use the shared `Full text` fallback. Schedules are retained
as one schedule node; their internal provisions are not parsed.

## Retrieval and deployment

- Scope terms come from [`scope_term_sg.csv`](../../../deploy/seed/scope_term_sg.csv).
- The text normalizer is `vn-fold`; the corpus and query language are English.
- Evaluation cases are in [`golden_sg.json`](../../../deploy/eval/golden_sg.json).
- `kaya` uses its own PostgreSQL database. The read service is the shared MCP container and shared
  embedder on the single ECS host. A dedicated CloudFront distribution selects Singapore through
  the jurisdiction header. It is not a separate Singapore ECS container.

## Open work

- Crawl SSO subsidiary legislation. The current source only ships Acts even though SSO exposes
  subsidiary-legislation routes.
- Replace SSO PDF parsing with the provision-fragment HTML path if it measurably improves fidelity.
- Give title-only MAS guidance a stable lookup contract without inventing formal numbers.
- Extract SSO validity history and MAS supersession evidence before claiming structured relations.

## History

- **2026-07-17:** Singapore launched with SSO, MAS, PDPC, and CSA; `sg-act` and the MAS paragraph
  parser shipped.
- **2026-07-25:** the em-dash dispatch, case-sensitive Schedule gate, and table-of-contents removal
  restored the large Act bodies. The accepted corpus became 292 documents and 27,951 chunks.
