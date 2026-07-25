# Thailand jurisdiction (tomyum) — design

**Status: LIVE since 2026-07-17** (`tomyum.danny.vn`). Sources live-verified 2026-07-16. Extends
banhmi to **Thai banking & financial regulation and technology law** per the shared
[`PLAYBOOK.md`](PLAYBOOK.md) — the heaviest language work of the six (see *Hard parts*). Corpus size
and accepted eval baseline live in [`PLAN.md`](../../../PLAN.md#current-state).

## Basics

- **Codename / endpoint:** `tomyum` / `tomyum.danny.vn`
- **Language:** **Thai** — the binding legal language. OCS publishes EN translations (147 Acts)
  that are explicitly **non-binding → never indexed** (playbook policy).
- **Scope:** the shared topical scope, Thai jurisdiction.

## Sources (live-verified 2026-07-16)

| Source | URL | What it provides | Bot protection | Crawl difficulty |
|---|---|---|---|---|
| **OCS** | www.ocs.go.th | **Consolidated Acts** via JSON API (`searchlaw/indexs/list_table_search`): 1,891 laws, amendment tracking, in-force status, subordinate legislation links. All 6 target Acts confirmed. | **None** (bad TLS cert only — needs InsecureSkipVerify). | **2/5** |
| **BOT** | app.bot.or.th/FIPCS | **Notifications/circulars** (ประกาศ ธปท.): ~800–1,200 in-scope of ~3,700 total. PDF-only (no HTML body). ASP.NET WebForms pagination (`__doPostBack`). | **None** (Akamai CDN, no WAF). | **3/5** |
| **ETDA** | www.etda.or.th | E-transactions / Digital ID / Platform services (~40–60 instruments). ASP.NET + PDF. | **None.** | **1/5** |
| **SEC** | capital.sec.or.th | Digital assets / fintech securities (~80–150). Search subdomain open; PDF download may need browser headers. | WAF on main site; search subdomain **open**. | **4/5** |
| **Royal Gazette** | ratchakitcha.soc.go.th | New-law publication signal. Cloudflare on HTML; PDFs open at `/documents/{id}.pdf`. | **Cloudflare** on HTML. | **4/5** — supplementary, defer to Phase 2. |
| OIC, NBTC | — | **Skip.** Low relevance (4–10 docs), high friction (SPA/blanket 403). | — | — |

**Key finding:** `krisdika.go.th` is **dead** (404, self-signed cert). The Office of the Council
of State migrated to **`www.ocs.go.th`** with a structured JSON API — far cleaner than the old
HTML scraping assumption. Only **SEC** geo-blocks (F5 BIG-IP, see below) — OCS, BOT, and ETDA are
reachable from anywhere.

### Doc number formats

- **BOT:** `ธปท. ว. 3725/2569` (circular), `ประกาศ ธปท. ที่ 26/2569` (notification). Pattern:
  `{dept-abbrev} {seq}/{B.E. year}`.
- **OCS:** lawCode `ธ0012-1B-0001` (internal); display: Act name + B.E. year.
- **ETDA/SEC:** notification numbers; descriptive.
- ~270 BOT docs (~7%) have English "Unofficial Translation" PDFs alongside Thai.

### API / access contracts (Playwright-verified 2026-07-16)

- **OCS — two APIs:**
  - *Discovery:* `GET www.ocs.go.th/searchlaw/indexs/list_table_search?page={N}` — 10/page, 1,884
    laws, 189 pages. Returns `lawCode`, `lawNameTh`, `lawNameEn`, `encTimelineID`, `year`,
    `publishDate`, `fileUUID` (JWT-signed PDF URL), `childrens` (subordinate legislation array),
    `state`. No server-side filtering — paginate all, filter client-side by lawCode type
    (`-1B-` = Acts). TLS: missing intermediate cert on `www.ocs.go.th` → `InsecureSkipVerify`.
  - *Full text:* `POST searchlaw.ocs.go.th/ocs-api/public/doc/getLawDoc` with
    `{reqHeader:{reqChannel:"WEB",serviceName:"getPublicLawDoc",...}, reqBody:{timelineId:"<encTimelineID>"}}`
    → structured JSON: `lawInfo` (metadata + dates + stateId) + `lawSections[]` (sectionTypeId,
    sectionNo, sectionContent HTML, sectionLabel) + `footnoteList[]` (amendment annotations) +
    `timelines[]` (version history). Section types: 4=มาตรา, 8=หมวด, 9=ส่วน. Valid TLS on this host.
  - *Target Acts found:* ธ0012 (FI Act), ร0058 (Payment Systems), ค0136 (PDPA), ก0189 (Cybersecurity),
    ว0063 (ETA), ห0015 (Securities). All state=01, with subordinate legislation links.
  - *Validity/relations:* `stateId` **`01` = in force, `02` = repealed** (`mapState`,
    `pkg/ingest/ocs/discover.go`); `timelines[]` is the amendment chain, `footnoteList[]` the
    per-section amendment notes, `childrens` the subordinate-legislation links.
  - *Text authority:* `sectionContent` HTML is stripped to text straight into `[]Section` — OCS Acts
    **never touch the PDF/OCR path**. Some sections carry noisy whitespace the content gate can read
    as non-binding; the **API text, not the PDF, is authoritative for OCS Acts** (open review item).
- **BOT FIPCS:** `app.bot.or.th/FIPCS/Thai/PFIPCS_list.aspx` — ASP.NET WebForms, 30/page, 374 pages
  (~11,220 total; ~1,560 active in-scope with DocGroup 1+3). ViewState pagination via
  `__doPostBack('ctl00$...$dgDocument$ctl33$ddlPageSelector', pageNum)`. Session-bound: must reuse
  `ASP.NET_SessionId` + ViewState from initial GET. 8 dropdown filters (DocGroup, Year, DocType,
  Status, etc.) + 3 text searches. Summary page at `PFIPCS_summary.aspx?packId={PACKID}` has
  dates, purpose, substance — but **`FetchDetail` deliberately skips it** (it needs the session,
  which serializes concurrency at ~3 s/doc); metadata comes from the listing row instead.
  *Listing row (6 cells):* 0=DocType, 1=date, 2=new-icon, 3=title + `packId` (from the
  `OpenWindow('PFIPCS_summary.aspx?packId=…')` call), 4=**status img `alt`** (`ยกเลิก` → revoked,
  else active), 5=PDF links. DocGroup **1 = Financial Institutions, 3 = Payment Systems**.
  PDF: `www.bot.or.th/content/dam/bot/fipcs/documents/{GROUP}/{YEAR_BE}/ThaiPDF/{PACKID}.pdf`
  (direct, no auth). packId format: `YYYYNNNN` (B.E. year + sequence). **`{GROUP}` is NOT always
  `FPG`** — documents also live under `DDD`, `DMG` and others, so the path cannot be synthesised from
  the packId; use the href `Discover` scrapes from listing column 5 (replayed via
  `DetailRef.Files`). Hrefs use Windows-style backslashes (`2541\ThaiPDF\x.pdf`); the server
  accepts either separator.
  **The dam CDN fails ~half of requests non-deterministically** — the same URL with identical headers
  returns 403/200/403/403/200 at 8-second spacing (measured 2026-07-25 from a VN IP). It is **not**
  geo-blocking (the PDFs download fine from outside Thailand), **not** User-Agent filtering, and
  **not** rate limiting — pacing does not help. Treat a 403 as transient and retry: the fetch ledger
  already backs off 5 minutes over 5 attempts, which recovers the large majority. Only **SEC**
  genuinely geo-blocks.
- **ETDA:** **3 listing pages shipped** — `/th/regulator/Digitalplatform/law.aspx`,
  `/th/regulator/DigitalID/law.aspx`, `/th/Our-Service/Recommendation.aspx`; every doc renders on one
  page per section (no pagination), and discovery **dedups by GUID** because the same PDF is linked
  from several pages. Server-rendered ASP.NET HTML. PDFs at `getattachment/{GUID}/{filename}.aspx`
  (direct, no auth). No API — HTML scrape. Intermittent connectivity but no hard geo-block.
- **SEC:** `capital.sec.or.th/webapp/nrs/nrs_main_search.php` — PHP POST form, no pagination (all
  results inline). 15 document types, hierarchical category filter. ~101 digital-asset + ~36 IT docs.
  PDFs on `publish.sec.or.th/nrs/{NRS_ID}{suffix}` — **F5 BIG-IP geo-blocks non-TH IPs** (flat 403,
  not JS challenge). TIS-620/CP874 charset. **Needs Thai IP proxy** — AWS `ap-southeast-7`
  (Bangkok) confirmed: t4g.micro + tinyproxy, on-demand (~$0.005/hr). Phase 2.

## Corpus state

- **SEC deferred — 0 docs indexed.** The package is wired and unit-tested, but the Bangkok proxy
  (`BANHMI_SEC_PROXY_URL`, `ap-southeast-7` t4g.micro) has never been launched. SEC coverage is a
  standing gap, not a bug.
- **~270 BOT docs unfetched** — `FetchDetail` synthesizes
  `{pdfBase}/FPG/{packId[:4]}/ThaiPDF/{packId}.pdf` and discards the real hrefs `Discover` scraped
  from listing column 5, so any packId that is not 8-char `YYYYNNNN` — or not in group `FPG` — 404s.
  Fix: carry Discover's hrefs through instead of synthesizing.
- **ETDA yields 1 in-scope doc of ~46 discovered** — `scope_term_th.csv` misses ETDA's standards and
  recommendation phrasing. Fix the **seed vocabulary**, not the source or the gate.

## Citation model

Acts: **มาตรา** (Section) → **วรรค** (paragraph, ordinal words: วรรคหนึ่ง, วรรคสอง, วรรคสาม) →
**(๑)(๒)** items; BOT notifications number by **ข้อ** (clause). Two label families (like VN's
Luật vs Thông tư). Amendment suffixes use Pali/Sanskrit (ทวิ, ตรี, จัตวา). Native Thai labels
render verbatim.

## Hard parts (why TH is recommended last)

1. **Thai word segmentation — SOLVED (2026-07-17): TCC-gram, pure Go.** Thai has no inter-word
   spaces, so whitespace BM25 produces garbage, and no Go-native tokenizer existed (mapkha is
   archived). Shipped: **Thai Character Clusters** (`pkg/rag/lexical/tcc.go`, PyThaiNLP /
   Theeramunkong rules — deterministic, regex-based, no dictionary). Descriptor
   `TextNormalizer: "th"` (`NormTH`) segments clusters, space-joins them, lower-cases Latin, and
   **never NFD-strips** (Thai combining marks are integral to the script). `cmd/lexindex` and the
   query path share the normalizer, so **index/query tokenization is identical**. **nlpo3 dropped**
   — no FFI or subprocess needed.
2. **Buddhist Era dates** (B.E. = CE + 543) — custom Go parser (~50 lines). Formal sources use
   Thai numerals + พ.ศ.; portals mix Arabic numerals. Pre-1941 edge: offset is 542 for Jan-Mar.
3. **Thai numerals** (๐–๙) — NFKD/NFKC do NOT convert Thai digits; explicit mapping required.
   Normalize on ingest, index both forms for BM25.
4. **BOT PDFs are mostly scans — RESOLVED 2026-07-17: Vision OCR is BOT's *primary* text path,
   not a fallback** (the large majority of fetched BOT PDFs are scanned — plan OCR cost and time
   for BOT accordingly). Vision OCR + `th` language
   hint (already integrated); Tesseract NOT viable for Thai (CER >75%); EasyOCR usable as
   fallback (8.6% CER on born-digital).
5. **OCS TLS cert chain incomplete** — needs InsecureSkipVerify or manual cert pinning.

## Deltas from the shared core

| Area | Thailand | Work |
|---|---|---|
| Structure | OCS JSON API (Acts); BOT PDFs | มาตรา/ข้อ/วรรค parser (new; วรรค uses ordinal words not numbers) |
| Validity/relations | OCS: `stateId` (01 in force / 02 repealed) + `childrens` subordinate links; **BOT: the listing row's status icon `alt`** (`ยกเลิก` → revoked) — not supersession prose | OCS state maps to config; BOT reads the icon, so no prose inference is needed |
| Scope vocab | new Thai seed (`scope_term_th.csv`) | research + seed |
| Retrieval | segmentation problem above | lexical-arm design decision + TH router profile |
| Source access | OCS JSON API; BOT ASP.NET WebForms | OCS client (easy); BOT `__doPostBack` session handler |

## Recommended build order

1. **OCS** (Acts) first — JSON API, no protection, all target Acts confirmed, easiest.
2. **BOT** (notifications) — primary regulatory source, ASP.NET scraping but no WAF.
3. **ETDA** — zero crawl friction, high relevance (Digital ID, e-transactions).
4. **SEC** — search subdomain open, PDF download needs browser headers.
5. **Royal Gazette** — defer to Phase 2 (Cloudflare on HTML; PDFs open by sequential ID).

## Phased plan

Playbook template with one extra gate: **the lexical/segmentation decision (Hard part 1) is
settled before Phase 6 (index/serve)**. Parser-spike flagship: PDPA B.E. 2562 (OCS JSON API).
Deploy = `tomyum` DB + ECS container + CloudFront distribution + domain.

**External resource:** PyThaiNLP/thai-law on HuggingFace has 42,755 laws scraped from old
krisdika.go.th (Parquet) — potential bootstrap/validation source.
