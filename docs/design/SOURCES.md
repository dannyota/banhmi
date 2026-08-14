# Sources and discovery

This document defines crawl scope, the discovery decision, current source wiring, and source-specific
failure lessons. Site observations are dated because official sites change. The Go packages and their
contract tests are the current executable behavior.

Each source lives in `pkg/ingest/{source}/`. Shared ledger behavior is described in
[PIPELINE.md](PIPELINE.md), tables in [SCHEMA.md](SCHEMA.md), and per-country detail in the
[jurisdiction index](jurisdictions/README.md).

## Scope

banhmi covers banking and financial regulation plus technology law that affects finance. Technology
scope includes data protection, cybersecurity, electronic transactions and signatures, cloud,
payments, digital banking, eKYC, artificial intelligence, and technology operations. Cross-cutting
national laws can be in scope even when a non-financial agency issued them.

Each jurisdiction selects a scope seed through `pkg/base/jurisdiction`. The files are
`deploy/seed/scope_term*.csv`. Terms use the corpus language; Cambodia's corpus-language terms are
English even though Khmer is binding. The current keyword-search policy is in
`deploy/seed/discovery_keyword.csv` and applies only to VBPL.

Document numbers identify and cite documents. They are not a maintained crawl allowlist. Source status
and relation evidence are retained even for expired or not-yet-effective documents; retrieval decides
which validity classes count as current law.

Vietnam has one deliberate exception to vocabulary filtering: every document in VBPL's State Bank
agency sweep is in scope. `vbpl.Source.SweepInScope()` marks that empty-keyword feed as structurally
pre-scoped. It includes prudential, foreign-exchange, accounting, and VBHN documents, not only
technology topics.

## Discovery decision

`pipeline.Discover` applies exactly one of these rules:

| Slice | Rule | Current use |
|---|---|---|
| Trusted sweep | Enqueue every document without `scope.Match`; provenance is `sweep` | Empty-keyword VBPL State Bank sweep |
| Trusted keyword slice | Trust the source's server-side filter; provenance is the keyword | Non-empty VBPL searches over configured non-SBV agencies |
| Local-filter sweep | Run `scope.Match(number, title, abstract)` | Every other current source |

A keyword slice bypasses the local matcher. A source must not receive keyword rows until its filter has
been verified as a precision boundary. A filter that silently ignores or broadens a query would admit
its entire response as in scope.

The default is therefore a sweep plus local matching. Keyword discovery is reserved for a general law
database that is too large to sweep and has a trustworthy server-side title filter.

### Scope matcher

`scope.Match` normalizes text to Unicode NFC and lower case. It does not remove diacritics. It uses four
term classes:

| Class | Fields | Verdict |
|---|---|---|
| `strong` | number, title, abstract | A match is enough |
| `strong_title` | number, title | A match is enough; abstract boilerplate cannot trigger it |
| `weak` | number, title | Requires a `signal` match too |
| `signal` | number, title | Qualifies a weak topic term; it is not sufficient alone |

Tight phrases avoid broad exclusions. For example, `an toàn thông tin` is specific, while bare
`an toàn` would also match capital-adequacy text. Procedural phrases such as generic amendment wording
must not be `strong`; they pull unrelated sectors from national-law feeds.

The matcher is loaded from `config.scope_term` for each Discover call. Operator changes therefore take
effect without restarting the process.

### Discovery exclusions

Discover applies two comma-separated settings before scope:

- `discover.exclude_doc_types`
- `discover.exclude_eff_status`

The current shared seed excludes `Chỉ thị` and `Bản dịch văn bản`. It does not exclude any validity
status. VBHN (`Văn bản hợp nhất`) is not excluded.

## Current source registry

`BANHMI_JURISDICTION` selects one disjoint source map. The Source column is the stable ledger source ID.
Its package directory has the same name except for `sbv_hanoi` (`pkg/ingest/sbvhanoi`) and `cdc`
(`pkg/ingest/cdcgov`).

| Jurisdiction | Source | Current discovery and access behavior |
|---|---|---|
| VN | `congbao` | 50-item document RSS feed, then local scope match; official files and exact-number fallback search |
| VN | `vbpl` | Pre-scoped State Bank sweep plus configured non-SBV title searches; tree, status, and structured relations; `BANHMI_VBPL_PROXY_URL` when the worker lacks Vietnamese egress |
| VN | `vanban` | Newest-first ASP.NET listing walk, then local scope match; official central-government files |
| VN | `sbv_hanoi` | Full SBV Region 1 portal walk, then local scope match; supplemental official files |
| MY | `agclom` | Full principal-Act JSON feed; Act detail supplies validity and P.U.(A)/(B) relation evidence |
| MY | `bnm` | Five banking/finance sector listings, then local scope match |
| MY | `sc` | Four technology/digital sections, then local scope match |
| ID | `bpk` | All 13 configured `jenis` listings; keyword requests are rejected |
| ID | `bi` | Bank Indonesia regulation feed; optional `BANHMI_BI_PROXY_URL` |
| ID | `ojk` | JDIH OJK API; wired only when `BANHMI_OJK_PROXY_URL` is set |
| ID | `ojkweb` | Public OJK SharePoint catalogue; direct by default, with the same proxy optional |
| SG | `sso` | Singapore Statutes Online consolidated Acts |
| SG | `mas` | Monetary Authority notices and guidelines |
| SG | `pdpc` | Personal Data Protection Commission advisory guidelines |
| SG | `csa` | Cyber Security Agency publications |
| TH | `ocs` | Office of the Council of State structured law API |
| TH | `bot` | Bank of Thailand notification/circular portal |
| TH | `etda` | Electronic Transactions Development Agency regulations |
| TH | `sec` | SEC NRS listings and PDFs; browser headers are required, proxy is optional |
| KH | `nbc` | Four English pages; English-file filter; `BANHMI_NBC_PROXY_URL` for geo-blocked egress |
| KH | `serc` | Five English board listings |
| KH | `cdc` | One laws-and-regulations page; each PDF URL is also its detail URL |
| KH | `odc` | Open Development Cambodia CKAN feed; supplementary |

Country-specific contracts and known gaps live in
[MALAYSIA.md](jurisdictions/MALAYSIA.md),
[INDONESIA.md](jurisdictions/INDONESIA.md),
[SINGAPORE.md](jurisdictions/SINGAPORE.md),
[THAILAND.md](jurisdictions/THAILAND.md), and
[CAMBODIA.md](jurisdictions/CAMBODIA.md).

## Relation intake and backfill

Fetch stores source relations in Bronze. Normalize writes `silver.relation_evidence` first. VBPL and
AGC LOM structured relations promote directly because they come from dedicated official relation
feeds. Other source metadata promotes only when its exact `config.relation_type` row is marked
`is_amending`. Text-derived references remain weak.

Promotion does not prove legal truth. It records that a configured source asserted the edge. A
2026-07-26 audit found that four of seven VBPL `amends_supplements` targets attached to
`143/2025/QH15` appeared only in recitals. Consumers must keep source authority and evidence snippets
visible.

Promoted structured relations can pull a target into the ledger when all these conditions hold:

- the jurisdiction allowlists the source;
- the relation carries a source-native target ID; the immediate Normalize path also requires a
  document number, while the maintenance pass can fall back to the stored label or reference key;
- the target is not already resolved;
- the acting document was not itself fetched through relation backfill.

Vietnam allowlists `vbpl`. Indonesia allowlists `bpk`. Other jurisdictions currently allowlist no
source. This makes relation targets bounded leaves rather than a recursive graph crawl.

VBPL English renditions use the `vbpqta_` external-ID namespace. Backfill skips them so a translated
rendition does not become a duplicate legal document.

## Vietnam

### Discovery modes

| Mode | Issuers | Discovery | Local matcher |
|---|---|---|---|
| State Bank corpus | VBPL agency IDs `62` and `908` | One empty-keyword sweep | Skipped; feed is pre-scoped |
| Cross-cutting central law | Configured non-SBV central agencies | One `all_words` title search per discovery keyword | Skipped; trusted title filter is the scope decision |
| Other VN sources | Their full bounded/newest feed | RSS or listing walk | Required |

The VBPL agency IDs come from `config.issuer_code`, not Go constants. If no non-SBV IDs are configured,
VBPL refuses keyword searches instead of searching every central issuer.

### VBHN consolidations

VBHN documents are official consolidated texts and are indexed as primary documents.

- VBPL's State Bank sweep admits them without vocabulary filtering.
- `vbhn-nhnn` is also a `strong_title` term, so other VN sources can match them.
- The `VBHN-` number suffix overrides a source's wrong document-type label.
- When an issue date exists, Silver identity includes the year because some VBHN numbers repeat.
- The VN family pass mirrors the base document's current status onto the newest consolidation, marks
  older consolidations expired, and leaves an unresolved family unknown.

The original instrument and amendments remain in the corpus. The consolidation is readable primary
text; it does not replace the binding originals.

### `congbao`

The main discovery endpoint is `/cac-van-ban-moi-ban-hanh.rss`. It exposes the newest 50 documents.
The title can be empty, so discovery derives the document number and type from the detail-page slug and
uses the description as the title.

The exact-number fallback posts to `https://api-searchcongbao.chinhphu.vn/search/van-ban/nhom/vbqpp`.
It is used only when VBPL extraction proves its source content unavailable. The result must match the
normalized number, compatible date/type metadata, and at least one official file.

Files can come from the g7 CDN with an opaque token. The g7 server was observed in 2026-05 serving an
incomplete certificate chain. `pkg/ingest/congbao/tls.go` follows the certificate's Authority
Information Access URL, caches the missing intermediate, and still verifies the hostname and system
root. It does not disable TLS verification.

### `vanban`

The government legal database is an ASP.NET WebForms site. The list hard-caps at 50 rows per page.
Discovery uses the grid's `Page$N` postback and carries `__VIEWSTATE`, `__EVENTVALIDATION`, and default
filter controls forward. The issuer-filtered search was not usable as a paginated crawl.

Cold start stops at year 2018 or 240 pages. Incremental discovery walks 180 days past the watermark and
caps at 40 pages. The overlap catches rows updated or republished after their original issue date.

Detail pages supply metadata and a direct official PDF or DOCX link. They do not supply a provision
tree, relation graph, or validity-status badge.

### `sbv_hanoi`

The Liferay listing uses a page size of 200 and a newest-first issued-date watermark. It supplements
VBPL and Congbao with State Bank decisions and attachments.

The portal's category field is not always a legal document type. Only known types should influence
Silver identity; otherwise the number/title parser supplies the type. This avoids splitting one
instrument across source aliases.

### `vbpl`

VBPL is a client-rendered site backed by the Ministry of Justice JSON gateway. The crawler uses the API,
not page HTML.

Discovery posts to `/api/qtdc/public/doc/all` with 50 rows per page, issue-date descending,
`groupVbpl=false`, `optionDoc=title`, and `matchMode=all_words`. The State Bank sweep uses agency IDs
`62` and `908` with no keyword. Cross-cutting searches replace those IDs with the configured non-SBV
set and add one keyword.

Detail and structure use:

- `/api/qtdc/public/doc/{id}`
- `/api/qtdc/public/doc/{id}/diagram`
- `/api/qtdc/public/doc/provision/tree/{id}`
- `/api/qtdc/public/doc/minio/buckets/vbpl/folders/{id}/files`

The provision tree has stable node IDs and raw `ptype` values. Empty trees fall back to text parsing and
are eligible for later recheck. The file-list request must omit `?parts=1`, which was observed hiding
files. File URLs may be 24-hour presigned S3 links; the gateway download path is the durable fallback.

`references[]` and the diagram payload are structured relation inputs. Raw relation codes are retained
so mapping changes can be recomputed without crawling again.

## Indonesia: why BPK is sweep-only

`bpk.Discover` rejects every non-empty keyword. The current sweep covers 13 `jenis` codes:

| Scope rule | Codes |
|---|---|
| Regulator or security mandate | `80` POJK, `212` SEOJK, `78` PBI, `54` BSSN, `83` LPS, `81` and `221` PPATK |
| Broad mandate; vocabulary-filtered | `8` UU, `10` PP, `11` Perpres, `42` PMK, `106` Kominfo, `278` Komdigi |

PBI is a fallback for older Bank Indonesia files that return 404 from the primary source.

Two live-site findings from 2026-07-13 make BPK keyword discovery unsafe:

1. The site silently ignores the wrong parameter name. `keyword=` returned the whole listing; the real
   parameters were `keywords=` and `tentang=`.
2. Multi-word searches use OR-like matching. `bank indonesia` can match a title that contains only
   `indonesia`.

This once combined with the pipeline's trusted-keyword rule and admitted whole national-law listings.
The measured polluted corpus was 68% irrelevant national law: 3,533 UU documents and 77,627 chunks,
including 365 regional-creation laws. A sweep plus local matching is also cheaper: about 1,400 listing
pages versus about 9,000 across overlapping keyword slices.

BPK incremental discovery is year-windowed because listing cards expose a regulation year rather than a
precise publication timestamp. It crawls from `since.Year()-1` through the current year. A full rescan is
required when BPK backfills an older year.

## Access and failure lessons

- **OJK JDIH:** `ojk` is omitted from the ID source map unless `BANHMI_OJK_PROXY_URL` exists.
  `ojkweb` remains available directly.
- **Thai SEC:** PDFs are not geo-blocked. The F5 endpoint rejects non-browser clients. The source sends
  browser `User-Agent`, `Accept`, `Accept-Language`, and `Referer` headers. A proxy remains optional.
- **Cambodia NBC:** crawl only the four `/english/` paths and reject `_kh/`, `_kh.`, and `-kh.` file
  markers case-insensitively. A PDF URL is the document's `DetailURL` because NBC has no detail page.
- **Cambodia CDC:** the same direct-PDF detail contract applies.
- **Partial fan-outs:** `bpk`, `bnm`, and `sc` return successful sub-results together with a non-nil
  error when any sub-unit fails. The ledger records the partial haul but holds the cursor.

## Cross-source identity and authority

Bronze keeps every source observation. Silver merges observations through `document_alias` and the
canonical `doc_key`; it does not union them in Bronze.

The normal key is normalized type plus normalized number. Source-specific repairs handle type drift and
repeating VBHN numbers. Issuer and issue date help evaluate metadata and fallback matches, but they are
not part of the normal Silver key.

Text, metadata, structure, relations, and validity can come from different observations:

- VBPL is the preferred VN source for provision trees, structured relations, and status.
- Congbao supplies gazetted official files and the short RSS signal.
- Vanban supplies fresh central-government files before VBPL catches up.
- SBV Hanoi supplies State Bank gaps.

Normalize's metadata-priority selector decides which observation writes the current structure and
validity. Extract preserves each source text and authority separately.

## Deferred sources

The following researched sources are not wired into `pkg/app` and are not current behavior:

- `phapluat.gov.vn`, a grouped view over the same Ministry of Justice backend as VBPL;
- a manual-folder source for operator-provided files;
- `sbv.gov.vn` as an additional State Bank source.

They remain MVP2 candidates in [PLAN.md](../../PLAN.md). Do not describe them as active discovery inputs
until a source package is wired and covered by contract tests.
