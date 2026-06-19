# Sources — scope, discovery & per-source crawl

How banhmi decides **what to capture** (scope), **how it discovers** documents, and the **raw per-source
crawl/fetch mechanics**. Verified against the live sites in 2026-05; sites change, so keep each source
isolated in its own package (`pkg/ingest/{source}/`) and add contract tests.

The official sites serve **public government legal data** but some disallow `/api/` in `robots.txt` on
their www hosts. banhmi treats access as a compliance judgment: descriptive User-Agent, Temporal
activity caps for fetch concurrency, exponential backoff on 429/5xx, and raw payloads kept for
provenance. See [crawler etiquette](../ARCHITECTURE.md#crawler-etiquette-and-compliance). Tables:
[`SCHEMA.md`](SCHEMA.md); the workflows that drive the ledger: [`PIPELINE.md`](PIPELINE.md).

## What's in scope

Scope = **Vietnamese banking digital/technology regulation**: IT systems & security, cybersecurity,
data & personal-data protection, e-transactions & e-signatures, cloud, payments & intermediary payment,
digital banking, eKYC — **plus the cross-cutting central laws that bind banks** even though the State Bank
did not issue them (e.g. *Luật An ninh mạng*, *Luật Bảo vệ dữ liệu cá nhân*, *Luật Giao dịch điện tử*).

The keyword set and issuers below were compiled from multi-agent research against vbpl.vn / chinhphu.vn /
sbv.gov.vn (2026-05) and are implemented in [`pkg/scope`](../../pkg/scope/scope.go). **Keywords and
official relation targets are the durable crawl policy; doc-number lists are not scope policy.** Status
is driven by vbpl `effStatus` (CHL in force / HHL expired / CCHL not-yet-effective), **not** by year —
2025–2026 is a re-codification wave (cyber, personal-data, and IT-industry laws all being replaced), and
both the outgoing and incoming instruments are in scope when discovered by keyword or relation.

**Explicitly out of scope** (the precision boundary): lãi suất, dự trữ bắt buộc, tỷ lệ an toàn vốn /
prudential ratios, ngoại hối/tỷ giá/vàng, phân loại nợ / trích lập dự phòng (loan-loss, *not* DR/BCP),
kế toán, báo cáo thống kê.

## Two discovery axes

| | Axis A — SBV/NHNN corpus | Axis B — cross-cutting central law |
|---|---|---|
| Who issues | Ngân hàng Nhà nước (NHNN) | Quốc hội, UBTVQH, Chính phủ, Thủ tướng, Bộ Công an, Bộ KH&CN, Bộ TT&TT (+ BBCVT) |
| Discover by | issuer (`agencyIds`) **sweep** — whole SBV feed, no keyword | **keyword** title search across the non-SBV issuers (`config.discovery_keyword`) **+** relation graph |
| Precision | `scope.Match` drops out-of-scope NHNN docs (lãi suất, vốn…) | the keyword **title** match is the filter — the term must be in the title, so a Bộ Giáo dục circular that only mentions "an ninh mạng" in its body is **not** returned |

Axis B is keyword-driven but **agency-scoped and title-matched**, which is what keeps it precise: a
framework law like *Luật An ninh mạng* carries the term in its title, so a title search across the
non-SBV issuers returns it directly (`an ninh mạng` → 7), where a no-agency body search over-captures
(~397 docs including vocational-education standards). The relation graph (SBV docs cite them via
`references[]`) then enqueues cited targets as bounded leaf fetches.

## The scope matcher (`pkg/scope`)

`scope.Match(number, title, abstract)` returns in-scope + the matched terms (recorded in
`ingest.doc_discovery`); **strong** terms match số ký hiệu + title + abstract (vbpl `docAbs` body
text), while **strong_title** and **weak** terms match số ký hiệu + title only. It filters
the keyword-less feeds (the SBV sweep, congbao RSS, and the vanban listing); the Axis-B keyword search is
already filtered by its keyword, so it is not re-checked here. Terms are NFC-normalized + lower-cased,
**never diacritic-folded** (folding `an toàn`→`an toan` over-matches):

- **strong terms** — specific enough for any issuer (personal-data, cyber, e-transaction, payment,
  digital-banking, **AI** phrases — `trí tuệ nhân tạo`/`hệ thống trí tuệ nhân tạo` added 2026-06 so the
  local-filter feeds catch standalone AI laws). This is how Axis-B laws are caught regardless of issuer. Matched on số ký
  hiệu + title + body (`docAbs`), so a terse amendment whose body cites a framework law is caught.
- **strong_title terms** — specific enough for any issuer, but matched on số ký hiệu + title **only**,
  not the body — for terms whose body occurrences are mostly boilerplate: `chữ ký số` (e-filing clauses),
  `chứng thực chữ ký` (notarizing a translator's signature), `tài khoản thanh toán` (generic account
  references). An offline pass over stored `docAbs` showed body-matching these pulled ~18% of the SBV
  corpus in as off-topic (FX, lending, accounting) with no real recall gain — docs that use them
  substantively also match a body-eligible term and survive.
- **weak terms** — generic tech (`công nghệ thông tin`, `hệ thống thông tin`, `dữ liệu`, `chuyển đổi
  số`…); count only with a **banking signal** (an NHNN số ký hiệu, or `ngân hàng`/`tổ chức tín dụng`),
  so a health-IT or e-government doc is not pulled. Matched on số ký hiệu + title only — body text floods
  with these generic terms (e.g. "công nghệ thông tin" appears in ~189 of 2044 SBV bodies).

Tight phrases give precision without an exclude list: `an toàn thông tin` matches, bare `an toàn` (which
also appears in `tỷ lệ an toàn vốn`, capital adequacy — out of scope) is not a term. Posture is **broad
recall** (include borderline op-risk / AML-with-eKYC / credit-information / general data law); the
relation graph still pulls cited cross-cutting law.

**Discovery exclusions (document form, not topic).** Beyond topical scope, the Discover activity drops
docs by `config.setting` (`discover.exclude_doc_types`; `discover.exclude_eff_status` is available but
empty by default):

- **non-normative types** — `Chỉ thị` (exhortatory directives) and `Văn bản hợp nhất` (VBHN —
  *consolidations for reference only, NOT văn bản quy phạm pháp luật*; the binding text is the văn bản
  gốc + văn bản sửa đổi/bổ sung, which we keep). The corpus is then VBQPPL normative law only.
- **validity is kept** — expired, partial, and not-yet-effective documents can matter for amendment and
  repeal history. Retrieval gates current-law results with `in_force_only`; ingestion does not erase the
  legal history.

## Recall mechanisms

- **Discovery keywords** are the positive search policy for cross-cutting law and SBV Hanoi local
  filtering. Examples: `an toàn thông tin`, `giao dịch điện tử`, `luật dữ liệu`, `luật viễn thông`,
  `cơ chế thử nghiệm có kiểm soát`, `cho vay ngang hàng`.
- **Relation backfill** is the second wave: promoted official VBPL `references[]` targets from matched
  corpus docs become `ingest.fetch_doc` rows with `provenance='relation'`. Relation leaves are fetched
  for RAG evidence and legal history, but their own references are not recursively expanded.
  **English-translation renditions are skipped** — vbpl serves them under the `vbpqta_` external-id
  namespace (`target_id` like `vbpqta_11014`). A rendition is not a distinct legal document; discovery
  drops the `Bản dịch văn bản` type, and backfill skips the `vbpqta_` namespace so a referenced
  translation is never materialized as a standalone doc duplicating the real one.
- **Doc numbers** remain identifiers and citations only. They are not a crawler policy list.

## Discovery inputs → one ledger

Five inputs (+ manual) feed one ledger; every discovered document is then **fetched as text from the
congbao or vanban CDN and enriched from vbpl** (provision tree + relations + validity), reconciled into one
`silver.document`:

1. **congbao RSS (incremental signal)** — poll `cac-van-ban-moi-ban-hanh.rss` (50 newest, all issuers);
   `scope.Match` each by số ký hiệu + title; new in-scope docs → ledger. The cheap near-real-time trigger.
2. **vbpl Axis A (SBV corpus + backfill)** — one keyword-less `doc/all` sweep with `agencyIds:["62","908"]`,
   paged newest-first to watermark → `scope.Match` (title + `docAbs`) → ledger (`doc_discovery` records the
   matched terms). One sweep + local filter replaces the per-keyword fan-out.
3. **vbpl Axis B (cross-cutting)** — one title search per `config.discovery_keyword`
   across the non-SBV central issuers (`agencyIds` = Quốc hội/UBTVQH/CP/TTg/Bộ Công an/Bộ KH&CN/Bộ
   TT&TT/BBCVT); the keyword (title match) is the filter, so each returns only the framework laws
   themselves (`an ninh mạng` → 7) → ledger, provenance = the keyword. The **relation graph**
   (`documentRelatedList`/`references[]`) then enqueues cited targets.
4. **vanban (current central-law discovery)** *(verified 2026-06; source #2, after vbpl)* — the Government
   legal database `/he-thong-van-ban` (`classid=1` = VBQPPL), the freshest/broadest central feed: it carries
   new central laws **before vbpl indexes them** (e.g. `134/2025/QH15` Luật Trí tuệ nhân tạo). Daily: GET the
   newest-first listing → `scope.Match` (số ký hiệu + title + trích yếu) → ledger. Cold-start: walk the
   list via the GridView `Page$N` postback back to ~2018 (page-capped). ASP.NET, **no RSS/API**; runs alongside congbao.
   See the per-source section.
5. **SBV Hanoi support** — one broad portal sweep after VBPL; skip rows whose normalized `Số/Kí hiệu`
   already exists in VBPL, then local-filter the remaining title/number text with the same
   `config.discovery_keyword` set used by VBPL title searches → ledger, provenance = matched keywords.
   The portal's "Thể loại" field sometimes holds its browse *category* ("Pháp luật ngân hàng"), not a
   loại văn bản — only known doc-type names are accepted; otherwise the type is inferred from the số ký
   hiệu/title (a wrong type would split the document's silver identity away from other sources).
6. **manual folder** *(MVP2)* — scan an operator-configured directory; new files (sha256-deduped) → ledger
   (`source = manual`) → explicit Extract/Normalize/Index stages. For documents the crawler cannot reach;
   optionally vbpl-enriched by the số ký hiệu parsed from the file.

**Enrich + text (per document):** congbao CDN → authoritative born-digital DOCX/PDF (text of record);
vbpl `doc/all` by `docNum` → `id`, `effStatus`/`effFrom`/`effTo` (validity), `documentRelatedList`
(relations), `documentMajors` (topics), then the provision tree by `id` (Điều/Khoản structure). Dedupe
by số ký hiệu across inputs (`document_alias` records each observation).

## Per-source crawl

| Source | Access | Primary text | Structure | Relations | OCR |
|--------|--------|--------------|-----------|-----------|-----|
| congbao.chinhphu.vn | Server HTML + CDN file download | Born-digital DOCX/PDF via MarkItDown (9/10) | parse from text | partial | rare |
| vanban.chinhphu.vn | Server HTML (ASP.NET postback) + CDN file download | Born-digital PDF/DOCX via MarkItDown | parse from text | none | rare |
| vbpl.vn | JSON API (`vbpl-bientap-gateway.moj.gov.vn`) | `.docx` → HTML → `.doc` bridge → PDF/OCR | provision tree API | full graph | rare |
| sbv.hanoi.gov.vn | Server HTML + `/documents/` files | official PDF/DOCX via MarkItDown; DOC via LibreOffice bridge | parse from text | shallow | rare |
| phapluat.gov.vn *(MVP2)* | JSON API (`/api/legal-documents`) | HTML body (9/10) | parse from HTML | relation arrays | rare |
| manual folder *(MVP2)* | operator-dropped files | the provided PDF/DOCX/DOC | from explicit Extract/Normalize stages | — | if scanned |

State Bank of Vietnam (Ngân hàng Nhà nước) filter: congbao category `c7`; vbpl agency id `62`
(code `NHNN`). congbao, vbpl, and the SBV Hanoi/Region 1 legal-doc portal are official government
sources; vbpl also enriches each document with structure/relations/validity.

### congbao.chinhphu.vn — Official Gazette (Văn phòng Chính phủ)

Server-rendered HTML (OpenResty); document files served from the CDN `g7.cdnchinhphu.vn`. No
Cloudflare/captcha; no JavaScript needed for listing, detail, or download links. `robots.txt` is
permissive (`Allow: /`). Advanced search is JS-only and **not** server-queryable (no lĩnh vực filter);
drive congbao off vbpl's scope-filtered số ký hiệu list and fetch the authoritative gazette text by number.

- **Discovery — RSS (verified 2026-05; primary):**
  - `/cac-van-ban-moi-ban-hanh.rss` — 50 newest **documents**; `<link>` = detail URL with the trailing
    doc id, `<pubDate>` = watermark. `<title>` is empty, so parse số ký hiệu/type from the slug. SBV
    docs appear (e.g. `van-ban-hop-nhat-so-43-vbhn-nhnn-…`).
  - `/cac-so-cong-bao-moi-dang.rss` — 50 newest gazette **issues** (`<title>` = issue number).
  - Note: the bare `/rss` is an empty shell and `/rss.htm` is an HTML index page — use the two `.rss`
    feeds above.
  - **Fallback / backfill:** latest gazette issue `/cong-bao/{slug}-{issueId}.htm`, sequential doc ids
    (`/van-ban/x-{id}.htm` resolves by the trailing id), and the **path-paginated** SBV stream
    `/van-ban-dang-cong-bao/ngan-hang-nha-nuoc-viet-nam-c7/trang-{N}.htm` (10 docs/page; `?page` is
    IGNORED; newest-first).
- **Search by số ký hiệu (verified 2026-05; source backfill):**
  `POST https://api-searchcongbao.chinhphu.vn/search/van-ban/nhom/vbqpp` with
  `{"filters":{},"page":1,"page_size":10,"query":"14/2022/NĐ-CP"}`. Query with the exact Vietnamese
  `doc_number`, then verify normalized equality against `so_ky_hieu`, compatible issue date/type when
  known, and at least one official PDF/DOC/DOCX; unaccented queries can rank the wrong document first.
  Used only from Extract when an older VBPL row has placeholder/empty official content, so the ledger can
  enqueue the matching Congbao files without widening normal discovery.
- **Detail page:** `/van-ban/{slug}-{docId}.htm` (metadata + download links present server-side).
- **Files:** opaque signed CDN token, scraped from the detail HTML:
  `https://g7.cdnchinhphu.vn/api/download/stream?Url={token}&file_name={name}`. **PDF and DOCX** are
  both available — prefer DOCX for cleaner structure. Download with header
  `Referer: https://congbao.chinhphu.vn/` and a browser User-Agent.
- **Metadata:** số ký hiệu, trích yếu, loại văn bản, cơ quan ban hành, ngày ban hành, ngày hiệu lực,
  người ký, số/ngày công báo. Validity status is not reliably exposed here.
- **Extraction:** DOCX/PDF are born-digital Tier 0; convert through MarkItDown and normalize to NFC.
  Wide appendix tables still need QA, and PDF text must pass the quality gate before binding use.
- **Coverage:** gazetted documents (QPPL) only. Supplement with vbpl for non-gazetted
  circulars, validity status, and the amendment graph.

### vanban.chinhphu.vn — Government legal database (Văn phòng Chính phủ) *(source #2, after vbpl)*

The Cổng "Hệ thống văn bản" — the Government's full central VBQPPL database (verified 2026-06: 47,378 docs,
newest-first, top row dated *yesterday*). **Fresher and broader than the Công Báo gazette**, and it carries
brand-new central laws **before vbpl indexes them** (e.g. `134/2025/QH15` Luật Trí tuệ nhân tạo — absent
from vbpl, present here with its signed file). **Role:** current central-law **discovery + authoritative
file + core metadata** — *not* structure/relations (vbpl stays authoritative for those). Runs **alongside**
congbao, not replacing it. `robots.txt` is `Allow: /`.

ASP.NET WebForms — the document grid **hard-caps at 50 rows/page** (`drdRecordPerPage=500` is ignored,
verified) and **No RSS, no JSON API**. The only paginator that reproduces from a plain HTTP client is the
unfiltered GridView **`Page$N` postback**; the issuer-filtered search returns one page and then **does not
paginate** (page 2 returns empty, verified). So discovery is a keyword-less newest-first walk + `scope.Match`
— the vanban analogue of the congbao RSS — implemented in [`pkg/ingest/vanban`](../../pkg/ingest/vanban):

- **The walk:** `GET /he-thong-van-ban?classid=1&mode=1` (`classid=1` = VBQPPL, newest-first) for page 1,
  then POST `__EVENTTARGET=<grid>$grvDocument`, `__EVENTARGUMENT=Page$N`, carrying the prior response's
  `__VIEWSTATE`/`__EVENTVALIDATION` and the filter dropdowns at defaults (no search button — it resets
  paging). The grid control id is read from the page, not hardcoded. Parse each row's số ký hiệu
  (`span.code`, stripping a leading `NN.` grid-sequence prefix), issue date (`span.issued-date`), trích yếu
  (`span.substract`), and detail `docid`.
- **Scope + watermark:** the pipeline `scope.Match`es each row (số ký hiệu + title + trích yếu) and advances
  the per-source `discover_cursor`. **Incremental** (daily) stops at the watermark — page 1 is usually
  enough. **Cold-start** walks back to a year floor (`2018`) or a page cap (`coldStartMaxPages`, fits the
  Discover activity timeout); if the cap is hit before the floor, it **logs** the oldest date reached rather
  than truncating silently (older central law is already covered by vbpl).
- **Detail:** `GET /?pageid=27160&docid={id}&classid=1` (plain GET) → metadata (số ký hiệu, ngày ban hành,
  ngày có hiệu lực, loại văn bản, cơ quan ban hành, người ký, trích yếu) + attached file link. **No inline
  body text, no relation graph, no effStatus badge** — only issue/effective dates.
- **Files:** born-digital **PDF (often `…signed.pdf`) or DOCX** on the public CDN
  `datafiles.chinhphu.vn/cpp/files/vbpq/YYYY/MM/…`, scraped from the detail page — plain GET, no auth/referer
  (verified 200, `application/pdf`). Convert via MarkItDown → content gate → OCR floor (same born-digital
  cascade). The CDN omits a `robots.txt` (S3-style bucket); fetch politely with the descriptive UA.
- **Structure/validity:** provision tree **parsed from text** (no API tree); validity from the metadata
  dates + the enacting-clause rule. When vbpl later indexes the doc, enrich adds tree/relations/status and
  the rows reconcile by số ký hiệu (`document_alias`).

### sbv.hanoi.gov.vn — SBV Region 1 legal-document portal

Server-rendered Liferay portal operated by SBV Region 1. It is the SBV supplement for decisions and
attachments missing from vbpl/congbao.

- **Discovery:** query `/van-ban-quy-pham-phap-luat` without a keyword, using
  `_4_WAR_portalvbpqportlet_delta=200` and paging through `_4_WAR_portalvbpqportlet_cur=N`; rows expose
  `Số/Kí hiệu`, title, issue date, signer, and detail id `_4_WAR_portalvbpqportlet_id`.
- **Scope:** support-only. Discovery runs after VBPL, skips rows whose normalized `Số/Kí hiệu` already
  exists in VBPL, then filters the remaining title/number text with `config.discovery_keyword` (the same
  keyword set VBPL uses for title searches). The portal is small (verified at three pages in 2026-05).
- **Detail:** fetch the detail page by id and parse `Số/Kí hiệu`, issue/effective dates, signer,
  summary, issuer, type, and `Tài liệu đính kèm`.
- **Files:** direct `/documents/...` PDF/DOCX links; legacy `.doc` can use the LibreOffice PDF bridge
  when a source downloads it. Example verified: `2345/QĐ-NHNN` has official PDF
  `120240628145341_2345.pdf`; `2872/QĐ-NHNN` repeals it from `01/01/2025`.
- **Role:** supplement, not replacement. Prefer vbpl/congbao when present for national structure,
  relations, and gazette provenance; use SBV Hanoi to fill official SBV file gaps.

### vbpl.vn — National VBQPPL database (Bộ Tư pháp)

Rebuilt as a Next.js SPA over a JSON API gateway. No ASP.NET viewstate. Pages are client-rendered, so
**use the JSON API, not HTML scraping**. No Cloudflare/captcha. Files on FPT Cloud S3
(`s3-han02.fptcloud.com`, buckets `nts-vbpl`/`vbpl`). Send `Origin: https://vbpl.vn`.

- **Discovery (primary):** `POST https://vbpl-bientap-gateway.moj.gov.vn/api/qtdc/public/doc/all`
  ```json
  {"pageNumber":1,"pageSize":500,"sortBy":"issueDate","sortDirection":"desc","groupVbpl":false,
   "agencyLevel":"TRUNG_UONG","optionDoc":"title","matchMode":"all_words","agencyIds":["62","908"]}
  ```
  No `keyword` — the agency query returns the whole SBV feed; `scope.Match` filters it locally on title +
  `docAbs`. Sorted `issueDate desc`; page until you reach known ids or older dates. Returns
  `data.items[]`, `data.total`. `agencyIds` `62` = Ngân hàng Nhà nước, `908` = legacy "Ngân hàng quốc
  gia" (62-alone misses ~12 predecessor docs). **`fieldIds` (lĩnh vực) is ignored server-side** (verified:
  agency-only `total=2398`, +`fieldIds` still `2398`) — the agency filter is the engine. `groupVbpl:false`
  is a deliberate recall-first choice: `[62]`+`true`=2044 collapses version rows, `[62,908]`+`false`=2398
  returns them all (downstream dedupe-by-số-ký-hiệu drops true duplicates). For the **non-SBV
  cross-cutting issuers**, the same request adds `"keyword":"<term>"` and swaps `agencyIds` to the
  cross-cutting set — the title match is the filter (no local `scope.Match`); one request per keyword.
- **Filter vocabularies:** `GET /api/qtdc/public/doc/agency/combobox?type=TW`,
  `…/doc/combobox?groupCode=LoaiVanBan|LinhVuc|TrangThaiHieuLuc`, `…/doc/area/combobox?areaLevel=1`.
  The `lĩnh vực` vocab (`CNTT`, `ATTT`, `TMĐT`, `MM`, `Dữ liệu`…) is still useful for **tagging** fetched
  docs — just not for server-side filtering.
- **Backfill:** `https://vbpl.vn/sitemap.xml` (sitemapindex, ~55k central docs). The API leads the
  sitemap for brand-new documents.
- **Detail:** `GET /api/qtdc/public/doc/{id}` plus `GET /api/qtdc/public/doc/{id}/diagram` —
  `id` is a numeric ItemID or a **UUID** for the newest documents (store whatever the API returns).
  Detail returns metadata + `documentContent.content` (HTML body) + `references[]`; diagram returns
  a cleaner relation map by `referenceType`. Treat HTML as usable only after extracting non-empty body
  text; some VBPL rows expose `*_content.html` as an empty shell.
- **Structure/content:** `GET /api/qtdc/public/doc/provision/tree/{id}` — Chương → Điều → Khoản →
  Điểm with stable UUID node keys, `ptype`/`level`, order, and per-node `content.content` HTML text.
  Fetch stores this as `provision_tree_json`; Normalize uses it as the primary section source for VBPL
  docs, preserving `node_key` and `ptype`. Missing or empty trees fall back to text parsing and are
  rechecked later. `tree-only/{id}` is hierarchy-only and useful for cheap checks, not RAG text.
- **Files:** `GET …/doc/minio/buckets/vbpl/folders/{id}/files` (**without `?parts=1`**, which hides most
  files; also returns a 24h presigned S3 URL); download `…/buckets/vbpl/{id}/{fileName}/download`. Human
  URL: `https://vbpl.vn/van-ban/chi-tiet/{ItemID}` (tabs via `?tabs=hien-thi-pdf|luoc-do`).
  Treat `relatedType` as advisory only: `1` can be any official source file and `2` can be either the
  main legal file or an appendix. Classify by extension + filename, then extract by DOCX → HTML → DOC
  rendered to PDF → source PDF.
- **Metadata:** `docNum`, `title`, `issueDate`, `effFrom`/`effTo`, `publicDate`, `docType{name,code}`,
  `effStatus{name,code}`, `agencyName`/`organization`, `documentIssues[].personName` (người ký),
  `documentFields[]` (lĩnh vực), flags `isConsolidatedDocument`/`hasContent`.
  Validity codes: `CHL` in force, `HHL` expired, `HHL1P` partly expired, `CCHL` not yet in force,
  `TNHL`/`TNHL1P` suspended, `KCPH` no longer appropriate.
- **Relations (lược đồ):** merge `references[]` = `{referenceType:int, targetDocument{id,docNum,…}}`
  with `/diagram` `documentNamesByType` (the documented top-level `docRelateEffects[]` arrays are empty
  in practice; relations live in `_qtdcRaw.references[]`). `referenceType` int map: 10=amends, 12=replaces,
  7=consolidates, 3=basis, 6=corrects, 1/8=abrogates… Stored raw and mapped conservatively. Fetch
  persists `references_json`; Normalize writes confirmed `document_relation` rows only for VBPL structured
  references, and writes all text/fallback links as `weak_relation` evidence until a local classifier
  produces `model_classification` evidence.
- **Extraction/evidence:** For VBPL docs, prefer the provision tree for RAG sections. Still download the
  real `.docx`/`.doc`/PDF when present for provenance, comparison, and fallback. `.docx`/PDF convert
  through MarkItDown/OCR; legacy `.doc` renders through LibreOffice headless to PDF, then MarkItDown.
  The text-bearing `documentContent.content` HTML body remains the first fallback whole-document text
  after DOCX.

### phapluat.gov.vn — Cổng Pháp luật Quốc gia (Bộ Tư pháp) *(MVP2)*

Newer (2025) Next.js SPA + JSON API (engine "LEXcentra"); aggregates and links to vbpl/congbao rather
than replacing them. **Same qtdc backend as vbpl** (`docGUId` ≡ vbpl `id`, grouped/lossy view) —
**dropped for MVP1**; vbpl is the canonical structured source. Files on FPT Cloud S3
(`s3-han02.fptcloud.com/cplqg-vn/{uuid}.pdf`). reCAPTCHA gates only forms/login/AI chat — browse,
search, and detail APIs are open.

- **Discovery (primary):** `POST https://phapluat.gov.vn/api/legal-documents`
  ```json
  {"keywords":"","pageIndex":0,"rowAmount":100,"sortBy":"issueDate","sortOrder":"desc",
   "organIds":["62"],"docTypeIds":[],"effectStatusIds":[],"languageId":1}
  ```
  `pageIndex` is 0-based; `rowAmount` ≤ 100 (phapluat silently returns 20 for >100). `data.rowCount` ≈
  157k total. No auth/CSRF needed. Walk pages until older than the last-run watermark.
- **Filter vocabularies:** `GET /api/legal-documents/combobox?groupCode=LoaiVanBan|CoQuanBanHanh|LinhVuc|TrangThaiHieuLuc`,
  `…/agency-combobox?type=TW`, `…/area-combobox?areaLevel=1`.
- **Detail:** `GET /api/legal-documents/detail?docGUId={guid}` — `docIdentity` (số ký hiệu), `docName`,
  `docType`, `organs[]`, `issueDate`/`effectDate`/`expireDate`, `effectStatus`, `fields[]`,
  `signers[]`, `gazetteNumber`/`gazetteDate`, `docContent` (full HTML body, ~9/10), `docFiles[]`.
- **Relations:** `docRelateEffects[]` and `docListRelates[]` (present in every response; populated when
  a document amends/supersedes another).
- **Files:** the `related-file` proxy needs a session cookie (401 to plain curl); prefer direct S3
  URLs when present. Attachments are a mix of `.doc`/`.pdf` — test each (`pdftotext` char count → OCR
  if zero).
- **Extraction:** `docContent` HTML is primary (~9/10). Add heading detection from "Điều N"/"Chương
  N"/"Mục N" (the HTML uses bold spans, not `<h>` tags).

## Counting, downloading & relations

**Counting.** One filtered request returns the total (`data.total` on vbpl). Pin
`pageSize` ≤ 100, use `agencyIds:["62","908"]`, and `groupVbpl:false`. Verified NHNN counts
(each is one `data.total` call = the count, no enumeration):

| keyword | docs | | keyword | docs |
|---|------|---|---------|------|
| công nghệ thông tin | 206 | | chữ ký số | 112 |
| an toàn thông tin | 89 | | giao dịch điện tử | 108 |
| dữ liệu | 346 | | điện toán đám mây | 4 |
| ngân hàng số | 701 | | thanh toán | 1,008 |

Default scope = union of the **core tech** keywords (công nghệ thông tin, an toàn thông tin, an ninh
mạng, giao dịch điện tử, chữ ký số, điện toán đám mây, mật mã, dữ liệu); optionally add **digital
banking** (ngân hàng số, thanh toán điện tử, eKYC, Open API). These live in `config.scope_term`.

**Downloading (verified; one document = multiple files).**

| Source | How | Verified |
|--------|-----|----------|
| congbao | **PDF** via the direct `congbaocdn.chinhphu.vn/.../{docId}-..._signed.pdf` (reachable, token-free); **DOCX** only via g7 `…/api/download/stream?Url={opaque token}` — the token **expires**, scrape it at discovery. | PDF 200, 649 KB, born-digital, text extracts |
| vbpl | List files via `…/doc/minio/buckets/vbpl/folders/{id}/files` (**without `?parts=1`**) → download via the 24h **presigned S3** URL or the non-expiring gateway `…/{id}/{fileName}/download`. | file 200/206 |
| sbv_hanoi | Detail pages link direct `/documents/{group}/{folder}/{file}/{uuid}` attachments. | `2345/QĐ-NHNN` PDF 200 `application/pdf` |
| vanban | Direct CDN file `datafiles.chinhphu.vn/cpp/files/vbpq/YYYY/MM/{name}.signed.pdf` scraped from the detail page; plain GET, no token/referer. | `luat134.signed.pdf` 200, `application/pdf`, 1.06 MB |

One document carries **multiple files** (main PDF + .doc/.docx + appendices + content HTML) → modeled
as `fetch_doc` → many `fetch_artifact`.

**g7 CDN TLS quirk (verified 2026-05):** `g7.cdnchinhphu.vn` serves an **incomplete certificate chain** —
only its leaf cert, omitting the `GlobalSign RSA OV SSL CA 2018` intermediate that `congbao.chinhphu.vn`
*does* send. Browsers auto-fetch the intermediate (AIA); strict clients (Go) fail with "certificate
signed by unknown authority". It is a server misconfiguration, not a block on us. banhmi completes the
chain the way browsers do — **AIA chasing**: it reads the missing issuer's URL from the leaf's Authority
Information Access extension (`x509.Certificate.IssuingCertificateURL`), fetches and caches that
intermediate, then verifies. Implemented with the standard library only in `pkg/ingest/congbao/tls.go`.
The leaf still must chain to a system root and the hostname is still checked, so nothing is weakened.

**Relations & predecessors (don't miss).** Relations are in vbpl `_qtdcRaw.references[]`, with the
`referenceType` int map (10=amends, 12=replaces, 7=consolidates, 3=basis, 6=corrects, 1/8=abrogates…).
Relation targets often fall **outside** the tech scope (laws, decrees) — enqueue them so the graph is
complete.

## Cross-source strategy

- **Deduplicate** by số ký hiệu (`docNum`/`docIdentity`) + issuer + issue date. Prefer the best text
  source (HTML or born-digital), and **union** metadata and relations across sources.
- Use **vbpl** for authoritative files/HTML plus the provision tree, relation graph, and validity status
  (richest); **vanban** (source #2) for the freshest central-law discovery + authoritative file + metadata
  — it carries new laws before vbpl; **congbao** for authoritative gazetted DOCX/PDF and the RSS signal;
  **phapluat** as a broad aggregator with relations (MVP2 only).
- The State Bank is currently the most active central publisher, which suits the banking focus.

## Pinned defaults (stable issuer codes & endpoints)

Government issuer codes rarely change, so these are pinned as code defaults (operator-overridable):

| Source | Issuer | Code |
|--------|--------|------|
| congbao (path `…-c{N}.htm`) | Chính phủ / Thủ tướng / **NHNN** / Bộ KH&CN / Bộ Công an / UBTVQH / Quốc hội | `c1` / `c2` / **`c7`** / `c13` / `c26` / `c28` / `c31` |
| vbpl (`agencyIds`) | **Ngân hàng Nhà nước** (current / legacy "Ngân hàng quốc gia") | **`62`** / `908` |

congbao `c7` listing is **path-paginated** (`…-c7/trang-{N}.htm`, 10/page; `?page` is ignored), newest-
first. vbpl discovery: `POST …/api/qtdc/public/doc/all` with `optionDoc:"title"`, `matchMode:"all_words"`,
`sortBy:"issueDate"`, `sortDirection:"desc"`, **no `keyword`** (whole-agency sweep; `groupVbpl:false`);
item fields `docNum`, `title`, `docAbs`, `effStatus`, `effFrom`/`effTo`, `documentRelatedList`,
`documentMajors`, `id`; **`fieldIds` ignored**.
