# Thailand jurisdiction (`tomyum`)

**Status: live since 2026-07-17** at `tomyum.danny.vn/mcp`. Source behavior was last checked between
2026-07-16 and 2026-07-26. This document describes the shipped implementation. Shared rules are in
[`PLAYBOOK.md`](PLAYBOOK.md).

## Current corpus

The accepted production baseline in [`PLAN.md`](../../../PLAN.md#current-state) includes the BOT
recovery and SEC ingest:

| Metric | Value |
|---|---:|
| Corpus documents | 1,790 |
| Indexed documents | 1,788 |
| Chunks | 32,715 |
| Eval cases | 58 |
| Recall | 89.5% |
| Mean reciprocal rank (MRR) | 69.6% |
| In-force accuracy | 100% |
| Abstention accuracy | 96.6% |

The corpus is Thai-only. English translations are not indexed because they are non-binding. Queries
should be translated to Thai before search.

## Sources and current contracts

The runtime wires `ocs`, `bot`, `etda`, and `sec` in
[`pkg/app/app.go`](../../../pkg/app/app.go). The Royal Gazette, OIC, and NBTC are not implemented
sources.

### Office of the Council of State (`ocs`)

Discovery paginates `www.ocs.go.th/searchlaw/indexs/list_table_search` and keeps law codes containing
`-1B-`, the Act type. The listing host has an incomplete certificate chain, so the client currently
uses `InsecureSkipVerify`. The separate full-text host has a valid TLS path.

`FetchDetail` posts the encrypted timeline ID to
`searchlaw.ocs.go.th/ocs-api/public/doc/getLawDoc`. The current client reads `lawInfo` and
`lawSections`, maps section types 4/8/9 to มาตรา/หมวด/ส่วน, and stores the resulting HTML as the
authoritative text. It does not use the listing PDF for extraction.

The client maps status code `01` to `in_force` and `02` to `repealed`. Although the live API and
listing records expose amendment and subordinate-law data, the current Go response model does not
persist timelines, footnotes, or child links as document relations.

### Bank of Thailand (`bot`)

Discovery crawls the FIPCS WebForms listing for document groups 1 (Financial Institutions) and 3
(Payment Systems). It keeps the ASP.NET cookie jar and ViewState across pages. Listing rows provide
the document type, issue date, title, `packId`, status icon, and PDF links. The status icon's `alt`
text maps `ยกเลิก` to `revoked`; other rows default to `active`.

The listing's PDF paths may contain Windows backslashes and several path groups, including `DDD`,
`DMG`, and `FOG`. Discovery normalizes the separators, persists the exact file references, and
`FetchDetail` reuses them. It synthesizes an `FPG/{year}/ThaiPDF/{packId}.pdf` URL only for older
ledger rows with no stored reference.

The detail summary page is deliberately skipped because it needs the same session and would
serialize fetches. Most BOT text comes from Thai Vision OCR. The CDN returned intermittent 403s in
the 2026-07-25 recovery; serial pipeline retries recovered 279 of 280 stranded documents. One source
link, `25650005`, remained dead.

### Electronic Transactions Development Agency (`etda`)

Discovery scrapes three server-rendered pages for digital-platform law, digital-ID law, and
recommendations. It deduplicates attachments by GUID. `etdaDocNumber` derives a citable
`ขมธอ. N-YYYY` identifier when that designation appears in the filename or title.

The site recovered before the 2026-07-26 ingest test. Adding designation terms raised the in-scope
set from 1 to 43 documents, but the larger ingest was reverted because recall fell to 87.7% and MRR
fell to 67.9%, below the 0.68 floor. The production baseline therefore retains the smaller ETDA
coverage.

There is also a prospective fetch bug: `Discover` records the real attachment URL, but
`FetchDetail` ignores `DetailRef.Files` and constructs `/getattachment/{GUID}/file.pdf.aspx`.
Future ETDA crawls should reuse the stored discovery reference, as BOT now does.

### Securities and Exchange Commission (`sec`)

Discovery posts to `capital.sec.or.th/webapp/nrs/nrs_main_search.php` for the Digital Assets and IT
Systems categories, decodes CP874, and parses all results from one response. The current row mapping
uses column 1 for the notification designation, column 2 for the subject, column 6 for the signed
date, and column 7 for the effective date. It prefers DOCX, then readable PDF, then signed PDF.

`publish.sec.or.th` is not geo-blocked. Its F5 rejects bot-shaped clients; browser request headers
work directly from Vietnam. `BANHMI_SEC_PROXY_URL` remains optional. Sixteen SEC documents are in
the production corpus; five other in-scope source files returned 404. Thirteen of the indexed rows
are low-value form or attachment entries, while three are substantive notifications.

## Parser and citations

The `th-act` parser is [`pkg/pipeline/thailandparse.go`](../../../pkg/pipeline/thailandparse.go).
It converts Thai digits to Arabic digits in labels and preserves amendment suffixes ทวิ, ตรี, and
จัตวา.

| Document shape | Parsed hierarchy | Citation examples |
|---|---|---|
| OCS Act | หมวด → ส่วนที่ → มาตรา → parenthesized numeric item | `หมวด 2, ส่วนที่ 1, มาตรา 26, (1)` |
| BOT notification | ข้อ → parenthesized numeric item | `ข้อ 4, (1)` |

The parser does not create separate nodes for unnumbered วรรค paragraphs or ordinal forms such as
`วรรคหนึ่ง`. Those remain inside the enclosing มาตรา or ข้อ content. `วรรค N` is also the label for
mechanical splits of long chunks, not proof that a legal paragraph was structurally parsed.

Format dispatch selects the BOT path only when at least two ข้อ markers and no มาตรา markers are
found. Documents with no recognized structure use the shared `Full text` fallback.

## Retrieval, dates, and deployment

- Thai Character Cluster segmentation is implemented in
  [`pkg/rag/lexical/tcc.go`](../../../pkg/rag/lexical/tcc.go). Indexing and queries use the same
  `th` normalizer. Thai combining marks are preserved.
- The code explicitly maps Thai digits. The implemented Buddhist Era conversion subtracts 543;
  it does not implement the pre-1941 January–March 542-year edge case.
- Scope terms come from [`scope_term_th.csv`](../../../deploy/seed/scope_term_th.csv).
- Evaluation cases are in [`golden_th.json`](../../../deploy/eval/golden_th.json).
- `tomyum` uses its own PostgreSQL database. The read service is the shared MCP container and
  embedder on the single ECS host, with a dedicated CloudFront distribution.

## Open work

- Parse OCS Acts into reliable มาตรา nodes before reintroducing the broader ETDA guidance set. The
  reverted test showed guidance outranking the ETDA Act on the mandate case.
- Make ETDA `FetchDetail` reuse discovery-time file references.
- Persist OCS timelines, footnotes, and subordinate-law links if they can be mapped without
  overstating legal relations.
- Model the likely succession from Act `ป0045-1B-0001` to `ป0054-1B-0001`; OCS marks both in force
  and the corpus has no relation edge.
- Decide whether to exclude SEC form and attachment rows from the scope gate.
- Remove `InsecureSkipVerify` if OCS fixes its certificate chain or a narrower trust workaround is
  added.

## History

- **2026-07-17:** Thailand launched with OCS, BOT, ETDA, SEC wiring, `th-act`, Thai Character
  Cluster segmentation, and Vision OCR for scanned BOT documents.
- **2026-07-25:** exact BOT file references and backslash normalization recovered 279 documents.
- **2026-07-26:** browser headers replaced the incorrect SEC geo-block diagnosis; 16 SEC documents
  were indexed. The broader ETDA ingest was measured and reverted.

An external validation dataset, `PyThaiNLP/thai-law`, was recorded as a possible historical
cross-check. It is not a production source.
