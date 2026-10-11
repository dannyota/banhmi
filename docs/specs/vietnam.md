# Vietnam country module

Status: Proposal. Not approved, not implemented.

## Purpose and boundaries

Build Vietnam first on the new country-agnostic corpus core. Reuse useful v1 code and evidence.
This is a rebuild design, not a plan for small fixes to the running v1 corpus.
Tra cứu quy định at `tracuuquydinh.vn` serves evidence. banhmi remains the engine and corpus name.
The Vietnam module supplies sources, scope, legal grammar, citations, language policy, and evaluations.
The core owns execution, storage, conversion machinery, retrieval, releases, and transport.
Malaysia, Singapore, Indonesia, Thailand, and Cambodia migrate as `migrated-disabled` only after Vietnam’s write path runs end to end on GitHub-hosted CI.
Their migration never blocks a Vietnam milestone. Once migrated, those modules compile and run unit tests, but the new system never runs or serves them.
Their existing AWS services and databases remain unchanged. Vietnam also uses isolated candidate data until an authorized cutover.

The eight sibling specs own shared contracts: [write path](corpus-write-path.md), [workflows](corpus-workflows.md), [conversion](document-conversion.md), [parts](document-parts.md), [database](corpus-database.md), [embedding](embedding-models.md), [hosting](vngcloud-hosting.md), and [review](corpus-review.md).
The owner has settled Temporal Cloud with one AWS writer per country dataset, Hugging Face Jobs for bulk embedding, Google Cloud Vision OCR with original/OCR backups in S3, and one query embedding model on a small dedicated VNGCloud host.
Run no answer, generative, or reranker model. Questions and personal data stay in Vietnam.
Public availability does not clear a document for foreign processing. Require residency admission before foreign capture or processing.
An unclassified or personal-data-bearing artifact stays excluded pending the shared residency procedure; never silently redact canonical evidence.
Review API placement, private-CA mutual TLS, human authority, and identity/audit boundaries follow [corpus-review.md](corpus-review.md). Defer the console.
Only `accepted` and `accepted_with_gaps` revisions enter builds. Reprocessing needs comparison and required adjudication.
Retain the active release plus at least two prior releases for at least 30 days, and all explicit pins or holds.
Before changing the sole query model, re-embed retained dense-search releases or retire their dense search with a visible gap.
Retired releases remain readable through `document` and lexical search. No Vietnamese question reaches foreign embedding, OCR, or workflow history.

## Module contract

Use one versioned `vn` descriptor and immutable policy bundles under [country modules](country-modules.md#package-boundaries-and-registration).
Implement `internal/country/vn/Entry()` and worker-only `corpus/Factories()` with the sibling's consumer-owned interfaces.
Keep Vietnam disabled during construction; `enabled` requires qualification and runtime admission. Other modules stay `absent` until migrated, then `migrated-disabled`.
Country logic must not import another country module, fetch bytes, or commit database writes. Bind pure request planners and sandbox parsers through core source factories.
Reject an explicit disabled or unknown country before opening a source, database pool, workflow, or serving route.
An omitted country resolves to Vietnam. Compilation and offline unit fixtures do not enable a country.

| Module contribution | Input and output contract |
|---|---|
| Descriptor | `vn`, language `vi`, legal timezone `Asia/Ho_Chi_Minh`, source IDs, enabled capability, policy/grammar/taxonomy digests, guide strings, golden manifest |
| Sources | Pure request plans to core fetch activities; immutable raw capture references to sandbox listing/detail parsers; typed observations, artifacts, assertions, follow-up requests, cursors, and completion/gap receipts out |
| Scope and subjects | Source classification, title, number, and eligible text to admission reason, primary/context role, subject assertions, and evidence spans |
| Identity | Raw metadata and parsed number to typed candidates, source aliases, ambiguity findings, and reviewed identity assertions |
| Structure and citations | Conversion blocks and source-tree observations to Vietnamese body grammar decisions; parts supplies namespaces; core assigns final ownership |
| Relations and validity | Acting blocks, typed identity candidates, exact targets, and commencement evidence to immutable assertions and proposed intervals |
| Language | Bounded query plus frozen lexicons to accepted Vietnamese/identifier input or a deterministic error; no corpus lookup during validation |
| Evaluation | Versioned fixture manifests, expected citations/targets/dates, cohort policy, and unchanged retrieval floors |

Pin all module digests in intake recipes and release manifests. A taxonomy, parser, or dictionary change creates a new recipe.
The core executes `SourceSync`, `DocumentIngest`, `OcrBatch`, `Reprocess`, `CorpusBuild`, and `Promote` under the sibling contracts.
Source adapters split network from parsing under the [shared source contract](country-modules.md#package-boundaries-and-registration). Vietnam plans bounded requests without I/O.
The core fetch activity enforces source politeness and captures raw responses to bronze with hashes, byte counts, and immutable capture references; it does not parse response bodies.
The core mounts verified captures in the network-denied converter sandbox. Vietnam listing/detail parsers consume those bytes and return typed records, continuation data, and follow-up requests.
The core validates parser outputs and admits each follow-up request under the same source policy and ledger. No country code both fetches and parses; pure grammar over validated blocks can run on CPU workers.
Country outputs retain raw spans, artifact hashes, authority, confidence scale, gaps, and review state. Confidence never proves legal effect.

## Scope and subjects

Include issued Vietnamese banking and finance instruments and technology law that governs those activities.
Keep historical and future-effective instruments for date-aware evidence. Admission does not mean current force.
Use the following proposed subject keys within the database's versioned taxonomy. A document can carry several subjects.

| Subject key | Included matters |
|---|---|
| `banking` | State Bank powers, credit institutions, licensing, governance, prudential duties, credit, deposits, supervision, deposit insurance |
| `payments` | Accounts, cards, payment intermediaries, electronic money, clearing, settlement, digital banking, electronic customer identification |
| `financial-integrity` | Anti-money-laundering, counter-terrorist-financing, customer identification, financial reporting duties |
| `financial-markets` | Foreign exchange, securities, market conduct, and investment rules relevant to regulated financial activity |
| `data-protection` | Personal data, confidentiality, lawful processing, banking data exchange, and cross-border data duties |
| `cybersecurity` | Information-system classification, cyber and information security, incident response, resilience, technology risk |
| `digital-operations` | Electronic transactions/signatures, cloud and outsourcing, APIs, AI governance, technology operations in finance |

General tax disputes, ordinary insurance claims, unrelated corporate/labor/land law, news, commentary, and commercial translations are out.
Preserve the insurance boundary recorded in [PLAN.md:128](../../PLAN.md#L128); deposit insurance remains included.
Drafts, consultations, unsigned templates presented as proposed instruments, and individual personnel decisions cannot enter enacted-law results.
Keep blank issued legal forms in scope. Filled forms, user uploads, account data, and private correspondence are outside the corpus.
An official guidance letter can be context, with its document class explicit; its hostname or type alone does not make it a general legal rule.

Reuse versioned vocabulary and issuer rules, with source evidence for each admission. A title match is a candidate, not a subject assignment for every clause.
The State Bank sweep discovers broadly; reject unrelated administrative material at admission. Do not impose an arbitrary historical cutoff on applicable law.
Keep entire admitted originals. Label relevant provisions primary and cross-subject or out-of-scope dependencies context; do not truncate source files.
Follow evidenced amendment/repeal targets outside primary scope through bounded manifests. Record unresolved closure instead of recursive unlimited crawling.
Use official classification, deterministic rules, or human review for subjects. Preserve unknown, unreviewed, and reviewed-not-applicable states.
Apply subject filters before candidate limits in both retrieval arms. Return possible matches separately; strict filters disclose classification gaps.
Always attach necessary amendment and validity context regardless of subject. Scope exclusions and missing coverage are distinct reasons for abstention.

## Sources and authority

The v1 builder wires `vbpl`, `congbao`, `vanban`, and `sbv_hanoi`: [app.go:371](../../pkg/app/app.go#L371).
The table describes coded collectors, not newly verified source uptime. Cadences and rate limits below are proposals.
No source has blanket authority over another. Issued originals establish wording; acting provisions and commencement clauses support legal effects.
Gazette publication establishes publication evidence. Metadata, trees, status badges, and relation feeds remain source assertions.

| Source | Evidence and discovery | Reliability and proposed cadence |
|---|---|---|
| `vbpl.vn` / `vbpl` | Ministry of Justice gateway: `/doc/all`, 50-row pages, descending issue date, configured State Bank agencies `62`/`908`, then scoped central-issuer title searches. [discover.go:82](../../pkg/ingest/vbpl/discover.go#L82). Detail, diagram, files, HTML, status, and consolidation fields: [detail.go:106](../../pkg/ingest/vbpl/detail.go#L106), [detail.go:139](../../pkg/ingest/vbpl/detail.go#L139). Tree decoding: [vbpl_tree.go:43](../../pkg/pipeline/vbpl_tree.go#L43). Preferred structured metadata/relations, subject to text checks. | Issue-date watermarks can miss old updates. Empty trees and expiring file links need explicit gaps/rechecks. Daily discovery with overlap; weekly known-document metadata/relations refresh; rolling monthly historical reconciliation. |
| `congbao.chinhphu.vn` / `congbao` | Gazette RSS discovery: [discover.go:39](../../pkg/ingest/congbao/discover.go#L39). Gazette/date metadata and official downloads, including VBHN when published: [detail.go:81](../../pkg/ingest/congbao/detail.go#L81). No body/tree/relation feed in this collector. Exact-number search fallback: [search.go:16](../../pkg/ingest/congbao/search.go#L16). | RSS is a recent signal, not historical completeness. Poll every four hours; daily reconcile missing official files by exact identity. Tokens/CDN failures retain retryable gaps. TLS chain repair still verifies trust and hostname: [tls.go:18](../../pkg/ingest/congbao/tls.go#L18). |
| `vanban.chinhphu.vn` / `vanban` | Central-government metadata and signed PDF/DOCX, including consolidation publications when listed. WebForms postback crawl, 50-row pages; v1 caps cold start at 2018/240 pages and increments at 40 pages with 180-day overlap: [discover.go:14](../../pkg/ingest/vanban/discover.go#L14). File adapter: [detail.go:95](../../pkg/ingest/vanban/detail.go#L95). No structured tree or relation feed. | Daily incremental crawl; weekly overlap reconciliation. Preserve session/form state. Hitting a cap means incomplete coverage, never successful exhaustion. Historical gaps can use VBPL/gazette evidence without claiming a complete Vanban archive. |
| `sbv.gov.vn` / proposed `sbv` | Direct State Bank publications, issuance notices, original attachments, and official consolidated texts are the intended coverage. No v1 collector is wired in [app.go:371](../../pkg/app/app.go#L371). The [official portal](https://sbv.gov.vn/vi/trang-chu) mixes news and administrative material with legal notices; a notice is only a discovery pointer. Qualify the Vietnamese legal listing, detail IDs, pagination, attachment URLs, and VBHN categories before registration. | New adapter, reliability and complete endpoint contract unverified. Proposed daily discovery and weekly reconciliation after qualification. Do not assume a relation API, status feed, or compatibility with SBV Hanoi. A failed qualification leaves a named source gap. |
| `sbv.hanoi.gov.vn` / `sbv_hanoi` | Supplemental State Bank decisions/files. Liferay legal listing, 200 rows/page and issued-date watermark: [client.go:1](../../pkg/ingest/sbvhanoi/client.go#L1), [discover.go:16](../../pkg/ingest/sbvhanoi/discover.go#L16). Detail metadata and PDF/DOCX: [detail.go:39](../../pkg/ingest/sbvhanoi/detail.go#L39). No dedicated structured relations/tree or consolidation feed; recognize VBHN by issued evidence when encountered. | Daily discovery with overlap; weekly refresh. Portal category can misstate type, and the collector skips legacy DOC: [detail.go:71](../../pkg/ingest/sbvhanoi/detail.go#L71), [detail.go:107](../../pkg/ingest/sbvhanoi/detail.go#L107). Source location does not make a central instrument local law. |

For VBPL, retain opaque IDs, raw relation codes, and full captured payloads. Never scrape translated `vbpqta_` renditions into the Vietnamese corpus.
Keep source URLs and resolved capture URLs separately. Expiring URLs are transport data, not artifact identity or public durable links.
Inventory every attachment, even formats the converter cannot read. Replace collector format skipping with captured-file or explicit unsupported-file outcomes.
For identical legal versions, prefer qualified DOCX, official HTML, legacy DOC, then born-digital PDF. Keep scans and all conflicting renditions.
Format preference cannot resolve a wording conflict. Verify issue identity and parent association before joining files from different sources.
Structured status and relations never overrule conflicting originals. A gazette or issuing-body publication supports an independent cross-check.

Start with one request in flight per source host and at least one second between requests, shared across workers, listings, details, and downloads.
Use explicit per-host budgets for approved attachment/CDN hosts. Check robots and published access rules before enabling each source.
Use the [core stage retry contract](corpus-write-path.md#stage-contract): the ledger owns five source attempts over 24 hours, recorded `NextEligible`, preserved `Retry-After`, and bounded jittered backoff.
No overlapping Temporal source retry or inner v1 source retry loop is allowed. Vietnam may make pacing stricter or retries fewer; it never adds retries.
Pause the source after repeated 429/403 responses. Do not bypass access controls or disable TLS verification.
The core enforces byte/time/page limits, redirect and address restrictions, cancellation, and retry accounting. Include certificate-fetch URLs in those restrictions.
Retain successful pages after partial failure, but advance a complete-window cursor only after every required slice is accounted for.
Use a 180-day overlap initially for issue-date sources; budget rolling historical rescans separately so old amendments and file replacements remain discoverable.
Recheck future commencement dates daily near their boundaries. Use conditional requests and hashes where supported; a changed hash creates an observation.
Report last successful window, oldest covered date, capped pages, expected/fetched artifacts, and failures per source. Fresh discovery alone does not prove fresh validity.

## Document types and số ký hiệu

Preserve source type, printed number, title, issuer, and date separately from matching keys. Do not derive legal rank from a suffix alone.
Treat the following as parsing vocabulary, not a claim that every type has qualified holdings.

| Type | Identity and handling |
|---|---|
| Luật; Bộ luật | National Assembly number such as `24/2018/QH14`; title/type distinguishes a law, code, or resolution sharing the number pattern |
| Nghị quyết | National Assembly, Standing Committee, government, or other evidenced issuer; support `QH`, `UBTVQH`, and `NQ-` shapes |
| Pháp lệnh | Standing Committee identity, including `01/2012/UBTVQH13`; no required hyphen |
| Nghị định | `NĐ-CP` and qualified historical shapes; preserve `Đ` in printed citations |
| Quyết định | Year-bearing or yearless numbers, including `QĐ-TTg`, `QĐ-NHNN`; verify normative versus individual act |
| Thông tư | `TT-NHNN` and other ministry issuers; year and issuer remain part of the number |
| Thông tư liên tịch | `TTLT-` with multiple issuer components; keep their printed order |
| Văn bản hợp nhất | `VBHN-` identifier, often without a year; retain issue/authentication date and original instrument type separately |
| Other issued classes | Hiến pháp, Lệnh, Sắc lệnh, Chỉ thị, Công văn, Công điện, Thông báo, Kế hoạch; retain exact type and classify legal/context role from evidence |
| Attached named text | Quy chế, Quy định, Điều lệ, and Hướng dẫn issued with an instrument use `issued_text` parts unless separately issued; title words alone do not create documents |

Parse numbers in a derived NFC matching view with whitespace removed around `/` and `-`, including nonbreaking spaces.
Compare issuer tokens case-insensitively. Preserve every raw spelling and its source span; do not rewrite stored citations.
The token grammar is `serial [ / year ] / issuer-or-kind { - issuer-component }` for qualified modern forms.
`serial` is digits, `year` is four digits, and components contain letters including `Đ` and digits. Historical punctuation requires a versioned grammar rule.
The year-bearing final token can be `QH14` or `UBTVQH13` with no hyphenated suffix. Reject numeric-only tails such as bare dates.
Accept `116/ 2025/QH15`, `50/2024/ TT-NHNN`, `01/2012/UBTVQH13`, and `266/QĐ - NH1` as matching variants.
Admit yearless identifiers only with a qualified issuer/kind token or explicit source identity; an unsupported shape remains an unresolved reference.
Treat `9/2024/TT-NHNN` and `09/2024/TT-NHNN` as alias candidates, not proof of sameness. Confirm type, title, issuer, and date.
Numbers ending in `QH15` do not determine whether the text is a Luật or Nghị quyết.

Resolve a candidate by source alias first, then normalized number plus compatible type and title, with issuer/date confirmation.
Conflicting type/title evidence or several candidates yields ambiguity and blocks effect attribution. Do not select the first number match.
Use title normalization only for spacing/case matching; similar titles alone never merge documents.
For repeated VBHN numbers, require the issue/authentication year and resolve remaining collisions by title/date evidence.
A missing year or `KHÔNG SỐ` stays under an opaque source identity until reviewed. Do not create a shared empty-number key.
New immutable identities preserve v1 aliases through explicit mappings. A rebuild cannot retarget old citation links silently.
V1's type/number key and VBHN year exception provide reuse evidence: [process_activities.go:1407](../../pkg/pipeline/process_activities.go#L1407).

## Structure and native citations

Conversion owns byte decoding and `BuildStructure`; Vietnam supplies the grammar below over normalized blocks.
Parts owns appendix/form identification, namespaces, and display under [document-parts.md](document-parts.md#part-model-and-deterministic-parsing).

| Level | Recognition and parent rule |
|---|---|
| Phần | Printed Roman/decimal or qualified Vietnamese ordinal heading, such as `Phần thứ nhất`; retain the exact label |
| Chương | Roman/decimal heading inside the document, issued text, or Phần; omitted intermediate levels need no fabricated container |
| Mục | Roman/decimal heading below Chương or an explicitly supported enclosing body |
| Tiểu mục | Explicit numbered heading under Mục; keep distinct from Mục and from numbered prose |
| Điều | `Điều` plus digits and optional inserted suffix, such as `21b`; require a heading block, not prose `Điều kiện` |
| Khoản | Printed numbered subdivision, commonly `1.` or explicit `Khoản 1`, inside an Điều; retain inserted suffixes when printed |
| Điểm | Printed letter such as `a)` or `đ)` inside an evidenced clause/article context; order `d`, `đ`, `e` distinctly |
| Older outline | Preserve `I.`, `A.`, `1.1.` and narrative blocks when the source lacks Điều; technical anchors never become invented statutory labels |
| Phụ lục / Mẫu số | Separate typed parts with exact Roman/decimal labels or codes such as `TTLNH-01`; repeated Điều/01 numbering belongs to its part namespace |

Recognize headings at block starts with label, style, and enclosing context. Tables, catalogues, references, and preambles do not open body subdivisions.
Keep `body`, `issued_text`, and appendix namespaces distinct. A bare form heading closes the body only through a confirmed parts boundary.
Preserve tables, footnotes, blank form fields, signatures within forms, and short parts. Never drop a form because its text is short or repeats another form.
Every retained block has one owner or an explicit unresolved region. Do not append uncertain attachments to the final Điều.
Reconcile VBPL node IDs and `ptype` with captured text. A missing source-tree khoản is a gap, not permission to invent its words.
Derive a missing subdivision only from an explicit printed label and anchored text; retain the original tree observation and comparison.

Track curly-quote depth, straight quotes, and amendment replacement state after `như sau:` across pages and blocks.
Quoted `Điều`, numbered khoản, appendix labels, and references stay inside the acting provision's replacement span.
After a supported closing quote, resume at the next genuine provision. Unbalanced quotes create `QUOTE_BOUNDARY`, never a guessed reset.
Return separate operator, target, recital, replacement, and citation spans to the relation parser.

Keep printed labels and exact native citation bytes beside structured citation components and normalized lookup keys.
Render new citations from evidenced components, for example `điểm đ khoản 2 Điều 5 Thông tư 09/2020/TT-NHNN`.
That syntax example does not assert the cited point's existence. No renderer may synthesize a missing hierarchy level.
Qualify part citations with parent instrument and namespace, for example a form code plus its printed Phụ lục label.
Retain v1 citation bytes for imported evidence. The new display formatter must not change legacy IDs or citation lookup semantics.
Include document identity, release, revision, source URL/hash, legal date, authority, and gaps separately from the native citation string.

## Amendment grammar and validity

Parse each operative statement independently. Substring co-occurrence is never sufficient attribution.
The binding parser safeguards are [instructions/corpus.md:48](../../instructions/corpus.md#L48).

| Printed operator | Typed assertion and effect rule |
|---|---|
| sửa đổi | Change the named provision/span; retain before/after evidence without applying an inferred consolidated rewrite |
| bổ sung | Add the named provision, phrase, or part at the explicit location; addition alone does not terminate its parent |
| sửa đổi, bổ sung | Preserve both operations and their exact extents; do not expand to every provision mentioned in the clause |
| thay thế | Replace only the named document, provision, phrase, or part; whole-document replacement needs explicit whole-document scope |
| bãi bỏ | Repeal the named scope from the evidenced effective date; partial repeal never ends the whole instrument |
| hết hiệu lực | Record the stated end of force and cause; a status badge alone cannot identify the acting provision or exact date |
| đình chỉ; ngưng hiệu lực | Suspend the named scope for the evidenced interval/condition; do not turn suspension into permanent repeal or invent resumption |

1. Identify the acting document and provision, then isolate the operative sentence/list outside quoted replacement text.
2. Bind each operator to its following target expression. Parse `điểm`, `khoản`, `Điều`, part code, document type/number, and coordinated lists/ranges.
3. Mark history introduced by `được sửa đổi ... theo ...` as recital references. A bare `theo` elsewhere is not automatically history; grammar decides its role.
4. Numbers inside replacement text after `như sau:` are quoted references. They cannot become targets of the enclosing operator.
5. Carry an enclosing target into subclauses only when an explicit amendment heading grammatically scopes them. Record that heading span; never use a document-level graph edge as the scope.
6. Resolve each target against type/title/date and its provision or part namespace. Preserve exact unresolved references and expected targets without applying legal effect.
7. Emit separate assertion/target rows for each supported operation. Store raw source-feed assertions alongside parsed and reviewed assertions, including disagreements.
8. Resolve commencement, exceptions, transitional conditions, and date precision separately. General commencement applies only after exception scopes have been checked.

An assertion packet contains the acting revision/citation, operator span, full clause, target span, extent, target identity,
recital/replacement spans, commencement evidence, authority, recipe, and review references.
`official_structured` and confidence `1` mean an official source asserted a relation. They do not verify the target or authorize a clause snippet.
Incoming amendment views join supported assertion targets, not all clauses from a related document. Keep weak assertions visible as unresolved context.
The core computes amendment differences from exact before/after inputs; missing inputs produce an unavailable difference, not generated text.

Provisions and parts use the database's shared `evidence_target`. Resolve text periods separately from legal-validity intervals.
Use half-open legal dates `[start,end)` in Vietnam time. Unknown endpoints retain certainty fields; they do not imply perpetual force.
Keep issue, publication, commencement, repeal, observation, and processing times distinct.
Whole-article repeal reaches evidenced descendants within that article, not unrelated provisions, attached forms, or the parent document.
Part replacement affects the exact part identity, not every `Mẫu số 01`. Phrase-level changes retain a partial scope and unresolved text period when needed.
Unknown, conflicting, conditional, and future effects remain explicit. Strict current-only results require supported target-level validity.
Never inherit a green current stamp from a document onto every provision or form.

### Worked repeal and six-clause attribution

The required case links khoản 6 Điều 22 Thông tư 50/2024/TT-NHNN to Điều 25 Thông tư 09/2020/TT-NHNN.
The [official government PDF](https://datafiles.chinhphu.vn/cpp/files/vbpq/2024/11/50-nhnn.pdf), printed pages 22–23,
places the repeal in khoản 6 and general commencement in khoản 1, effective 2025-01-01.
Khoản 2–4 identify delayed effects for other provisions; khoản 5 addresses other instruments. Do not assign those effects to Điều 25.
The PDF text layer is corrupt. This source inspection is not a hash-pinned corpus fixture or human legal adjudication.

| Assertion/output | Required result |
|---|---|
| Acting citation | `khoản 6 Điều 22 Thông tư 50/2024/TT-NHNN`, with the complete clause and original-page anchors |
| Target citation | `Điều 25 Thông tư 09/2020/TT-NHNN`, resolved using its type and title, not `09/2020` alone |
| Effect | `bãi bỏ`, whole target article, from 2025-01-01, supported by khoản 1 and checked exceptions |
| Historical interval | End the prior supported Điều 25 interval at 2025-01-01; do not invent its start or presume its pre-repeal status without the original |
| Other text | Keep the rest of 09/2020 independently assessed. Do not label the entire circular repealed |
| Incoming view | Show the supported khoản 6 once. The other five reported clauses cannot inherit this target from a document relation |

Capture both issued originals, commencement pages, source relation payloads, and all six reported incoming clauses in the fixture manifest.
The repository reports six-clause misattribution at [WEBSITE.md:218](../design/WEBSITE.md#L218); their full identities and bytes remain to be captured.
Do not assume those six reported clauses are exactly the six khoản of Điều 22. Test that article's clauses as a separate negative set.
For every reported clause, require its own operator/target proof. Preserve unsupported source assertions with rejection or unresolved reasons.
Use the [review API case](corpus-review.md#worked-example-and-rollout) for human checking, fenced acceptance, and release selection.
Test 2024-12-31 and 2025-01-01, separately asserting target status, acting citation, unchanged body text, and no whole-document repeal.

## Official consolidated texts

Treat Văn bản hợp nhất (VBHN) as an official consolidated rendition with its own identity and authentication/issue date.
Keep the binding base instrument and amendments. A VBHN never overrides them or independently creates an amendment/repeal effect.
Record base/amending members, evidenced consolidation cutoff, footnote-to-original mappings, and unresolved members.
Do not assume its issue date is the effective date of every provision or that the newest VBHN includes every later amendment.
Allow VBHN retrieval with a visible consolidation label and links to originals. Preserve its own citations and source spans.
For as-of queries, use only evidenced text periods; a later VBHN cannot silently supply historical wording from before an amendment.
Conflicting text requires review. An unmapped clause may remain consolidated reading evidence with a gap, not a binding-original substitute.
V1 mirrors base status onto the newest family member and expires older ones: [vbhn_validity.go:61](../../pkg/pipeline/vbhn_validity.go#L61).
Replace that projection with evidence-backed family/cutoff assessment. Reuse family candidates and caches only where their provenance qualifies.

## Vietnamese input and text policy

Validate all Vietnam search surfaces before embedding, expansion, or corpus queries. Identifier readers validate their selector grammar separately.
Use a deterministic, versioned gate, not a language model. Preserve the original query in request memory only.
After UTF-8/NFC validation and request-size checks, accept an entire qualified document-number/citation expression without requiring diacritics.
A number embedded in an English sentence does not bypass the prose gate. A bare date is not a document number.
For prose, require Vietnamese diacritics and Vietnamese phrase/token evidence from the frozen legal/general vocabulary.
Proposed initial rule: longest-match phrases must cover at least 80% of word tokens after excluding qualified numbers and allowlisted technical terms.
Require at least two Vietnamese vocabulary tokens, including one Vietnamese-specific accented token; normalize combining marks before counting.
Permit declared technical terms such as API, OTP, eKYC, and Internet Banking within Vietnamese prose; their presence cannot establish the language.
Queries shorter than two Vietnamese tokens require an exact reviewed accented entry, such as `nợ`.
Reject ambiguous or unsupported language with `VIETNAMESE_QUERY_REQUIRED`. Record the rule version and reason, without logging the query.
Qualify these proposed thresholds on tuning fixtures, freeze them, then evaluate sealed cases before activation. A single accented character cannot establish Vietnamese.
Reject accent-free prose and English even when v1 could retrieve something. Do not translate, restore accents, or call a third-party classifier to admit input.
Accepted examples: `50/2024/TT-NHNN`, `khoản 6 Điều 22 Thông tư 50/2024/TT-NHNN`, and `Ngân hàng cần kiểm soát truy cập hệ thống thế nào?`.
Rejected examples: `bank security requirements`, `an toan he thong ngan hang`, `What changed in 50/2024/TT-NHNN?`, and `01/01/2025`.

Keep stored legal text in Vietnamese NFC, without translation or accent folding. Derived lexical matching keys may fold accents; citation bytes may not.
Delegate TCVN3/VNI and mixed-font decoding to [conversion](document-conversion.md#legacy-encodings-and-reversible-normalization).
Require evidenced font/charset mapping, retained originals, reversible transforms, zero unresolved corruption in accepted spans, and review of ambiguous mappings.
A diacritic dictionary can flag suspicious text or supply reviewed lexical aliases. It cannot repair canonical text or bypass the query gate.
V1 dense-query accent restoration is coded at [retrieve.go:469](../../pkg/rag/retrieve/retrieve.go#L469); remove that path from the new Vietnam serving composition.
Reuse abbreviation expansion after validation with a release-pinned dictionary: `NHNN`, `TCTD`, `CNTT`, `ATTT`, `PCRT`, `eKYC`.
Keep the query and expanded retrieval view distinct. Apply whole-token matches; ambiguous abbreviations need context or remain unexpanded.
English can label the interface. Examples, questions, legal text, citations, and source-derived excerpts remain Vietnamese.

## MCP guide and website strings

Keep exactly `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`. Review actions stay outside public MCP.
The guide uses the same country bundle as the website. Replace v1's translation-first instruction at [brief.go:46](../../pkg/mcp/brief.go#L46).
Guide contract: “Search Vietnamese banking, finance, and related technology law using Vietnamese with diacritics or a document number.
Do not translate a foreign-language question to bypass the input rule. Ask the user for Vietnamese input.
Read `corpus_status` and `quality_gaps`; use `search` to find evidence and `document` to inspect the full provision, parts, and acting clauses.
Keep the release and legal date fixed. Cite the native provision and official source. Check target-level validity and disclose missing evidence.
OCR is non-binding transcription. A VBHN is an official consolidation; the originals prevail. The service returns evidence and does not answer.”

| Surface | Vietnamese string | English interface string |
|---|---|---|
| Title | `Tra cứu quy định` | `Regulation lookup` |
| Input | `Nhập câu hỏi tiếng Việt có dấu hoặc số ký hiệu văn bản` | `Enter Vietnamese with diacritics or a document number` |
| Language error | `Vui lòng dùng tiếng Việt có dấu hoặc số ký hiệu văn bản, ví dụ 50/2024/TT-NHNN.` | `Use Vietnamese with diacritics or a document number, for example 50/2024/TT-NHNN.` |
| Legal date | `Hiệu lực tại ngày` | `Legal status on` |
| Unknown validity | `Chưa xác định hiệu lực` | `Validity unresolved` |
| Scoped repeal | `Đã bãi bỏ từ {date}` | `Repealed from {date}` |
| Partial force | `Còn hiệu lực một phần` | `Partly in force` |
| OCR | `Bản chép OCR, không phải văn bản gốc` | `OCR transcription, not the original text` |
| Consolidation | `Văn bản hợp nhất. Đối chiếu văn bản gốc và các văn bản sửa đổi.` | `Official consolidation. Check the original and amending instruments.` |
| Missing evidence | `Chưa đủ dữ liệu để xác định` | `Insufficient evidence` |
| Sources | `Nguồn và tệp gốc` | `Sources and original files` |

Use the remaining part/status/review labels from the owning specs. Never translate a legal citation when switching interface language.
Responses retain release/revision IDs, scoped validity, authority, source links, named gaps, and review scope at every detail level.
Coverage strings come from release counts by source/type/subject, not hardcoded claims of complete Vietnamese law.

## Reuse map

“Keep” means port with characterization fixtures. “Adapt” means reuse pure logic behind the new contract. Neither means run legacy mutations.

**Decision:** Port useful v1 logic into the country module and new core contracts.
**Why:** Existing collectors, grammar fixtures, and captured evidence reduce work while immutable acceptance fixes the system boundary.
**Rejected:** Repairing the live v1 corpus incrementally, or rewriting qualified components without evidence of need.
**Detail:** Preserve legacy services and identifiers; qualify reused inputs and logic through the milestones below.

| V1 code and evidence | New-module treatment |
|---|---|
| Four source builders, [app.go:371](../../pkg/app/app.go#L371), and collectors cited above | Keep source IDs, mappings, pagination, and fixtures. Port qualified transport mechanics into core fetch activities without inner retries. Split country request planning from sandbox parsing; retain bounded manifests, attachment accounting, and immutable capture. Qualify `sbv` separately. |
| Scope matcher, [scope.go:1](../../pkg/scope/scope.go#L1), and [scope_term.csv:1](../../deploy/seed/scope_term.csv#L1) | Keep the pure matcher and useful vocabulary. Adapt admission/subjects and freeze config versions; remove any trust in title-search admission as final legal classification. |
| Identity, [process_activities.go:1407](../../pkg/pipeline/process_activities.go#L1407) | Extract Vietnam number/type helpers. Replace number-only collision fallback with explicit candidates, title/type disambiguation, and reviewed aliases. |
| Body state machine, [legalparse.go:27](../../pkg/pipeline/legalparse.go#L27), tree adapter, [vbpl_tree.go:43](../../pkg/pipeline/vbpl_tree.go#L43) | Adapt grammar/quote fixtures to blocks. Add Phần ordinal and Tiểu mục cases; preserve tree gaps. Replace Markdown as the interchange model. |
| Citation rendering/tests, [index_activities.go:615](../../pkg/pipeline/index_activities.go#L615), [citation_golden_test.go:18](../../pkg/pipeline/citation_golden_test.go#L18) | Keep native citation behavior for imported evidence. Adapt namespace/part support and preserve old selectors through explicit mappings. |
| Text repairs, [vietnamtext.go:78](../../pkg/pipeline/vietnamtext.go#L78), [vietnamtext.go:119](../../pkg/pipeline/vietnamtext.go#L119) | Reuse diagnosed patterns as findings/qualified reversible transforms. Replace destructive glyph stripping and unaudited repair. Conversion owns implementation. |
| Relation candidates, [relation_evidence.go:70](../../pkg/pipeline/relation_evidence.go#L70), [relation_evidence.go:417](../../pkg/pipeline/relation_evidence.go#L417) | Keep number recognition and raw source evidence. Replace weak substring attribution with operator/target grammar and typed targets. |
| VBHN family projection, [vbhn_validity.go:61](../../pkg/pipeline/vbhn_validity.go#L61); unknown-as-current default, [jurisdiction.go:187](../../pkg/base/jurisdiction/jurisdiction.go#L187) | Keep family candidates; replace status mirroring and unknown-as-current in the new module. |
| Query recipes/dictionaries, [app.go:478](../../pkg/app/app.go#L478), [retrieve.go:1205](../../pkg/rag/retrieve/retrieve.go#L1205) | Reuse abbreviation and lexical data after validation. Exclude accent restoration; core retains dense/lexical fusion and qualified embedding caches. |
| Destructive normalization, [normalize_activities.go:420](../../pkg/pipeline/normalize_activities.go#L420); CLI sequencing, [main.go:528](../../cmd/pipeline/main.go#L528) | Replace with immutable revisions and fenced core activities. New workers never wrap these mutations. Existing AWS entry points remain untouched. |
| Core ports, [run/apply.go:44](../../internal/corpus/run/apply.go#L44), [release/validation.go:5](../../internal/corpus/release/validation.go#L5); schema, [silver/schema.sql:396](../../sql/silver/schema.sql#L396) | Reuse under sibling ownership. Vietnam supplies typed facts; core implements persistence, migrations, release validation, and shared transport. |
| Vietnam guide/goldens, [brief.go:43](../../pkg/mcp/brief.go#L43), [golden.json:1](../../deploy/eval/golden.json#L1) | Adapt guide to the gate. Preserve the original suite unchanged in the legacy baseline lane for offline parity and model controls. Version public-search assertions separately; retain floors and add attribution/parts cases without hiding failures. |

Import v1 captured originals, OCR results, source observations, and compatible vectors through manifests where hashes and recipes prove identity.
Missing provenance becomes a gap or a fresh conversion candidate. Do not recrawl or re-embed qualified unchanged inputs merely to change package layout.

## Evaluation and first milestones

All execution belongs on GitHub-hosted CI under [verification.md](../../instructions/verification.md). No local builds, tests, database, or cloud commands.
Use synthetic parser fixtures in Git and hash-pinned public originals in isolated hosted artifacts. Keep real payloads out of Git and personal data out of Git and hosted CI.
Freeze code commit, manifest, recipes, taxonomy, legal dates, retrieval settings, and expected evidence. Missing inputs or skipped required checks block the gate.
Keep retrieval and external-agent workflow evaluation separate. Run unchanged v1 goldens against the new core in a separate legacy baseline lane using frozen legacy routing/preprocessing outside public serving.
Record legacy baseline results per milestone, including unavailable cases and reasons. They gate only milestone 5, which claims retrieval parity; earlier milestones cannot claim parity.
The public-search lane applies the versioned Vietnamese gate. For `edge-no-diacritics-infosec`, `edge-no-diacritics-payment`, and `edge-no-diacritics-mangluoi`, require `VIETNAMESE_QUERY_REQUIRED` and zero embedding/corpus calls.
Report suite/version, routing policy, case IDs, cohort, exclusions, and denominator for every release/model metric. Score language rejection separately from retrieval and scope abstention.
Compare control and candidate under identical lane conditions and frozen inputs, varying only the declared factor. Never hide the language change with `expect_fail`, altered citations, or lower floors.
Reuse accepted Vietnamese scenarios and author the embedding spec's 120-case supplement, split 60 tuning/60 sealed by document family.
Include realistic questions such as `Ngân hàng mở tài khoản bằng eKYC cần đối chiếu những quy định nào?` and
`Ngày 01/01/2025, Điều 25 Thông tư 09/2020/TT-NHNN còn hiệu lực không? Văn bản nào bãi bỏ điều này?`.
Form requests must name the user's task; bare identifiers belong in lookup tests, not the scenario suite. Human reviewers set exact source-backed expectations.

| Release measure | Required gate |
|---|---|
| Retrieval by lane | Recall@8 ≥ 0.90, mean reciprocal rank@8 ≥ 0.66, current-law precision ≥ 0.99, abstention ≥ 0.95, from [Makefile:69](../../Makefile#L69). Preserve [metric definitions](../design/RAG.md#retrieval-evaluation). Legacy baseline uses the unchanged suite; public search uses its frozen expected gate-accepted cohort; unexpected rejection fails. Report rejection and existing `expect_fail` separately; neither can hide retrieval failures. |
| Rebuilt Vietnam versus frozen control | At milestone 5, meet both lanes’ retrieval floors and explain every per-case difference; no new wrong-current-law result. Model controls use the unchanged legacy suite, plus the embedding spec’s sealed set and runtime gates under matched routing and cohorts. |
| Parts | Per parts spec: recall@8 ≥ 0.95 and MRR@8 ≥ 0.80, separately for forms and appendices, at least 20 answerable cases each. |
| Critical legal fixtures | 100% expected identity, citation, target attribution, date-boundary result, part namespace, and OCR label; zero false current stamps or invented text |
| Conversion and coverage | Meet conversion gates; every expected page/part/artifact has evidence or an approved named gap. Zero orphan citations, dangling targets, hash mismatches, or missing required index/vector outcomes. |
| Language/residency | 100% fixed gate fixtures; rejected input makes zero embedding/corpus calls. Synthetic privacy sentinels cannot enter export, logs, Temporal, or batch manifests. |
| Subject and source scope | Exact expected classification on fixtures; report unknown/unreviewed denominators, strict-filter recall, stale windows, failed pages, and file coverage |

Hard fixtures include the worked repeal and six reported clauses; omnibus recital traps; `116/ 2025/QH15`; duplicate QH numbers;
quoted replacement articles spanning pages; an explicit Tiểu mục; missing VBPL khoản; old numbered outlines; repeated VBHN numbers;
mixed TCVN3/VNI uppercase fonts; corrupt PDF text with readable scans; forms after signatures; duplicate Mẫu số in different appendices;
readable body with scanned attachments; merged/nested tables; a replaced appendix; a draft attachment on an official host; and missing commencement evidence.
Require exact native citations and direct target hits in new legal cases. Relation-based document credit cannot satisfy a provision/part expectation.
Audit at least 30 eligible documents per release, stratified by source/format, against originals; inspect all when fewer than 30 qualify.
Report sampled completeness separately from declared-slot accounting. A zero denominator is unknown or not applicable, never 100%.

| Milestone | Deliverable and GitHub-hosted exit evidence |
|---|---|
| 1. Module boundary and reuse baseline | Require only the Vietnam module, neutral registry, and frozen six-country v1 baselines with input/recipe hashes. Prove absent/disabled-country denial before I/O. Preserve existing six-country CI contracts and AWS services; no other module migration is required. |
| 2. Sources and identity | Replay four sources through pure request plans, core fetch activities, and sandbox parsers. Prove parsers have no network access, fetch activities never parse bodies, request counts and `NextEligible` survive retry/worker loss, and `Retry-After` persists. Cover caps, cursors, attachments, whitespace/duplicate-number handling, and aliases. Protected qualification supplies hashes/access evidence; `sbv` stays disabled until qualified. |
| 3. Conversion and Vietnamese structure | Hosted sandbox fixtures prove encodings, quote state, Tiểu mục, source gaps, body/part ownership, tables, and citation parity. Run sibling schema generation and clean/upgrade checks on disposable databases before accepting revisions. |
| 4. Legal effects and review API | Synthetic and protected real-document fixtures prove clause attribution, typed targets, date boundaries, VBHN limits, human approval, stale-decision rejection, and no v1 writes. Missing real originals blocks corpus validation. |
| 5. Candidate Vietnam release and retrieval parity | Run the Vietnam write path end to end on GitHub-hosted CI over a bounded accepted cohort. Prove replay/retry/cancellation, receipts, immutable membership, closure, index coverage, and rollback. Gate retrieval parity on the unchanged legacy baseline suite; require public-search assertions, both lanes’ floors, and hard fixtures. |
| 6. Serving contract and release readiness | Hosted website/MCP fixtures prove one gate, five tools, language strings, exact citations, scoped stamps, retained-release reads, privacy, and disabled-country denial. Archive capacity/parity evidence under the hosting/embedding gates. Owner promotion and post-deployment verification remain separate. |

Defer the five other module migrations to [country-modules milestone 5](country-modules.md#migration-milestones), after Vietnam’s hosted end-to-end write path; they never block Vietnam.
Each milestone requires independent review and exact-commit hosted evidence for its stated gates before dependent implementation proceeds.
This design claims no review, build, test, corpus evaluation, or deployment result.

## Owner questions and unresolved work

1. Approve the seven subject keys and the primary/context boundary? Recommend the table above, with ordinary insurance claims excluded and deposit insurance included.
2. May the first release ship with the four reused collectors while direct `sbv.gov.vn` qualification remains open? Recommend yes only with explicit source/coverage gaps and all release floors met; never substitute press releases for issued texts.
3. Approve the crawl cadence and initial one-request-per-second host budget? Recommend the stated defaults, lowered for source rules or pressure and raised only after measured qualification.

Unresolved: exact direct-SBV listing/pagination contract, robots/access terms and live source reliability; source-personal-data screening before foreign capture;
hash-pinned originals and the six reported clause identities; real legacy-font fixtures; query-gate qualification; and independent cross-spec review.
The primary must reconcile shared records, add the same-name implementation plan, and link this spec from the index in a separate authorized documentation slice.
This task revises only this file. Shared-spec approval questions retain their owners; none of these Vietnam questions reopens settled hosting, model, or language decisions.
