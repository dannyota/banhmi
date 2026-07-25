# Singapore jurisdiction (kaya) — design

**Status: LIVE since 2026-07-17** (`kaya.danny.vn`). Sources **live-verified 2026-07-16**. Extends
banhmi to **Singapore banking & financial regulation and technology law** per the shared
[`PLAYBOOK.md`](PLAYBOOK.md) — English corpus + MY citation family + the best-structured statute
source of the planned countries. Corpus size and accepted eval baseline live in
[`PLAN.md`](../../../PLAN.md#current-state).

## Basics

- **Codename / endpoint:** `kaya` / `kaya.danny.vn`
- **Language:** **English** — the binding legal language (native, like MY).
- **Scope:** the shared topical scope, Singapore jurisdiction.

## Sources (live-verified 2026-07-16)

| Source | URL | What it provides | Bot protection | Crawl difficulty |
|---|---|---|---|---|
| **MAS** | mas.gov.sg | **Notices (binding) + Guidelines**: TRM (FSM-N05), Cyber Hygiene (FSM-N06), outsourcing, BCM, Payment Services (PSN…). HTML landing + PDF. ~50–80 in-scope instruments. | **Akamai Bot Manager** — checks User-Agent only, not JS execution: `ChromeTransport()` alone suffices, **no cookie minter needed** (resolved 2026-07-17). | **4/5** |
| **SSO** | sso.agc.gov.sg | **Consolidated Acts + subsidiary legislation in HTML** — genuine provision tree with selectable sections. Banking Act 1970, FSMA 2022, Payment Services Act 2019, PDPA 2012, Cybersecurity Act 2018, ETA 2010. ~10–15 key Acts. | App-level 403 to non-browser requests; **bypassable** with proper headers (proven by sg-eli-mcp, Apache-2.0). | **3/5** |
| **PDPC** | pdpc.gov.sg | PDPA advisory guidelines (~15–20 PDFs). | **None.** JS-rendered listing pages; PDFs direct-downloadable. | **2/5** |
| **CSA** | csa.gov.sg | CII Codes of Practice (~5 PDFs). Supplementary only (the Cybersecurity Act is on SSO). | **None.** Server-rendered HTML. | **1/5** |

The sg-eli-mcp project (Apache-2.0, github.com/matematicsolutions/sg-eli-mcp) demonstrates SSO
is programmatically accessible — useful reference for URL patterns.

### API / access contracts (Playwright-verified 2026-07-16)

- **SSO:** Browse `/Browse/Act/Current/{letter}?PageSize=100` (A-Z, ~520 Acts). Per-Act PDF:
  `?ViewType=Pdf`. HTML lazy-loads (only Part 1 inline; full via `?ProvIds=pr{N}-`). SL:
  `/Browse/SL/Current/All` (~5,850 items). No API; table-based HTML with semantic CSS classes
  (`prov1Hdr`, `prov1Txt`, `prov2Txt`, `amendNote`). Browser UA required; no cookies.
  `data-json` embeds timeline (`timelineItems`) + fragment map. robots.txt blocks `/search` only.
- **MAS:** **Solr API** `GET /api/v1/search?fq=mas_contenttype_s:"Notices"&rows=500` — returns all
  321 notices in one JSON call. Also `"Guidelines"` (123), `"Circulars"` (170). Fields:
  `document_title_string_s`, `page_url_s`, `mas_date_tdt`, `mas_contenttype_s`. Faceting by
  `mas_sector_sm` (Banking 158, Capital Markets 78, Payments 27). PDF URL from HTML page scrape.
  Akamai checks User-Agent only (no cookie minter needed — `ChromeTransport()` suffices).
  43 cancelled notices have `[Cancelled]` in title. Sitemap: 7,319 URLs (339 notices, 124 guidelines).
- **PDPC:** **JSON API** `GET /api/listing-api?listingtype=regulatory_guidance&itemsperpage=100`.
  Returns `{totalItems, data[{id, topic, title, href, date}]}`. PDFs on Optical CDN:
  `files.app.optical.gov.sg/pdpc/production/assets/{UUID}.pdf` (direct, no auth).
  10 Advisory Guidelines + 7 sector-specific + 22 practical + 385 enforcement decisions.
- **CSA:** Isomer CMS. Sitemap `sitemap.xml` lists all pages. PDFs at
  `isomer-user-content.by.gov.sg/36/{uuid}/{filename}.pdf` (direct, needs non-empty UA).
  ~20-30 regulatory PDFs (CCoP, AI security guidelines, CII frameworks). No WAF.

### Doc number formats

- **MAS legacy (sector Acts):** `MAS Notice {NNN}` (e.g. 610, 637, 655).
- **MAS FSMA era (post-May 2024):** `FSM-N{NN}` (e.g. FSM-N05, FSM-N06). Payments: `PSN{NN}`.
- **SSO:** Act code + year (e.g. `PSA2019`, `CA2018`).
- **PDPC/CSA:** descriptive names only (no formal numbering).

## Citation model

Acts: `Part → **Section** → **Subsection** "(1)" → **Paragraph** "(a)"` — the **same family as MY**,
so the MY parser/chunker path near-reuses. MAS Notices/Guidelines cite by **paragraph number**
(e.g. "para 4.2") → one new small parser for notice-style documents.

## Structure parser (`sg-act`) — validated 2026-07-17

One parser, two formats, auto-detected per document (`pkg/pipeline/singaporeparse.go`); reuses the MY
tree builder, level constants, and monotonic section filter.

- **Dispatch on em-dash section markers** — `^\d+[A-Z]*\.—` ("2.—(1) In this Act"). SSO Acts have
  them; MAS Notices never do. Without this gate the MAS-notice detector claims the largest Acts
  (BA1970, SFA2001, FSMA2022, PSA2019, PDPA2012, ETA2010, CA2018 …) and their bodies vanish.
- **The Schedule gate must be case-SENSITIVE** — real headings are ALL-CAPS (`FIFTH SCHEDULE`). A
  case-insensitive regex matches inline prose ("the First Schedule;") mid-Act and permanently stops
  section parsing: PSA2019 2→136, SFA2001 0→724, CA2018 0→74, BA1970 57→170 sections.
- **Cut the `ARRANGEMENT OF SECTIONS`/`OF PROVISIONS` ToC** before parsing — its `1. Short title`
  entries otherwise consume the monotonic filter, which then rejects every real body section
  (PDPA2012 / ETA2010 / FSMA2022 each produced 0 chunks).
- **MAS notice parser** — paragraph numbering (`1 `, `1. `, `1.1 `, `4.2(a)`) under bare section
  headings, cited as `paragraph N` / `paragraph N.M`, chunked at section level (not the full-text
  fallback).

## Corpus quality — known gaps

- **MAS Guidelines often have no formal number** — descriptive titles only, so `doc_number` lands
  empty. Eval and citation paths keyed on `doc_number` cannot match these — cite them by title.
- **No subsidiary legislation** — `sso` ships Acts only. SL routes exist (per-Act `?ViewType=Sl`,
  index `/Browse/SL/Current/All`) but are unbuilt; the SG corpus is Acts + regulator instruments.

## Deltas from the shared core

| Area | Singapore | Work |
|---|---|---|
| Structure | **SSO PDF** (`?ViewType=Pdf`) — the HTML lazy-loads, so the PDF is what shipped; MAS PDFs | `sg-act` PDF→tree parser + MAS notice-paragraph parser; SSO per-fragment **HTML** (`?ProvIds=pr{N}-`) is the remaining provision-fidelity upgrade |
| Validity/relations | SSO consolidation dates + revised editions; MAS supersession prose | map via `config.validity_status`; BNM-style inference for MAS |
| Scope vocab | reuse the MY English seed as the base (`scope_term_sg.csv`): + TRM, FSMA, MAS Notice, e-payments, digital bank licence, … | adapt + seed |
| OCR | modern corpus, born-digital | minimal; Vision OCR `en` floor exists |
| Retrieval | English | MY router profile reuses |

## Risks / open questions

- **MAS Akamai — RESOLVED.** Akamai only checks User-Agent, not JS execution. `ChromeTransport()`
  with a browser UA bypasses it. No cookie minter needed. Solr API returns all notices in one call.
- **FSMA migration is live** — instruments moving from sector Act numbering (e.g. `MAS Notice 655`)
  to FSMA numbering (`FSM-N06`). 43 cancelled notices have `[Cancelled]` in title. Source package
  must handle both numbering schemes and infer supersession from topic matching + cancellation dates.
- **PDPC JS-rendering — RESOLVED.** Clean JSON API at `/api/listing-api` returns all items.
  No headless browser needed for discovery.
- **SSO ToU** — AGC grants permission to use/reproduce legislation; no explicit prohibition of
  automated access found. The sg-eli-mcp project operates within these terms.

## Recommended build order

1. **SSO** (Acts) first — the foundation. Clean HTML provision trees, predictable URLs, small
   in-scope set. Spike with Payment Services Act 2019 (as planned).
2. **PDPC** second — easiest technically. Direct PDF downloads, no bot protection.
3. **CSA** third — trivial (~5 PDFs, supplement only).
4. **MAS** last — hardest technically (Akamai). But MAS is the PRIMARY source for binding
   Notices/Guidelines, so the Akamai spike must happen early in the build even if full
   ingestion comes last.

## Phased plan

Playbook template. Parser-spike flagship: Payment Services Act 2019 (SSO HTML) + TRM
Guidelines PDF (MAS). Deploy = `kaya` DB + ECS container `:8084` + CloudFront distribution +
domain + `golden_sg.json` eval gate.
