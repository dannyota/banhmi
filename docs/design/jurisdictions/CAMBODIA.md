# Cambodia jurisdiction (`amok`)

**Status: live since 2026-07-18** at `amok.danny.vn/mcp`. The current corpus was rebuilt and deployed
on 2026-07-20. This document describes the shipped implementation. Shared rules are in
[`PLAYBOOK.md`](PLAYBOOK.md).

## Current corpus

Cambodia is the one jurisdiction where the document counts need two labels:

| Metric | Value |
|---|---:|
| Silver documents | 284 |
| Indexed documents | 244 |
| Chunks | 7,757 |
| Eval cases | 42 |
| Recall | 94.4% |
| Mean reciprocal rank (MRR) | 72.7% |
| In-force accuracy | 100% |
| Abstention accuracy | 100% |

[`PLAN.md`](../../../PLAN.md#current-state) reports 284 under its general `Docs` column. The
2026-07-20 rebuild record gives the more precise split: 244 of 284 Silver documents are indexed.

The served corpus is English-only. Khmer is the binding language; the indexed English texts are
translations or English publications. Users should treat an unofficial translation as evidence of
the published English text, not as a replacement for the Khmer original.

## Sources and current contracts

The runtime wires `nbc`, `serc`, `cdc`, and `odc` in
[`pkg/app/app.go`](../../../pkg/app/app.go).

### National Bank of Cambodia (`nbc`)

Discovery crawls only these English pages:

| Path | Document type |
|---|---|
| `/english/legislation/prakas_new.php` | Prakas |
| `/english/legislation/laws_applicable_to_banks_and_financial_institutions.php` | Law |
| `/english/publications/guidelines_it_policy.php` | IT Guideline |
| `/english/publications/banking_code.php` | Banking Code |

NBC has no per-document page. `DetailURL` is the PDF URL, and `FetchDetail` reconstructs the file
reference from it. Discovery skips URLs containing `_kh/`, `_kh.`, or `-kh.` without regard to
case. This filter is NBC-specific; it is not a general Khmer-script detector.

NBC requires a browser User-Agent and, at the last live check on 2026-07-20, a residential Cambodia
egress address. `BANHMI_NBC_PROXY_URL` accepts an HTTP or SOCKS5 proxy URL through `http.ProxyURL`.
The proxy used for the rebuild could not resolve SOCKS5 domain requests, so operations placed a
local HTTP CONNECT forwarder in front of it to resolve DNS locally and tunnel by IP. Proxy addresses
must not be stored in code or documentation.

### Securities and Exchange Regulator of Cambodia (`serc`)

Discovery paginates five English BBS boards: Law, Anukret, Prakas, Order/Guideline, and Other. Each
listing row provides a direct `/boards/data_dir/{board}/{file}.pdf` link.

The current fetch contract is inconsistent. `Discover` stores the PDF in `Files` but sets
`DetailURL` to the board listing page. `FetchDetail` ignores `DetailRef.Files` and treats
`DetailURL` as the PDF. A new SERC fetch can therefore plan listing HTML as a PDF. This should be
fixed before a full SERC recrawl.

### Council for the Development of Cambodia (`cdc`)

Discovery scrapes the single `/laws-and-regulations/` page for WordPress PDF links. As with NBC,
`DetailURL` is the PDF URL. The 2026-07-20 fix replaced the earlier listing-page value that caused
HTML to be downloaded as document content. CDC is mainly a source of older foundational and
investment laws.

### Open Development Cambodia (`odc`)

ODC is a supplementary open-data mirror, not the primary publisher. Discovery runs a fixed set of
financial and technology-law queries through CKAN `package_search`, keeps Cambodia-only packages,
and accepts PDF, DOCX, or DOC resources. `package_show` enriches each record. The document number
comes from ODC metadata when present and otherwise falls back to the CKAN package slug.

## Language gate and quarantine

Forty Khmer-only scans were quarantined during the 2026-07-20 rebuild. Their `ocr_extractive` rows
were deleted because English-hint OCR produced unusable transliteration. They remain visible as
coverage gaps through `quality_gaps`.

This quarantine is operational, not yet enforced by `OcrAll`. A future `ocr-all` run can restore the
cached OCR rows and make the documents eligible for indexing again. Until a Khmer-script gate is
implemented, every rebuild must repeat the English-word-rate classification before indexing.

## Parser and citations

The `kh-act` parser is [`pkg/pipeline/cambodiaparse.go`](../../../pkg/pipeline/cambodiaparse.go).
It parses:

`Chapter → Article → (1) → (a) → (i)`

It also recognizes `Clause N` as a primary marker, but normalizes the emitted label to `Article N`.
The current citation output therefore does not preserve the source word `Clause`. Annex, Schedule,
and Appendix headings open one schedule node and stop further article parsing. Documents with no
recognized Article or Clause sequence use the shared `Full text` fallback.

The source packages do not currently extract structured amendment or replacement relations.
Statusless documents follow Cambodia's configured unknown-validity-as-in-force policy. ODC may
supply a raw status string, but NBC, SERC, and CDC generally do not.

## Retrieval and deployment

- Scope terms come from [`scope_term_kh.csv`](../../../deploy/seed/scope_term_kh.csv).
- The text normalizer is `vn-fold`; the corpus and query language are English.
- Evaluation cases are in [`golden_kh.json`](../../../deploy/eval/golden_kh.json).
- `amok` uses its own PostgreSQL database. The read service is the shared MCP container and shared
  embedder on the single ECS host, with a dedicated CloudFront distribution.

## Confirmed coverage

The 2026-07-20 rebuild record confirms coverage for:

- NBC Technology and Cyber Risk Management Guideline 2026;
- Technology Risk Management Guidelines 2019;
- Banking Code compilations from 2008–2021;
- Law on Banking and Financial Institutions 1999;
- anti-money laundering and counter-terrorist financing law;
- consumer-complaint Prakas; and
- capital-adequacy Prakas.

## Open work

- Add a Khmer-script gate to `OcrAll` before the next full OCR rebuild.
- Fix SERC `FetchDetail` to reuse discovery-time PDF references.
- Deduplicate the ODC and NBC copies of the 2019 technology-risk guidelines.
- Record the 2026 TCRMG relationship to the 2019 guidelines only after the source text supports an
  exact relation type.
- Remove table-of-contents noise from TCRMG citations.
- Recheck the 2026-07-20 external-law note before relying on it: that review described the
  cybersecurity and personal-data-protection laws as drafts, but this audit did not refresh the
  external legal status.

## History

- **2026-07-18:** Cambodia launched with 282 documents and 2,609 chunks.
- **2026-07-20:** NBC and CDC's `DetailURL` contract was repaired; NBC was recrawled from English
  pages; 40 Khmer-only scans were quarantined. The rebuild produced 284 Silver documents, 244
  indexed documents, and 7,757 chunks.
