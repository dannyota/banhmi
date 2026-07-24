# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md); the multi-country model in
[`docs/design/jurisdictions/`](docs/design/jurisdictions/). Last updated: 2026-07-19.

## Vision

A self-hostable, **multi-country** platform for Southeast-Asian **banking & financial regulation** and
**cross-cutting technology law**: one codebase that crawls each country's official sources, builds a
clean, citable corpus in that country's binding legal language, and **serves it as evidence over MCP** — exact native citations
(Dieu/Khoan, Section/Subsection, Pasal/ayat), validity, relations, provenance, and explicit gaps.

- **One codebase, one corpus per country** — separate database, MCP service, and domain per
  jurisdiction ([playbook](docs/design/jurisdictions/PLAYBOOK.md)). Never a branch or fork.
- **The data is the product; the user brings the model.** No built-in answer LLM — hosted agents
  connect over MCP and reason over the evidence themselves.

> **Status convention:** "coded" = code written + unit/integration tests; "validated" = checked on real
> documents. Never report one as the other.

## Jurisdictions

| # | Country | Codename | Endpoint | Status | Design |
|---|---------|----------|----------|--------|--------|
| 1 | Vietnam | `banhmi` | banhmi.danny.vn/mcp | **LIVE** (2026-06-01) | [SOURCES](docs/design/SOURCES.md) |
| 2 | Malaysia | `laksa` | laksa.danny.vn/mcp | **LIVE** (2026-06-22) | [MALAYSIA](docs/design/jurisdictions/MALAYSIA.md) |
| 3 | Indonesia | `rendang` | rendang.danny.vn/mcp | **LIVE** (revived 2026-07-12) | [INDONESIA](docs/design/jurisdictions/INDONESIA.md) |
| 4 | Singapore | `kaya` | kaya.danny.vn/mcp | **LIVE** (2026-07-17) | [SINGAPORE](docs/design/jurisdictions/SINGAPORE.md) |
| 5 | Thailand | `tomyum` | tomyum.danny.vn/mcp | **LIVE** (2026-07-17) | [THAILAND](docs/design/jurisdictions/THAILAND.md) |
| 6 | Cambodia | `amok` | amok.danny.vn/mcp | **LIVE** (2026-07-18) | [CAMBODIA](docs/design/jurisdictions/CAMBODIA.md) |

All 6 jurisdictions shipped on one codebase, one ECS instance, one RDS.

## Deployment shape

- **Read path:** CloudFront (6 distributions, ACM TLS) → ECS on EC2 t4g.medium (ARM64 Graviton) → RDS PostgreSQL 17 + pgvector.
  1 container serving all 6 jurisdictions (routed by `X-Banhmi-Jurisdiction` header), host networking,
  in-process Qwen3 ONNX FP16 query embedder shared across jurisdictions.
- **Write path:** local pipeline runs, dumped/restored to RDS. Bulk embed on Kaggle T4 (free).
  VN sources geo-locked (needs VN IP; Hanoi Local Zone EC2 relay). ID OJK via GCE Jakarta proxy.
  TH SEC via AWS Bangkok proxy (ap-southeast-7, on-demand).
- **DB:** RDS `ap-southeast-1`, one DB per country (`banhmi`, `laksa`, `rendang`, `kaya`, `tomyum`, `amok`).
  Origin-SG-only; pipeline runs temporarily allowlist the maintainer /32.
- **GCP (remaining):** Vision OCR API (global endpoint) + Jakarta GCE proxy only.
- **S3 data buckets:** `danny-banhmi-data-{vn,my,id,th}` (file cache + OCR cache mirror).
- **Cost:** ~$65/mo (EC2 t4g.medium $25 + RDS $26 + CloudFront×6/EIP/S3/ECR ~$15).

## Current state (v0.3.3-20260719)

**Prod runs 6 jurisdictions.** KH (`amok`) deployed 2026-07-18. Accepted 2026-07-19 baselines
(local eval; floors in the Makefile track these):

| Jurisdiction | Docs | Chunks | Recall | MRR | In-force | Abstain | Cases | Floors (R/M/I/A) |
|---|---|---|---|---|---|---|---|---|
| VN (`banhmi`) | 1,771 | 52,546 | 92.7% | 69.7% | 100% | 100% | 80 | 0.90/0.66/0.99/0.95 |
| MY (`laksa`) | 109 | 11,304 | 94.3% | 79.6% | 100% | 100% | 72 | 0.92/0.77/0.99/0.95 |
| ID (`rendang`) | 2,371 | 160,142 | 79.8% | 62.4% | 100% | 100% | 110 | 0.78/0.60/0.99/0.98 |
| SG (`kaya`) | 292 | 27,951 | 93.5% | 79.9% | 100% | 97.9% | 76 | 0.90/0.75/0.99/0.90 |
| TH (`tomyum`) | 1,551 | 29,736 | 89.5% | 72.0% | 100% | 96.6% | 58 | 0.86/0.68/0.99/0.90 |
| KH (`amok`) | 282 | 2,609 | 93.1% | 74.4% | 100% | 100% | 42 | 0.90/0.70/0.99/0.95 |

**Workflow eval (agent contract):** 10-case VN pilot, Haiku stand-in agents over `tools/mcpcall`,
scored by `tools/wfscore`: citation 85.0%, abstention 100%, relation-following 83.3%.
See [`docs/design/WORKFLOW-EVAL.md`](docs/design/WORKFLOW-EVAL.md).

## Roadmap

### v0.3.0 — AWS read path, Qwen3-Embedding, 3 jurisdictions — COMPLETE

**Shipped 2026-07-12.** Read path: CloudFront + ECS on EC2 ARM64 Graviton. Embedder: BGE-M3 -> Qwen3-Embedding-0.6B
ONNX FP16. GCP teardown complete (only Vision OCR API remains). ID revived with `ojkweb` source.

### v0.3.1 — MCP token optimization + amendment-chain awareness — DEPLOYED (2026-07-14)

1. **`search` `detail` param** — `compact`/`standard` (new default)/`full`; standard cuts tokens ~54% vs full.
2. **`document` `include` param** — selective section loading; `citation` + `chunks` = cheapest one-provision read (~92% savings).
3. **`amendment_chain`** — recursive lineage walk (depth <= 4, cycle-safe); `target_amended_by` citator warnings.
4. **ID relation promotion** — bi/bpk structured status -> 1,198 confirmed relations (719 revokes, 479 amends).

### v0.3.2 — Eval-driven corpus & retrieval fixes — COMPLETE (final deploys 2026-07-19)

**Completed** (shipped/deployed 2026-07-15 through 2026-07-16):

1. VN validity-starvation fix — normalize selector repaired (recall 61.1 -> 79.6).
2. VectorK/BM25K raised 50 -> 100 (MY/ID +1 case each, VN MRR +0.7).
3. Per-document cap (`defaultDocCap=4`) — ID recall +3.5, MY MRR +0.2, zero regressions.
4. ID abstention fix — Perpres/PMK/Perppu reference shapes + 16 scope terms (87.5 -> 100%).
5. Diacritics seam — TextNormalizer descriptor (vn/my/id byte-identical, regression-pinned) + query diacritic restoration via `config.diacritic_restore` (698 entries, `cmd/dictgen`).
6. MY parser overhaul — pre-split marginal notes in `myBodyLines`; broken Acts 0 -> 281/291/101/26 sections.
7. SC dedup — derived SC doc identifiers, eliminated ~1,600 chunks of duplicate noise.
8. BNM Liferay fix — PDF-link regex now accepts UUID-suffixed URLs; +34 in-scope docs incl. Outsourcing PD.
9. VN label-only chunks — heading orphans fixed in `splitLongChunkContent`; vbpl empty-body markdown fallback.
10. ID parser fixes — OCR-noise strip, omnibus amendment-block guard, Roman-numeral Pasal (UU 27/2022: 22 -> 76 Pasal).
11. OJK source wiring — F5-WAF rejection detection, OJKMinter, BPK-canonical doc_keys (zero duplicates), watermark cap + cursor reset.
12. BI terminal-state fix — `finalizeDoc` terminal-completion path for permanent PDF 404s + BPK PBI (jenis 78) text fallback.
13. Chunk dedup across pipeline.
14. Abbreviation expansion for retrieval.
15. Promotion-only section aggregation — appends <= 2 multi-fragment article groups (VN recall +3.7, zero regressions).
16. Golden expansion — VN 80 / MY 73 / ID 110 cases (from 50/46/69).
17. Workflow eval harness + 10-case VN pilot (citation 85/abstention 100/relation-following 83.3).
18. OCR engine -> Vision `images:annotate` (Document AI quota-blocked); file-first cache (local + S3).
19. Kaggle embed fix — per-run ORT arena shrinkage (dual-T4 OOM root-caused).
20. Online Safety Act 2025 (Act 866) scoped + ingested for MY.

**Completed before v0.4.0:**

1. ID jdih drain (903/968), BPK PBI jenis 78 discovery, MY embed fix — all done + deployed.
2. VN bigram diacritics — 27,210 entries; recall +4.9 to 82.9%; `edge-no-diacritics-payment` flipped.
3. VN large-law goldens relaxed to doc-level (5 cases — bridging is the agent's job).
4. ID golden retargets for JDIH short-form doc_numbers (5 cases recovered).
5. VN/MY/ID scope-term fixes — VN abstention 95→100%, MY 93→98.6%.
6. MY + ID prod restore (12,525 + 175,102 chunks).

**Open (not blocking SG):**

1. **JDIH doc_number convergence in eval matcher** — `sameDocNumber()` doesn't normalize
   short-form vs verbose; causes false eval misses (not real retrieval failures). Quick fix.
2. **MY wrong-jurisdiction case** — `new-abstain-sg-mas-regulation` asks about SG's MAS;
   scope gate sees "technology risk management" (legitimate MY topic). Structural: scope gate
   only checks topic, not jurisdiction. Fix: negative-jurisdiction signal (detect foreign
   regulator names). Honest failure for now (98.6% passes the floor).
3. **ID jdih 59 stragglers** — diminishing returns; manual runner exists.
4. **Reranker** — researched, deferred. Gated on MRR-ceiling measurement.
5. **Regulatory hierarchy boosting** — proposal only.
6. **VN duplicate citations — DONE + DEPLOYED 2026-07-17.** Root cause: `sectionCitationPart`
   ignored the parser's `~N` citation-path dedup; plus restart-numbered Điều per Chương/Mục.
   Fix: native ", Đoạn N" from `~N` on all VN kinds; Chương/Mục prefix only for colliding Điều;
   counter for colliding unnamed Phụ lục. Duplicates 2,601 → 72; chunks 53,286 → 52,891;
   16,150 chunks re-embedded (Kaggle); eval recall 84.1 → **86.6%**, MRR 64.0 → **66.1%**.
   Deployed via direct in-place restore into `banhmi` (no `_new`/rename — maintainer's standing
   instruction for banhmi AND laksa; rollback = the S3 dump). Residue (small, follow-up):
   (a) 36 duplicate pairs / 13 docs from duplicate silver sections — normalize should apply
   `~N` at section creation, not only index time; (b) verbose Chương-heading prefixes could be
   truncated; (c) 1089/QĐ-NHNN silver text has systematic missing-space OCR defects
   ("đểtặng", "tổchức") — needs re-extraction, separate concern.

**VN+MY code review findings (2026-07-17)** — agent review vs local corpora + cached files.
**All 11 items implemented, locally backfilled, and DEPLOYED to prod 2026-07-17**
(dump → S3 → disposable EC2 restore into `*_new` → rename swap in a ~3-min ECS bounce; old
corpora kept as `banhmi_old20260716`/`laksa_old20260716` for rollback — drop after soak).
vbpl re-discovery closed via temporary Hanoi-LZ proxy EC2 (`BANHMI_VBPL_PROXY_URL` wiring
committed): no missing in-scope docs — "trí tuệ nhân tạo" returns 0 on vbpl itself (their
indexing lag; AI laws already in corpus via vanban/congbao); 5 relation-target rows the vbpl
gateway 400s were dead-lettered. **Local eval after fixes: VN recall 84.1% / MRR 64.0% (from 82.9/56.1),
MY recall 92.9% / MRR 78.2% (from 91.4/76.7); current-law 100% both; all floors pass.**
The 3 re-parsed BNM PDs were refreshed locally (normalize→index→Kaggle embed→lexindex;
laksa 12,231 chunks, embeddings/sparse complete, 0 duplicate citations). Outcomes: VN 90 unknown-validity docs → in_force
(backfilled, 4 miss docs now retrievable); MY citations deduped (1,355 → 0, format `(a) [N]`);
VN chunks 56,203 → 53,286, MY 12,525 → 12,232 (dedup + <20-char filter); 3 superseded BNM PDs
expired with `replaces` relations (22 clauses reference out-of-corpus docs, left unresolved);
3 BNM PDs parse into sections (CCBM stays fulltext; needs re-normalize→index→embed→lexindex);
VN relations 2,995 remapped (3 opaque rows left: types 6/11/13, 1 row each); `payment-systems`
golden → Act 758; NCII seeded (MY only); 3 stuck fetch rows → error; 4/6 Act commencement dates
backfilled (519 phased, 701 has none). Deferred: vbpl re-discovery for "trí tuệ nhân tạo"
(needs VN network); SG/TH corpora untouched by new shared filters until their next index run.

*High:*

1. **VN unknown-validity starvation** — 95 docs / 9,983 chunks (~18%) with `status_class='unknown'`
   (vanban/sbv_hanoi carry no status; VN descriptor lacks `UnknownValidityInForce`, unlike MY/ID/SG/TH).
   Demoted to vector-only non-current pass (cap 3) → 4–5 eval misses (71/2025/QH15, 142/2026/ND-CP,
   58/2021/ND-CP, 134/2025/QH15). Fix: set flag in VN descriptor + backfill silver validity.
2. **MY duplicate chunk citations** — 1,355 chunks (10.8%, 402 groups) share citation strings
   (`sectionCitationPart()` returns raw label; parser's unique `citation_path` dedup suffix discarded —
   definition sections restarting (a)/(b)/(c) collide, e.g. Act 758 s.2 ×24). Fix scoped to MY/SG
   paragraph arm; deliberate `gold.chunk.citation` change (signed off 2026-07-17).

*Medium:*

3. **MY BNM supersession** — MCIPD 2017 + 2025 both served in-force; parse "Policy documents
   superseded" section into relations + expire the old doc.
4. **VN intra-doc duplicate chunks** — 2,803 chunks (5%) identical content per doc (tabular docs,
   e.g. 1089/QD-NHNN 1,129→127 unique). Content dedup at index time (shared).
5. **MY golden `payment-systems`** — expects repealed Act 627; retarget to FSA 2013 or abstain.
6. **VN vbpl relation codes** — 2,999 relations stored `vbpl_type_N` (only 3/14 codes mapped);
   seed `relation_type.csv` (type 9 ≈ is-implemented-by ×1,304, type 4 ≈ implements ×1,230).
7. **MY BNM PD fulltext fallback** — 4 PDs with bare `N.N` numbering miss `isBNMPolicyDoc()`
   (requires `S/G N.N`); extend detection.

*Low:*

8. **MY degenerate short chunks** — 177 chunks <50 chars (form labels/table fragments);
   min-length filter in shared indexing (validate VN/ID first — VN has zero).
9. **MY NCII abbreviation** — `abbreviation_expand` empty for MY; seed `NCII` (2 Act 854 s.22 misses).
10. **MY fetch hygiene** — 3 fetch_doc stuck `fetching`; 6 Acts missing commencement dates
    (`agclom/detail.go` regex misses their page format).
11. **VN misc** — vbpl keyword "trí tuệ nhân tạo" returned 0 last run (re-discover);
    unwrapped retry errors (sbvhanoi/vanban clients); per-call regex compile in
    `canonicalVBPLDocNumber`.

**ID code review findings (2026-07-17)** — 5-agent review vs local rendang DB + 9.1 GB cached files:

*Critical (3):*

1. **BPK FetchDetail un-normalized Number/DocType** — `parseDetail` stores verbose header
   (e.g. "Peraturan Badan Siber...Nomor 10 Tahun 2024") while listing normalizes to "BSSN 10/2024".
   Divergent doc_keys → 49 duplicate silver docs (370 rows = 321 unique). Fix: normalize in
   `parseDetail()` via `parseNumber()` + `bentukShort` map. **DONE** (detail now produces same short
   codes as listing; 3 new tests verify alignment).
2. **BI doc_key convergence failure** — `normalizeNumber`/`expandDocType` added in `5925a59` but
   corpus built before that commit. 899 BI bronze rows still carry raw short forms → 7 confirmed
   PBI duplicates + 28 partial docs can't merge with BPK copies. **DONE** — `normalizeNumber` now
   produces BPK short form ("PBI 10/2025", "PADG 15/2024") instead of verbose; `expandDocType`
   returns lowercase short codes ("pbi", "padg") matching BPK's `jenisCode`. Re-run BI fetch needed
   to update bronze rows.
3. **OJK numberTitleRe whitespace** — regex `\bNomor\s+(.+?)\s+tentang` fails on 2 malformed JDIH
   titles ("Nomor34/POJK.05/2015") → bare-number doc_keys → 190 duplicate chunks. Fix: `\s*` instead
   of `\s+`. **DONE** (regex now `\bNomor\s*(.+?)\s*tentang`; 2 edge-case tests added).

*Major (9):*

4. **Zero SEOJK in silver** — 234 discovered (25 BPK + 209 ojkweb), all fetched, none normalized.
   Complete coverage gap for OJK circulars. Likely normalize pipeline kind/type mapping gap.
   **INVESTIGATING.**
5. **Missing config.relation_type for source='ojk'** — 5,115 relations captured in
   `relation_evidence`, 0 promoted to `document_relation`. OJK uses passive voice (Dicabut/Diubah)
   vs BPK's active (Mencabut/Mengubah). Fix: seed CSV addition. **FIXING.**
6. **30 BI docs false-positive "in_force"** — marked Berlaku but revoked by forward-edge relations.
   Fix: post-pass validity override from relations.
7. **2,709 label-only "Pasal N" chunks** (1.5%) — `labelOnlyChunk` guard compares against child
   (ayat) citation, not parent (Pasal). Fix: also check parent label. **INVESTIGATING.**
8. **Penjelasan chunking reverted** — penjelasan (explanatory notes) are non-binding; chunking them
   mixed noise into the recall pool and contributed to the label-only chunk problem. Removed from the
   indexing switch. **DONE (reverted — penjelasan not chunked by design).**
9. **Zero BPK structured relations** — `statusPeraturanRe` likely doesn't match real HTML; 0
   `references_json` in bronze despite implemented parser. Only 19 weak_relation rows.
10. **BI PADG slash-form normalizer missing** — 174 PADG docs (65%) un-normalized. Fix: add
    `padgSlashRe`. **DONE** (`padgSlashRe` added; "22/24/PADG/2020" → "PADG 22/24/PADG/2020").
11. **BI "Nomor" form normalizer missing** — 23 docs (2026 regs) use "PBI Nomor" not "PBI NO.".
    Fix: broaden regex. **DONE** (regex now `(?:NO\.?\s*|NOMOR\s+)` for both PBI and PADG).
12. **ojkweb 100% NULL issued_at/status_raw** — 1,233 docs; detail-fetch ran for only 2.

*Key eval root-causes (25 recall misses):*
- **Dedup** — 251 groups, 540 docs → 289 removable duplicates waste doc_cap slots (3-4 recall cases).
  **DONE** — `docTypeKey` in `process_activities.go` now maps verbose Indonesian doc-type names to
  BPK short codes for cross-source convergence; merged ~25 duplicate docs (corpus now 2,876 docs /
  195,115 chunks locally; embeddings pending).
- **Abbreviation gaps** — POJK, PBI, PADG, SEOJK, PMK, PSTE, ITSK, PSE, OJK, BI, Komdigi added to
  `abbreviation_expand_id.csv`; also corrected QRIS and LPBBTI expansions. **DONE.**
- **Expired golden** — 2 cases target LPS 1/PLPS/2010 (expired); retarget to PLPS 1/2023. **DONE**
  (golden_id.json retargeted to "PERATURAN LEMBAGA PENJAMIN SIMPANAN 1/2023" / "LPS 2/2024" / "LPS 5/2024").
- **Omnibus parser** — UU 4/2023 stops at Pasal 9; Pasal 10-328 not chunked (+1 recall, +2 rank).
- **Ranking** — 15 cases are ranking-side (term mismatch, topic crowding, doc_cap limits).

*Measured 2026-07-19 (local, 2,372 docs / 159,026 chunks, all embedded + sparse): recall **76.3%**,
MRR **61.2%**, in-force 100%, abstain 100% — new ID baseline (floor stays 0.73). Landed:
eval-matcher canonicalization of verbose doc-type phrases, and **docKey-time doc_number
canonicalization** (`canonicalIDDocNumber` + code-prefix rule) — ~550 verbose/bare duplicate silver
docs merged (194,681 → 159,026 chunks; zero remaining dup pairs). Deep-probe split of the 20
remaining misses (`cmd/eval -pool-k 200`, pool recall **94.7%**): **14 ranking failures** (gold doc
inside the top-200 candidate pool — 2 within pool rank ≤7 — but outside top-8; reranker /
fusion-tuning headroom) and **6 retrieval failures** (absent from the pool: query-vocabulary gaps —
insiden siber / komputasi awan vs POJK 11 terminology, PBI 10/2024 CDD, POJK 8/2023 AML, ITSK
mandate, PJP security — candidates for abbreviation/synonym seeds or targeted chunk vocabulary).*

**Deployed 2026-07-19: v0.3.2-20260719 live on rendang.danny.vn** — RDS restore (159,026 chunks,
verified chunks=embeddings=sparse) + new MCP image (doc_cap=3, version stamp) in one ECS bounce;
prod `corpus_status` + search smoke verified. Residual: `43/PADK.03/2025` exists twice (one bronze
row mis-typed `SEOJK`, one bare ojkweb row) — source-side doc_type mismatch, needs an ojk detail
re-fetch or a type-correction rule.

**Deployed 2026-07-19: v0.3.2-20260719 live on banhmi.danny.vn + laksa.danny.vn** — VN recall
**92.7%** / MRR 68.5 (was 86.6/66.1), MY recall **94.3%** / MRR 79.1 (was 92.9/78.4). Fixes:
doc_cap=3 default; VN abbreviation_expand seed (CII statutory-term mapping + 7 banking
abbreviations); golden retarget (ics-scope → Điều 4) + `relation_ok` credit (83↔09/2024 amends);
sbv_hanoi zero-pad + type-from-suffix parser fixes; dedup — VN 3 doc pairs (09/2024, 117/2018,
368/2025; 345 chunks), MY 14 doc_type-reclassification orphans (945 chunks). Zero re-embedding
(deletes + query-time changes only). Restore flow: dump → S3 → disposable EC2 (origin SG,
`banhmi-pipeline-ec2` profile, AL2023 postgresql17) restoring into `*_v2` then rename-swap —
seconds of downtime; old DBs kept as `*_old20260719` for rollback (drop after burn-in).
Remaining misses are ranking-only (VN pool-recall 98.8%, MY 98.6%): the Kaggle
Qwen3-Reranker-0.6B offline experiment is the next lever. Eval floors not yet raised —
raise VN to recall≥0.90/mrr≥0.66, MY to ≥0.92/≥0.77 once the baseline is accepted.

**Deployed 2026-07-19 (night): VN corpus refresh live on banhmi.danny.vn (v0.4.0-20260719)** —
incremental discovery landed 39 vanban docs (fetch → Vision OCR 38 broken-encoding PDFs → index →
Kaggle delta embed). Scope-vocabulary bug found and fixed: 4 generic procedural terms
(`xử phạt hành chính`, `xử phạt vi phạm hành chính`, `nghị định xử phạt`, `sửa đổi bổ sung`) were
seeded `strong,banking`; strong ignores issuer, so the vanban sweep dragnetted **32 sectoral penalty
decrees** (forestry, customs, veterinary, …; 7,304 chunks; recall −2.4pp). Reclassified strong→weak
(weak requires a banking signal — the designed gate), re-seeded, surgically deleted the 32 (fetch
rows flagged out-of-scope), lexindex rebuilt. Kept in scope: **17+18/2026/TT-NHNN**,
**284/2026/NĐ-CP** (crypto-asset penalty decree), 2 ngoại hối docs, 2 amendment keepers (bases in
corpus). Corpus 52,546 → **53,667** chunks (= embeddings = sparse); `ingest.embedding_cache` seeded
(8,425 rows). **Eval: recall 90.2% / MRR 69.8% / current-law 100% / abstention 100% — floors pass**;
−2.4pp recall vs pre-refresh baseline = 2 marginal ranking cases (KNOWN-WEAK OCR-only AI-law case +
1 multi-citation), reranker headroom unchanged. In-place restore into `banhmi` per standing
instruction (dump `banhmi-20260719-2.dump`; rollback = `banhmi-20260719.dump`); prod
`corpus_status` + 17/2026/TT-NHNN search smoke verified. Residue: fetch 3515 (27/2018/NĐ-CP,
relation target) still has no local scan PDF — needs artifact reset + re-fetch to index its text.

**ID push 2026-07-19 (afternoon): recall 76.3% → 79.8%, MRR 62.4, pool-recall@200 98.2%
(160,142 chunks).** Landed: **scan-layer gate** (`fitz.ScanStats` + `extract.pdf.max_scan_image_ratio`
0.8 — a predominantly image-paged PDF's embedded OCR text layer is never binding; 27 ID gazette laws
carried "REPIJELIK"-grade layers that passed every text gate; VN was immune via diacritic density);
**alias-wide OCR selection** (OcrAll finds the PDF across all `document_alias` observations — post-dedup
the primary observation can be file-less; UU 4/2023 sat unselected behind a dead jdih link); **ojkweb
FAQ demotion** (FAQ PDFs never binding — one blocked P2SK's repair); **needs_review fallback rule**
(normalize never builds structure from non-OCR text the gate distrusted); **P2SK fully chunked** —
341/341 Pasal from clean Vision OCR (the "omnibus parser bug" was the garbage layer all along;
`p2sk-itsk-mandate` now rank 4); **sparse-arm abbreviation expansion** (regulations abbreviate what
queries spell out — PBI 23/6/PBI/2021 says PJP 325:2; dense-only expansion measurably failed; VN/MY
regression clean, VN MRR +1.2 to 69.7); **5 grounded ID seeds** (insiden TI, penyedia jasa TI, PJP,
APU PPT, CDD/pengguna jasa); **PADK dup merged** + OJK number-infix type override in docKey.
Remaining: ~17 in-pool ranking misses (reranker territory), 2 vocabulary-hard absents
(cyber-incident-bank, pjp-security). Setneg page-stamp stripping landed same day (line-level
cleaner on both PDF and OcrAll paths; local re-clean via Vision cache, recall-neutral).

**Closeout 2026-07-19 (evening): MY rename + KH scope, both deployed.**
- **MY:** bnm file stems URL-decode into readable doc numbers; 20 laksa docs renamed with
  pipeline-exact doc_keys (canonicalDocNumber edge-trim included), bronze aligned, re-indexed +
  delta-embedded. Recall 94.3% held, MRR 79.1 → **79.6**.
- **KH:** every wrong abstain was the scope gate (`out_of_domain`), not retrieval — the KH scope
  vocabulary lacked core banking terms. 15 grounded `scope_term_kh` additions: abstention
  **75% → 100%**, recall/MRR unchanged; abstain floor raised 0.60 → 0.95.
- **Floors now track the accepted baselines** for all six (VN 0.90/0.66, MY 0.92/0.77,
  ID 0.78/0.60, SG 0.90/0.75, TH 0.86/0.68, KH 0.90/0.70 + abstain 0.95). Rollback DBs dropped.

**KH rebuild 2026-07-20: deployed `v0.4.3-20260720` — 244/284 docs indexed, 7,757 chunks
(= embeddings = sparse), recall 94.4% / MRR 72.7% / current-law 100% / abstention 100%.**
The "missing OCR" was a fetch bug: nbc/cdcgov `FetchDetail` treats DetailURL as the PDF URL but
`Discover` stored the listing page — all 154 nbc + 53 cdc docs had listing HTML saved as their
"main PDF" (OCR'd website nav passed the content gate; caught only by content inspection). Fixed
DetailURL, re-crawled NBC via `/english/` pages only (the non-English pages carry only `*_kh`
PDFs; this also added TCRMG 2026, TRM Guidelines 2019, banking codes 2008–2021), widened the
Khmer-file filter (case-insensitive `_kh/`, `_kh.`, `-kh.`). NBC re-fetch through the KH
residential SOCKS5 via a local-DNS CONNECT forwarder (the proxy rejects SOCKS5 DOMAIN requests;
Go and Chromium both delegate DNS to SOCKS5 proxies — see SOURCES.md). 209 PDFs re-fetched (all
`%PDF-` verified); 91 docs born-digital binding, 117 OCR'd English kept, **40 Khmer-only scans
quarantined** (verified: 0.0 English fraction across start/mid/end + middle-page renders; they
stay as explicit `quality_gaps` coverage, incl. 13 report-genre items — scope question open).
Eval: `expected_citations` gained `alt_doc_numbers` (any-of identity match — the TRM guidelines
exist under odc + nbc identities); KH golden refreshed (8 `expect_fail` removed,
financial-inclusion marked known-gap); 3 grounded scope terms (capital adequacy, consumer
protection/complaint) fixed the last false abstains. Deploy: dump → S3 → disposable EC2
(user-data self-driving, marker to S3, self-terminating) → `amok_v2` → rename swap; rollback
`amok_old20260720` (drop after burn-in). Ops: per-jurisdiction storage layout restored
(`config.yaml` absolute `storage.dir` pin had disabled the `data/<jur>` default — flat store
split by hardlink + content-hash attribution, 314 junk residue deleted); S3 mirrors for all six
verified zero-diff via `aws s3 sync` (danny-banhmi-data-sg/-kh created, ap-southeast-1).
Residue/queued: KH cross-source dedup (TRM odc 1754 = nbc 2520), TCRMG-2026-supersedes-2019
relation, ID 30 / TH 19 docs with no PDF artifact (fetch gaps), TCRMG TOC-line citation noise.

### Reranker experiment — CONCLUDED: NOT DEPLOYING (2026-07-19)

**Decision: keep current retrieval; no reranker.** The naive first pass (rerank all deep
candidates) measurably HURT all three corpora — VN 92.7→82.9, MY 94.3→92.9, ID 79.8→76.3 —
because a semantic reranker eagerly promotes superseded versions of the right rules
(current-law precision fell to 86.7% on VN, whose supersession chains are longest). A
production-shaped round 2 (rerank strict current-law candidates only, badged tail pinned)
was prepared but cancelled: MY's flat result and the latency economics (measured 0.76 s/pair
CPU INT8 ⇒ top-16 ≈ 12 s/query; ID's misses hide at pool ranks 22–79 needing N≈50 ≈ 38 s)
made the ceiling not worth the read-path cost at current quality. Current baselines accepted.

**Retained for a future revisit** (bigger reranker, grown corpora, or GPU quota): the full
offline harness — `cmd/eval -rerank-dump` / `-rerank-scores` (candidates out, scores
replayed through the standard matcher with the non-current tail preserved) and
`tools/rerankkernel` (T4-pinned Kaggle scoring with dataset readiness gating; `-cleanup`
removes the kernel+dataset). The long-game alternative stays reranker-as-teacher:
distill its judgments into embedder fine-tuning (MVP2).

### Original experiment plan (for the record)

All large corpora now have pool-recall ≥98% — retrieval finds nearly everything; ordering is the
last gap. The experiment converts that ceiling into a measured number before any infra decision.

1. **Dump** — `cmd/eval -rerank-dump`: per golden case (VN/MY/ID), retrieve top-50 candidates
   (per-doc cap lifted) and write query + candidate texts as JSONL.
2. **Score on Kaggle T4** — Qwen3-Reranker-0.6B FP16 (pairs with the Qwen3 embedder; the smallest
   credible multilingual reranker covering Vietnamese/Indonesian), yes/no-logit scoring per the
   model card. Kaggle dataset I/O, same offload pattern as bulk embedding; no local GPU, no WWAN
   model download.
3. **Rescore offline** — `cmd/eval -rerank-scores`: reorder candidates by reranker score, apply
   doc_cap + top-8, score with the standard matcher; report recall@8/MRR deltas per jurisdiction.
4. **Decision gate** — meaningful lift (≈ ID recall +4pp or VN MRR +8pp) → design the query-time
   deployment options (bigger ARM origin vs eval-only insight); weak lift → document and stop.
   Query-time reranking on the current t4g origin is already known-infeasible (~3–8 s per
   candidate pair on 2 vCPUs); the experiment informs whether new infra is worth pricing.

### v0.3.3 — Post-v0.3.2 hardening — TAGGED (2026-07-19)

Tags the 23 commits since `v0.3.2` (the code behind the 2026-07-19 deploys and baselines above):

1. **Extraction trust:** scan-layer gate (`fitz.ScanStats` + `extract.pdf.max_scan_image_ratio`),
   alias-wide OCR selection in `OcrAll`, ojkweb FAQ demotion, `needs_review` fallback rule.
2. **Retrieval:** sparse-arm abbreviation expansion; `doc_cap=3` default; eval-matcher canonicalization.
3. **Identity:** docKey canonicalization + dup merges (VN/MY/ID), BNM file-stem URL-decode + MY rename,
   sbvhanoi zero-pad + type-from-suffix, OJK number-infix type override, Setneg stamp stripping.
4. **Eval:** floors raised to the accepted baselines (all six), KH scope vocabulary, reranker
   experiment harness + the NOT-DEPLOYING decision (section above).
5. **Code review (4 Opus agents over the full v0.3.2..HEAD diff, 2026-07-19):** no critical or major
   defects; minors fixed in the tag — stale dense-arm-only comments in `pkg/rag/retrieve`,
   `hasNearbyGazetteHeader` rename, rerank-dump close/flush error handling, rerankkernel glob error
   split, `max_scan_image_ratio` pinned in `TestGateFromSettings`. Noted, not acted on: BNM literal-`+`
   filename edge (no real-world trigger), pre-existing TTLT suffix gap, ScanStats per-page HTML-error
   skew (PLAUSIBLE, low probability).

> **Version renumbering:** releases are git-tagged `v0.3.x`; the "v0.4.0/v0.5.0" labels previously
> attached to the SG/TH rollouts below were plan labels, not tags. From here, v0.4.0 = the embedder
> split (next section).

### v0.4.5 — VN SBV sweep take-all — CODED (2026-07-24)

The vbpl SBV agency sweep (`agencyIds: [62, 908]`) is now **pre-scoped**: `ingest.SweepInScoper`
lets a source declare its empty-keyword sweep in scope by construction, and the discover activity
enqueues those docs without `scope.Match` (ledger provenance `sweep`). Trigger: 04/2025/TT-NHNN
(records retention) and ~54 other 2025 TT-NHNN circulars were vocabulary-dropped — the live SBV
feed's newest 200 docs held 65 circulars from 2025 vs 11 in the corpus. Decision 2026-07-24:
everything the SBV issues is banking regulation; the vocabulary precision boundary now applies only
to non-SBV paths. Also: `tt-nhnn` seeded `strong_title` (other VN sources catch SBV circulars by
số ký hiệu); vbpl feed VBHN detection fixed (feed leaves `isConsolidatedDocument` null — doc-type
code/số-ký-hiệu now decide). **VBHN consolidations stay vocabulary-gated** (phase 2 after
consolidation-indexing design check; ~half the 2,460-doc feed). **Re-sweep DONE locally 2026-07-24**
(direct VN access): 2,461 feed docs → 1,674 in scope → VN corpus 1,781 → 3,627 docs, 52.5K → 93.3K
chunks; invariants chunks=embeddings=sparse verified; eval-vn recall 90.2 / MRR 67.6 / in-force 100 /
abstain 100 — floors pass (baseline 92.7/69.7 predates the doubled corpus; new accepted VN baseline:
recall 90.2 / MRR 67.6 on the 3,633-doc corpus). **Corpus DEPLOYED to prod RDS 2026-07-24**
(dump → S3 → disposable EC2 restore into `banhmi_v2` → verified counts → instant swap; rollback DB
dropped after live verification via prod MCP: 04/2025/TT-NHNN + Phụ lục + amendment 37/2026/TT-NHNN
all serving). MCP image NOT redeployed — retrieval code unchanged; see follow-ups.

**Appendix supplementation (same date):** vbpl tree-normalize drops appendices (the tree covers only
the enacting body) — 225 VN docs had Phụ lục text but no phuluc sections. Fix: tree path now recovers
root-level Phụ lục sections from the binding extracted text (`appendixSectionsFromText` +
`mergeAppendixRoots`, tree's own phuluc wins); 279 docs now carry appendix chunks (04/2025/TT-NHNN
retention schedule: 38 chunks). Repair lesson re-learned: per-id `-normalize` bypasses the
priority selector — always re-normalize via the document's highest-priority alias (vbpl=10).

**Open follow-ups from v0.4.5 (tracked; maintainer schedules):**

1. **Tag + ECS bounce — needed, not urgent.** Prod image still reports `v0.4.3-20260721` in
   `corpus_status`; a new tag + `force-new-deployment` ships the correct snapshot string plus the
   pending v0.4.4/v0.4.5 code (both write-path/`files[]` — no retrieval behavior change today).
2. **Query-scope terms for take-all topics — recommended.** Retention queries return full evidence
   but badge `abstain/out_of_domain`: query-time scope has no term for them (`tt-nhnn` only matches
   queries citing a số ký hiệu). Seed e.g. `thời hạn lưu trữ` (weak) + re-seed prod config. Same
   class of gap likely for other newly-swept topics (prudential, FX) — sample before seeding.
3. **VBHN consolidations phase 2 — decision pending.** Design check for consolidation indexing
   (dedup vs primary docs, validity presentation), then un-gate + `vbhn-nhnn` strong_title seed;
   ~half the 2,460-doc SBV feed.
4. **04/2025/TT-NHNN `issued_at` wrong — data fix.** vbpl feed says 2024-05-15; the circular is
   dated 2025-05-15. Source metadata error; audit other 2025 circulars for the same off-by-a-year.
5. **Spaced-diacritic PDF artifacts — cosmetic.** 11 appendix chunks carry mupdf spacing soup
   ("tà i liệ u"); hurts BM25 on those chunks only. Candidate for a normalize-time cleanup pass.
6. **Stragglers — low value.** 4 ancient sweep docs (broken vbpl details), 5 relation-target
   fetch errors, 6 OCR "no output" docs — all pre-existing classes, retry on next refresh.

### v0.4.4 — Document file download links — CODED (2026-07-21)

`document` now returns **`files[]`** — the downloadable official artifacts, merged across sibling
sources by sha256 — with durable **`origin_urls[]`** direct links where an official source serves
one, and **`files_url`** on the vbpl `sources[]` entry (its stable listing endpoint minting fresh
~24h presigned links; VN-reachable only). Original links only, no self-hosted copies (decision
2026-07-21). Measured on the local VN corpus: vbpl file URLs are 100% expiring presigned
(unsigned = 403); 208/1,781 docs carry ≥1 durable original link via vanban/congbao/sbv_hanoi.
Verified from prod EC2: the vbpl gateway is geo-blocked from AWS, but minted presigned links work
globally. Coded + tested locally (real-corpus integration test); not yet deployed.

### v0.4.3 — Language-rule and jurisdiction-neutral schema copy — DEPLOYED (2026-07-20)

- Shared tool-schema descriptions no longer leak cross-jurisdiction examples or the false
  "index is multilingual" claim: the search query description now states the native-language
  rule; doc_number/citation/location/source/doc_type descriptions are jurisdiction-neutral.
- VN/ID/TH guide texts drop "English works via cross-lingual matching" — native language ONLY,
  consistent with the search tool descriptions.

### v0.4.2 — Issuer filter fix + listing surface — DEPLOYED (2026-07-19)

**Problem (found via ChatGPT app testing):** a search with an `issuer` filter returned zero hits
unless the string matched stored metadata exactly — and MY/SG/TH/KH have no issuer metadata at
all (VN full, ID 289/2,371 empty), so any issuer filter blanked those corpora.

- **Fix:** issuer pre-filter is now case-insensitive **substring** match (LIKE, escaped); the
  search schema's issuer description carries the corpus's real issuer vocabulary (read at
  startup) or an "omit this filter" warning when the corpus has none; the no-evidence gap
  names the filters as a likely cause. Backfilling issuer metadata for MY/SG/TH/KH is a
  separate, future normalize+backfill decision.
- **Also in v0.4.x line (2026-07-19):** directory-listing surface (/privacy, /terms, /support,
  /demo.mp4 → public S3, S3-backed openai-apps-challenge), explicit destructiveHint on all
  tools, footer links, docs/PUBLISHING.md.

### v0.4.1 — Concurrent query embedding — DEPLOYED (2026-07-19)

**Problem:** the onnx embedder held one mutex across tokenize + inference + pooling — every search
across all six jurisdictions queued behind a single run (head-of-line blocking).

- **Fix:** bounded semaphore instead of the mutex (`BANHMI_EMBED_CONCURRENCY`, default NumCPU);
  ORT `Session.Run` is concurrency-safe on one session (weights shared, activations per-run).
  Tokenizer FFI stays behind its own sub-ms lock. Commit `a11f242`.
- **Memory model:** ~23 MB per in-flight run (measured locally and confirmed in prod: 8-burst
  cost ~190 MB). Prod cap `BANHMI_EMBED_CONCURRENCY=10` ⇒ ≤ ~350 MB burst against ~1.1 GB
  host MemAvailable with a 300 MB floor.
- **Hard cgroup limits dropped** (`banhmi-embedder:3`, `banhmi-mcp:15`): both services keep only
  `memoryReservation` (2400/300). Rationale: MCP + embedder are one serving chain — killing either
  kills `search`; the semaphore is the real memory guard, and the old 2900 MB hard limit would have
  OOM-killed the embedder (~4 min cold start) while the host still had ~1 GB free. AZ rebalancing
  disabled on both services (single-instance cluster; it blocks `maximumPercent=100` deploys).
- **Verified in prod:** 8 parallel MCP sessions → 8 `embeddings: start` within 46 ms, all done
  ~5.2 s (2 vCPU time-slicing; solo run ~0.6 s); correct evidence packs from VN + MY corpora.

### v0.4.0 — Read-path embedder split: two ECS services — DEPLOYED (2026-07-19)

**Prod cutover 2026-07-19:** `banhmi-mcp:14` (slim, sha-pinned `1da9e3e88b4d`, 28 MB) +
`banhmi-embedder:2` live on the t4g.medium; verified over live MCP — `corpus_status` reports
`v0.4.0-20260719`/`search_ready`, and a Vietnamese cloud-outsourcing search returned the correct
09/2020/TT-NHNN Điều 33/34/35 evidence through the CloudFront → slim MCP → loopback embedder → RDS
path. Soak closed same day (maintainer call): pre-split rollback image + `banhmi-mcp:12`
retired; rollback from here = previous slim revision or rebuild from tag. ECR now holds only
the live pair (932 MB); untagged-expiry lifecycle + 30-day log retention set.

**Local validation (2026-07-19):** VN eval through the split stack reproduced the accepted baseline
**exactly** — recall 92.7%, MRR 69.7%, current-law 100%, abstention 100% (80 cases, floors pass).
HTTP hop + JSON overhead ~1 ms (client p50 197 ms vs inference p50 196 ms).

**Cutover incidents (2 rollbacks, ~9 + ~4 min outages, both fixed + runbook'd):**
1. `distroless/static` shipped no dynamic loader — Go 1.26 arm64 emits PIE (dynamic) even with
   CGO off; slim image now uses `distroless/cc`.
2. `CGO_ENABLED=0` flipped go-fitz from static-MuPDF to purego `dlopen` at package init — slim
   build keeps CGO on. (Follow-up idea, v0.4.x: stop linking go-fitz into `cmd/server` at all —
   composition-root split.)
3. Process fixes now in the runbook: sha-pinned images only (never `:latest` in task defs),
   pre-flip image validation, MCP-first flip order on the 4 GB host.

**Goal:** move the query-time CPU embedder out of the MCP server process into its own ECS service on
the **same t4g.medium host** (4 GB — 3,829 MB registered to ECS; both services CANNOT hold reservations alongside the old in-process task, so cutover flips MCP first) (same cost, no new infra). MCP keeps everything else — query
pre-processing, retrieval SQL, evidence assembly; the embedder is stateless text→vector only, no DB
credentials.

- **Why:** ORT pre-packs the FP16 weights into ~2.1 GB private RSS per process (measured 2026-07-13).
  Splitting makes the MCP image small (bounces in seconds, no 40 s model reload on every code
  deploy). (Hard ECS memory limits were dropped in v0.4.1 — the semaphore is the memory guard.)
- **Topology:** second ECS service `embedder` — new `cmd/embedder` (promoted from
  `cmd/pipeline -serve-embed`, which is retired), `-tags onnx`, model+tokenizer baked into the
  image, bound to `127.0.0.1:8089` (host networking ⇒ shared loopback, ~0 ms hop, nothing exposed).
  MCP image drops the onnx tag + model files; `BANHMI_EMBED_QUERY` unset ⇒ existing HTTP client
  path with `BANHMI_EMBED_ENDPOINT=http://127.0.0.1:8089`.

**Decisions (settled 2026-07-19):**

1. **API contract — OpenAI-compatible** `POST /embeddings` + Bearer token (kept even on loopback;
   Secrets Manager). The existing `embed.New` client works unchanged; any consumer can use an
   OpenAI SDK. gRPC rejected: one short query → one 1024-d vector, ~50 ms inference dominates.
2. **Substrate — second ECS service, no K8S.** Same host, $0. k3s/EKS rejected for now (cost/ops
   disproportionate); MCP-on-K8S and per-jurisdiction MCP containers are possible later versions —
   this split is their prerequisite either way.
3. **Readiness** — embedder `/ready` flips only after a real warm-up inference; ECS health check
   targets it. Embedder deploys accept a ~60 s outage (min-healthy 0%, same stance as the no-ALB
   bounce) — they are rare (only ORT/model pin changes).
4. **Failure mode — hard error.** Embedder down ⇒ MCP `search` returns an explicit retryable
   "embedder unavailable" error after a short client retry. **No BM25-only fallback in prod** — the
   abstain floor gates on dense cosine and cannot run without it (evidence honesty). BM25-only
   stays an eval-harness mode.
5. **Parity guard** — MCP probe-embeds one text at startup (retries while the embedder warms) and
   refuses readiness unless dims = 1024 and model tag = `config.EmbedModel`. Index/query parity
   stays a hard invariant (Qwen3-Embedding-0.6B ONNX FP16).
6. **Latency budget** — +10 ms over in-process (~50 ms) allowed; measured before cutover, abort
   threshold p95 > 150 ms.
7. **compliary sharing — deferred.** VPC-internal only; the sharing surface (exposure, auth,
   quotas) is designed when compliary actually consumes it.

**Build order:** `cmd/embedder` + contract refactor → embedder Containerfile (model moves there) +
slim MCP image → MCP parity guard + hard-error surface → local two-container compose, `eval-vn`
parity (recall/MRR must equal in-process baseline) + latency measurement → new ECS task def/service,
MCP env flip, bounce → docs (DEPLOYMENT.md, ARCHITECTURE.md). Local dev + `make eval-*` keep the
in-process ONNX embedder (no eval behavior change). **Rollback:** flip MCP task def back to
`BANHMI_EMBED_QUERY=onnx`; the last onnx-capable MCP image tag stays in ECR for one release.

### Singapore (`kaya`) — DEPLOYED (2026-07-17)

Singapore deployed as fourth jurisdiction. English corpus; MY citation family.
Sources: SSO (Acts, 29 docs), MAS (Notices/Guidelines via Solr API, 244 docs),
PDPC (advisory guidelines, 7 docs), CSA (CII docs, 13 docs). `sg-act` parser
with MAS paragraph-level parsing + em-dash/Schedule case-sensitivity fixes.
292 docs / 27,951 chunks. Eval: recall 84.8%, MRR 72.6%.

**Open (SG):**
1. **29 known-gap golden cases** — SSO Act PDFs still lose some body sections (ToC/body interleaving).
   Switch to SSO HTML fetch (`?ProvIds=pr{N}-`) for full provision coverage.
2. **47 empty doc_number MAS docs** — MAS Guidelines with no formal number (descriptive titles only).
3. **Wrong-jurisdiction abstention** — 2 honest failures (BNM/OJK questions match SG topic scope).
Follows the [playbook](docs/design/jurisdictions/PLAYBOOK.md) and
[SINGAPORE design](docs/design/jurisdictions/SINGAPORE.md).

**Sources (all live-verified 2026-07-16, Playwright-confirmed — no proxy needed):**

| Source | Docs | Discovery | Access |
|---|---|---|---|
| **SSO** | ~520 Acts + 79 SL (7+ target) | Browse `/Act/Current/{letter}?PageSize=100` A-Z | Browser UA only; no WAF; PDF via `?ViewType=Pdf`; HTML lazy-loads (PDF preferred) |
| **MAS** | 321 Notices + 123 Guidelines | **Solr API** `GET /api/v1/search?fq=mas_contenttype_s:"Notices"&rows=500` — all in one call | Akamai UA-only check (no cookie minter needed); `ChromeTransport()` suffices |
| **PDPC** | ~10 Advisory Guidelines + 7 sector | **JSON API** `GET /api/listing-api?listingtype=regulatory_guidance` | No WAF; PDFs on `files.app.optical.gov.sg` CDN |
| **CSA** | ~20-30 CII/cyber docs | Sitemap `sitemap.xml` + HTML scrape | Isomer CMS; PDFs at `isomer-user-content.by.gov.sg/36/{uuid}/{file}.pdf`; needs non-empty UA |

**Build order:** SSO → PDPC → CSA → MAS.

**Phase 1 — Seam + spike** (playbook steps 1-3):
1. Jurisdiction registry entry (`sg`): `Code=sg`, `DBName=kaya`, `StructureParser=sg-act`,
   `ParagraphLabel=Paragraph`, `UnknownValidityInForce=true`, `OCRLanguages=en`.
2. Scope vocabulary: `scope_term_sg.csv` — fork MY English base, add SG-specific terms (TRM, FSMA,
   MAS Notice, FSM-N, PSN, digital bank licence, e-payments, PDPA advisory, CII).
3. Structure parser spike: fetch Payment Services Act 2019 PDF from SSO (`/Act/PSA2019?ViewType=Pdf`),
   run go-fitz, verify Section/Part/Chapter parse → exact section inventory, 0 gaps.
4. Wire `sg` in `pkg/app` source builder + `TestSourceBuildersCoverRegistry`.

**Phase 2 — Sources** (playbook step 4):
1. `pkg/ingest/sso/` — Browse A-Z discovery (`/Browse/Act/Current/{letter}?PageSize=100`), scope-filter
   by title keywords, PDF fetch (`?ViewType=Pdf`). SL listing per Act (`?ViewType=Sl`). Browser UA
   header via `ChromeTransport()`. Metadata from `data-json` (timeline, revision info).
2. `pkg/ingest/pdpc/` — JSON API discovery (`/api/listing-api`), filter `Advisory Guidelines` +
   `Sector-Specific Guidelines`. PDF fetch from Optical CDN (`files.app.optical.gov.sg`).
3. `pkg/ingest/csa/` — sitemap discovery, filter `/legislation/` + `/resources/publications/` URLs,
   extract `isomer-user-content.by.gov.sg` PDF links from HTML. Non-empty UA required.
4. `pkg/ingest/mas/` — Solr API discovery (`/api/v1/search?fq=mas_contenttype_s:"Notices"&rows=500`),
   scope-filter by `mas_sector_sm` (Banking, Payments). HTML page scrape for metadata (issued-pursuant,
   applies-to, amendment notes, PDF URL). PDF fetch. `ChromeTransport()` for Akamai UA check.
   Parse `[Cancelled]` from title for validity status.

**Phase 3 — Extract + normalize** (playbook step 5):
1. SG structure parser (`sg-act`): reuse MY Section/Part/Chapter hierarchy. SSO PDFs are born-digital;
   go-fitz extracts clean text. MAS notice-paragraph parser (new, small: "para 4.2" style).
2. Validity: SSO Acts = in-force (consolidated editions); MAS = parse `[Cancelled]` + amendment notes
   for revocation dates. Map via `config.validity_status`.
3. Relations: SSO timeline versions; MAS FSMA supersession (topic-matched cancellation inference).

**Phase 4 — Index + serve** (playbook step 6):
1. Chunker walks Section → Subsection "(1)" → Paragraph "(a)" with English labels.
2. MCP brief for `sg` jurisdiction (identity, guide, tool descriptions in English).
3. Golden set: `golden_sg.json` — 50+ practical scenario-based questions in English (banking tech risk,
   payment services, data protection, cybersecurity, outsourcing, digital banking).
4. Eval gate: recall/MRR/in-force/abstention baseline. Floors TBD from first run.

**Phase 5 — Deploy** (playbook step 7):
1. RDS: `CREATE DATABASE kaya` on the shared instance. `make migrate` + `make seed` with `BANHMI_JURISDICTION=sg`.
2. Pipeline: `go run ./cmd/pipeline -run-all` with `BANHMI_JURISDICTION=sg`. Bulk embed on Kaggle.
3. ECS: fourth container `:8084` (same image digest, env `BANHMI_JURISDICTION=sg BANHMI_DATABASE_NAME=kaya`).
4. CloudFront: new distribution → `kaya.danny.vn`. ACM cert.
5. Validate: Haiku stand-in agent over live MCP — search, document, corpus_status, quality_gaps.

### Thailand (`tomyum`) — DEPLOYED (2026-07-17)

Thailand deployed as fifth jurisdiction. Thai corpus with TCC word segmentation (pure Go).
Sources: OCS (75 Acts via getLawDoc API), BOT (1,476 notifications via WebForms + PDF),
ETDA (1 doc). SEC deferred (needs Bangkok proxy for PDFs). Vision OCR for 1,204 scanned
BOT docs. 1,551 docs / 29,736 chunks. Eval: recall 80.7%, MRR 66.8%.

**Open (TH):**
1. **TH SEC source** — 120-200 docs on capital.sec.or.th. PDFs geo-blocked by F5 BIG-IP.
   AWS ap-southeast-7 (Bangkok) t4g.micro proxy ready but not deployed yet.
2. **270 BOT docs not fetched** — FetchDetail fails on short packIds (<8 chars). Fix packId
   URL construction edge case.
3. **OCS text quality** — OCS getLawDoc returns HTML; some sections have noisy whitespace.
   Content gate may reject some as non-binding. Review OCR vs API text authority.
4. **Wrong-jurisdiction abstention** — 3 honest failures (VN/MY/ID questions match TH topic scope).
5. **ETDA scope matching** — only 1 of 46 ETDA docs in scope. Thai scope terms need tuning.
Follows the [playbook](docs/design/jurisdictions/PLAYBOOK.md) and
[THAILAND design](docs/design/jurisdictions/THAILAND.md).

**Sources (all live-verified 2026-07-16, Playwright-confirmed):**

| Source | Docs | Discovery | Access |
|---|---|---|---|
| **OCS** | 1,884 Acts (6+ target) | **JSON API** `POST /searchlaw/indexs/list_table_search` — paginated, client-side filter | No WAF; bad TLS cert (InsecureSkipVerify); full text via Angular SPA viewer |
| **BOT** | ~1,560 active in-scope | WebForms POST, 30/page, ViewState pagination | No WAF; PDFs at `www.bot.or.th/content/dam/bot/fipcs/documents/{GROUP}/{YEAR_BE}/ThaiPDF/{PACKID}.pdf` |
| **ETDA** | ~100-120 instruments | HTML scrape (5 listing pages) | No WAF; PDFs at `getattachment/{GUID}/{file}.aspx`; intermittent connectivity |
| **SEC** | ~120-200 (digital assets + IT) | PHP POST `capital.sec.or.th/webapp/nrs/nrs_main_search.php` | **F5 BIG-IP geo-blocks non-TH IPs** — needs Thai proxy (AWS ap-southeast-7 or Thai VPS) |

**Build order:** OCS → BOT → ETDA → SEC (proxy-gated, Phase 2 of TH).

**Phase 0 — Language work (BLOCKER, before any source):**
1. Thai word segmentation decision: nlpo3 (Rust FFI, ~89% accuracy, fast) vs TCC-gram (pure Go regex,
   no dictionary, comparable IR recall). Interim fallback: vector-primary router for TH (Qwen3 handles
   Thai natively). TextNormalizer seam is ready; TH normalizer must NOT NFD-strip.
2. B.E./C.E. date parser (~50 lines Go): B.E. = CE + 543. Thai numerals (๐–๙) → Arabic; formal
   sources use พ.ศ. prefix. Pre-1941 edge: offset 542 for Jan-Mar.
3. Thai numeral normalizer: NFKD/NFKC do NOT convert ๐–๙; explicit mapping required. Normalize on
   ingest, index both forms for BM25.

**Phase 1 — Seam + spike** (playbook steps 1-3):
1. Jurisdiction registry entry (`th`): `Code=th`, `DBName=tomyum`, `StructureParser=th-act`,
   `ParagraphLabel=วรรค`, `UnknownValidityInForce=true`, `OCRLanguages=th`.
2. Scope vocabulary: `scope_term_th.csv` — Thai banking/finance/tech terms (ธนาคาร, สถาบันการเงิน,
   การชำระเงิน, ไซเบอร์, ข้อมูลส่วนบุคคล, ธุรกรรมอิเล็กทรอนิกส์, etc.).
3. Structure parser spike: fetch PDPA B.E. 2562 from OCS API (`getLawDoc` with `encTimelineID`),
   parse structured JSON sections (sectionTypeId 4=มาตรา, 8=หมวด, 9=ส่วน) → exact section inventory.
4. Wire `th` in `pkg/app` source builder + `TestSourceBuildersCoverRegistry`.

**Phase 2 — Sources (Phase 1: OCS + BOT + ETDA):**
1. `pkg/ingest/ocs/` — Paginate discovery API (`GET www.ocs.go.th/searchlaw/indexs/list_table_search?page={N}`,
   189 pages, `InsecureSkipVerify` for TLS). Filter `lawCode` by `-1B-` (Acts). Full text via
   `POST searchlaw.ocs.go.th/ocs-api/public/doc/getLawDoc` (structured JSON: `lawSections[]` with
   sectionTypeId/sectionNo/sectionContent HTML + `footnoteList[]` amendment annotations + `timelines[]`
   version history). Subordinate legislation from `childrens` field. PDF fallback via `fileUUID`.
2. `pkg/ingest/bot/` — WebForms session: initial GET for `ASP.NET_SessionId` + ViewState, then POST
   with DocGroup filter (1=Financial Institutions, 3=Payment Systems) + ViewState pagination
   (`ctl33$ddlPageSelector`). Parse 6-column table (type, date, packId, title, status img alt, PDF links).
   Metadata from summary page (`PFIPCS_summary.aspx?packId={PACKID}` — dates, purpose, substance).
   PDF fetch direct from `www.bot.or.th/content/dam/...` (no auth). TIS-620 → UTF-8 decode.
3. `pkg/ingest/etda/` — Scrape 5 listing pages (DPS, Digital ID, ETC, Digital Law, Recommendations).
   Parse `getattachment/{GUID}` links for PDF URLs. Dedup by GUID across pages. Retry on intermittent
   timeouts. Born-digital PDFs, Thai + some English translations.

**Phase 3 — Sources (Phase 2: SEC, proxy-gated):**
1. Thai proxy: launch t4g.micro in AWS `ap-southeast-7` (Bangkok), install tinyproxy, on-demand
   (~$0.005/hr). Set `BANHMI_SEC_PROXY_URL` (same pattern as `BANHMI_OJK_PROXY_URL`).
2. `pkg/ingest/sec/` — PHP POST discovery (`capital.sec.or.th/webapp/nrs/nrs_main_search.php`,
   SearchCat=1299 digital assets + 1346 IT systems). Parse HTML table for NRS IDs, titles, status,
   dates. PDF fetch from `publish.sec.or.th/nrs/{NRS_ID}s.pdf` via Thai proxy. Prefer
   `{id}p.docx` → `{id}p_r.pdf` → `{id}s.pdf` cascade. TIS-620/CP874 → UTF-8.

**Phase 4 — Extract + normalize** (playbook step 5):
1. TH structure parser (`th-act`): two families — OCS Acts use มาตรา (Section) in หมวด (Chapter) /
   ส่วน (Part); BOT notifications use ข้อ (clause). วรรค (paragraph) uses ordinal words
   (วรรคหนึ่ง/สอง/สาม), not numbers. Amendment suffixes: ทวิ, ตรี, จัตวา (Pali/Sanskrit).
2. OCS structured JSON → `[]Section` directly (sectionContent is HTML, strip to text).
   BOT/ETDA/SEC PDFs → go-fitz + TH structure parser.
3. Validity: OCS `stateId` (01=in-force, 00=superseded) + `timelines[]` for amendment chain.
   BOT status from listing img alt (`ใช้อยู่`/`ยกเลิก`). SEC from `ready_flag.png` icon.
4. Relations: OCS `footnoteList[]` amendment annotations + `childrens` subordinate links.

**Phase 5 — Index + serve** (playbook step 6):
1. Chunker walks มาตรา → วรรค → (๑)(๒) items with Thai labels. BOT chunks by ข้อ.
2. Lexical arm: Thai word segmentation decision from Phase 0 applied here. BM25 tokenizer via
   TextNormalizer seam (TH profile). Index both Thai numerals and Arabic forms.
3. MCP brief for `th` jurisdiction (identity, guide in Thai context, tool descriptions).
4. Golden set: `golden_th.json` — practical scenario-based questions in Thai (banking regulation,
   payment systems, data protection, cybersecurity, e-transactions).
5. Eval gate: recall/MRR/in-force/abstention baseline. Floors TBD from first run.

**Phase 6 — Deploy** (playbook step 7):
1. RDS: `CREATE DATABASE tomyum`. `make migrate` + `make seed` with `BANHMI_JURISDICTION=th`.
2. Pipeline: `go run ./cmd/pipeline -run-all` with `BANHMI_JURISDICTION=th`. Bulk embed on Kaggle.
3. ECS: fifth container `:8085` (env `BANHMI_JURISDICTION=th BANHMI_DATABASE_NAME=tomyum`).
4. CloudFront: new distribution → `tomyum.danny.vn`. ACM cert.
5. Validate: Haiku stand-in agent over live MCP.

### MVP2 candidates (parked)

Gemma 4 E4B OCR enhancement, figure extraction, manual-folder source, crawl depth >1,
`sbv.gov.vn` extra source, reranker-as-teacher embedder distillation (serving reranker
rejected 2026-07-19), validity/amendment refresh re-crawl, drift & quality monitoring.

## Milestone history

- **2026-06-01** — VN deployed (Cloud Run + Firebase).
- **2026-06-22** — Malaysia deployed. Hybrid retrieval (BM25 + RRF).
- **2026-07-06** — Indonesia LIVE. Temporal removed.
- **2026-07-08** — Qwen3-Embedding FP16, go-fitz, Document AI OCR.
- **2026-07-12** — **v0.3.0 shipped.** AWS read path (CloudFront + ECS ARM64). GCP teardown. ID revived.
- **2026-07-13** — ID scope rebuild (bpk sweep-only, issuer-mandate scope, ojkweb source).
- **2026-07-14** — Corpus rebuild + RDS restore (all 3). Kaggle dual-T4 OOM root-caused (per-run arena
  shrinkage). **v0.3.1 + v0.3.1b deployed** (MCP token optimization + amendment-chain awareness).
- **2026-07-15** — OCR engine -> Vision `images:annotate`; OCR backfill (VN +8,334 chunks); MY scope
  expansion (46 -> 97 docs); eval overhaul (jurisdiction-aware provision matcher, local eval targets,
  VN validity regression fixed, K=100, ID abstention 100%). **v0.3.1 tagged + image deployed.**
- **2026-07-16** — v0.3.2 bulk: diacritics seam + restoration, MY parser overhaul + SC dedup + BNM
  Liferay fix + Online Safety Act 866, VN label-only chunk fix + vbpl empty-body recovery, ID parser
  fixes + OJK source wiring + BI terminal-state fix, chunk dedup, abbreviation expansion,
  promotion-only aggregation, per-doc cap, golden expansion (VN 80/MY 73/ID 110), workflow eval
  harness + VN pilot (85/100/83.3). All 3 corpora redeployed. First agent-workflow baseline recorded.
- **2026-07-17** — **v0.4.0 + v0.5.0 shipped.** Singapore (kaya) + Thailand (tomyum) deployed as
  4th + 5th jurisdictions. 8 new source packages (SSO, MAS, PDPC, CSA for SG; OCS, BOT, ETDA, SEC
  for TH). sg-act parser (em-dash sections, MAS paragraph numbering, case-sensitive Schedule gate).
  th-act parser (มาตรา/หมวด/ส่วนที่ for Acts, ข้อ for BOT). TCC Thai word segmenter (pure Go, ~25
  regex rules). Vision OCR for 1,204 scanned BOT docs. Lexindex jurisdiction-aware normalizer.
  VN citation dedup (2,601→72 duplicates, recall 82.9→86.6%). VN+MY code review (11 items fixed,
  deployed). SG: 292 docs / 27,951 chunks / recall 84.8%. TH: 1,551 docs / 29,736 chunks /
  recall 80.7%. All 5 live: banhmi/laksa/rendang/kaya/tomyum.danny.vn. Total ~298K chunks.
- **2026-07-18** — **Cambodia (amok) deployed** as 6th jurisdiction (NBC via residential SOCKS5 proxy,
  282 docs / 2,609 chunks). ID corpus rebuilt with normalizer consolidation + cross-source dedup.
- **2026-07-19** — **v0.3.2 final deploys + v0.3.3 tagged.** All six corpora redeployed as
  `v0.3.2-20260719` (doc_cap=3, dedup, seeds, MY rename, KH scope). Scan-layer gate + P2SK rebuild
  (ID 79.8%). Sparse-arm abbreviation expansion. Eval floors raised to accepted baselines.
  Reranker experiment concluded — NOT deploying. Full-diff code review (4 agents, no critical/major
  findings); minors fixed and tagged `v0.3.3`.
- **2026-07-20** — **KH corpus rebuilt + deployed `v0.4.3-20260720`** (31 → 244 indexed docs,
  2,609 → 7,757 chunks; recall 93.1 → 94.4% on a 36-case scored pool, 8 former known-gap cases now
  answering). Root cause of the thin corpus was a fetch bug (nbc/cdc planned listing pages as the
  document PDF), not missing OCR. NBC re-crawled via /english/ pages only (+TCRMG 2026, TRM 2019,
  banking codes). 40 Khmer-only scans quarantined as explicit gaps. Eval `alt_doc_numbers` added.
  Per-jurisdiction storage layout restored; all six S3 data mirrors synced (sg/kh buckets created).

## Decisions (settled)

| Decision | Choice |
|----------|--------|
| Evidence-only; no answer LLM | citations/validity/relations/gaps over MCP; user brings the model |
| One language per country | index/serve/search the binding native language only; never translate |
| Deploy shape | local pipeline -> Kaggle T4 embed -> dump/restore RDS <- ECS ARM64 <- CloudFront |
| Hybrid retrieval | dense + pgvector `sparsevec` BM25 + RRF; no ParadeDB |
| Qwen3-Embedding-0.6B FP16 | 1024 dims, 32K context, ONNX FP16 everywhere; ~2.3 GB/process |
| go-fitz + Vision OCR | zero-Python extraction; `images:annotate` page-per-request, file-first cache (local + S3) |
| Kaggle-only embedding | free T4 GPU, fresh per run, dataset I/O |
| No composite PKs | surrogate identity + UNIQUE business keys |
| No query-time reranker | measured 2026-07-19: promotes superseded law, 12–38 s/query on CPU; revisit only with new triggers (bigger model, GPU quota, grown corpora) |
