# Cambodia jurisdiction (amok) — design

**Status: LIVE v0.4.3-20260720.** Sources live-verified 2026-07-20 (NBC via local HTTP CONNECT
forwarder → KH SOCKS5; direct for SERC/ODC/CDC). **English-only corpus** — Khmer-script docs are
filtered out at discovery and quarantined at extraction (see [Corpus state](#corpus-state)).

## Basics

- **Codename / endpoint:** `amok` / `amok.danny.vn`
- **Language:** **English** — NBC, SERC, CDC publish English translations. English is the working
  language for international banking compliance in Cambodia. Khmer is the constitutionally binding
  language; English translations are explicitly "unofficial" but routinely published by NBC.
- **Scope:** the shared topical scope, Cambodian jurisdiction.

## Sources (live-verified 2026-07-20)

| Source | URL | What it provides | Bot protection | Crawl difficulty |
|---|---|---|---|---|
| **NBC** | www.nbc.gov.kh | Prakas, laws, IT guidelines, banking codes — `/english/` pages only (4 paths) | **CloudFront geo-block** (residential KH IPs only) + browser UA | **3/5** |
| **SERC** | serc.gov.kh | Securities laws, Prakas, Guidelines, Anukrets — ~49 English docs | Cloudflare UA check (trivial) | **2/5** |
| **CDC** | cdc.gov.kh | English law PDFs (30 financial) — foundational laws pre-2011 | Cloudflare (trivial) | **1/5** |
| **ODC** | data.opendevelopmentcambodia.net | CKAN API: laws, bilingual PDFs | **None** (open API) | **1/5** |

### NBC crawl contract (verified against code 2026-07-20)

NBC discovery crawls **only the `/english/` page variants** (`pkg/ingest/nbc/discover.go`):

| Path | DocType |
|---|---|
| `/english/legislation/prakas_new.php` | Prakas |
| `/english/legislation/laws_applicable_to_banks_and_financial_institutions.php` | Law |
| `/english/publications/guidelines_it_policy.php` | IT Guideline |
| `/english/publications/banking_code.php` | Banking Code |

The non-`/english/` pages are the Khmer UI and link only `*_kh` PDFs — crawling them yields
Khmer-only files or misses English documents entirely.

**Khmer-file filter** (case-insensitive): URLs containing `_kh/`, `_kh.`, or `-kh.` are skipped.
Covers patterns seen on the live site: `laws_kh/`, `itguideline_kh/`, `*_kh.pdf`, `*-KH.pdf`.

**DetailURL = PDF URL.** NBC has no per-document page; Discover sets `DetailURL` to the PDF
download URL, and `FetchDetail` reconstructs the file reference from it. (Previously `DetailURL`
pointed at the listing page, which caused fetch to download the listing HTML as every document's
"main PDF" — 154 nbc + 53 cdc junk docs.) The same contract applies to CDC (`pkg/ingest/cdcgov`).

### NBC access — proxy mechanism

NBC is geo-blocked by CloudFront to **residential KH IPs only** and requires a **browser
User-Agent**. The known KH residential SOCKS5 proxies have **no remote DNS** — they reject SOCKS5
DOMAIN requests. Go's `http.Transport` and Chromium both delegate DNS to the SOCKS5 proxy, so a
raw SOCKS5 URL does not work.

**Solution:** `BANHMI_NBC_PROXY_URL` points at a **local HTTP CONNECT forwarder** that resolves
DNS locally and tunnels the connection by IP through the SOCKS5 proxy (pattern:
`http://127.0.0.1:<port>`). Never hardcode proxy addresses in code or docs — the mechanism is
what matters, not the IP.

The code accepts both `socks5://` and `http://` proxy URLs via `http.ProxyURL` (`pkg/ingest/nbc/nbc.go`).

### CDC crawl contract

Single static HTML page at `/laws-and-regulations/`. PDF links matched by regex; DetailURL = PDF
URL (same contract as NBC). Mostly investment/foundational law; uploads span 1994–2022.

### Other sources

- **SERC:** BBS at `/boards/index.php?bid={boardID}`. 5 EN + 5 KH boards. Curl + Chrome UA works.
- **ODC:** CKAN REST API. CC-BY-SA-4.0. Supplementary (thin on NBC prakas).

## Corpus state (v0.4.3-20260720)

| Metric | Count |
|---|---|
| **Silver docs** | 284 |
| **Indexed docs** | 244 |
| **Chunks** | 7,757 |

**40 Khmer-only scanned docs quarantined.** Their `ocr_extractive` rows were deleted — the corpus
is English-only, and OCR of Khmer script with `en` language hints produces transliteration
mojibake. These surface as explicit coverage gaps via MCP `quality_gaps`.

**Operational note:** any future `ocr-all` rerun on amok will re-create those rows from the OCR
cache. They must be re-classified (windowed English-word-rate analysis) and deleted before
indexing, until a Khmer-script gate lands in `OcrAll` (queued follow-up).

## Notable coverage

- **NBC Technology and Cyber Risk Management Guideline (TCRMG) 2026**
- **Technology Risk Management Guidelines 2019**
- **Banking Codes 2008-2021** (compilation volumes)
- **Law on Banking and Financial Institutions 1999**
- **AML/CFT Law**
- **Consumer-complaint Prakas**
- **Capital-adequacy Prakas**

## Citation model

Cambodian law uses **Article** as the primary provision unit. Sub-articles use numbering `(1)`,
`(2)` — same family as SG/MY (Section/Subsection). NBC Prakas cite by **Article** or **Clause**.

## Hard parts

1. **NBC geo-block** — CloudFront blocks all non-Cambodia IPs including ASEAN cloud instances.
   Requires a SOCKS5 KH residential proxy fronted by a local HTTP CONNECT forwarder (no remote
   DNS). Free public proxies are ephemeral — design for fallback.
2. **No consolidated legal database** — no JDIH-equivalent. Documents scattered across NBC,
   SERC, CDC, ODC.
3. **Khmer-only scanned docs** — 40 quarantined docs need a Khmer-script OCR gate before they can
   be indexed. Currently manual classification + delete.
4. **Regulatory gaps** — Cybersecurity law and Personal Data Protection Law are still drafts.
5. **NBC JS redirect trap** — delayed JavaScript redirects to serc.gov.kh on some pages.
6. **Stale CDC corpus** — covers laws pre-2011 only.

## Deltas from the shared core

| Area | Cambodia | Work |
|---|---|---|
| Structure | NBC/SERC/CDC PDF-only sources; DetailURL = PDF URL | Article-based parser (MY/SG family) |
| Validity/relations | NBC Banking Code compilations track amendments | parse compilation; no structured API |
| Scope vocab | English seed (`scope_term_kh.csv`) | banking, payment, AML, fintech terms |
| Retrieval | English | MY/SG router profile reuses |
| Source access | NBC needs KH proxy (HTTP CONNECT forwarder → SOCKS5); SERC Cloudflare trivial; CDC/ODC open | `BANHMI_NBC_PROXY_URL` |
