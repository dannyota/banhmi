# Jurisdiction playbook — how banhmi adds a country

The shared model for every jurisdiction, generalized from the Malaysia build (the first expansion,
[`MALAYSIA.md`](MALAYSIA.md)). Country docs state only what differs; this doc is the single source for
what is common. Registry of countries: [`README.md`](README.md).

## The model

- **One codebase, one corpus per country.** A jurisdiction is a **config dimension**
  (`BANHMI_JURISDICTION`, default `vn`) — never a branch or fork. Every core fix (extract/RAG/MCP)
  lands once and serves all countries.
- **The Postgres database is the jurisdiction boundary.** Each country gets its **own database**
  (same instance until load says otherwise) — full isolation, no `jurisdiction` column in data tables,
  zero migration risk to live corpora.
- **One image, N deployments.** The same worker/MCP image serves every country; env selects the
  jurisdiction and DB. Per country: one Cloud Run service (scale-to-zero) + one domain
  (`<codename>.danny.vn/mcp`); v0.3.0 migrates to AWS CloudFront + ECS.
- **Same topical scope everywhere:** banking **digital/technology** regulation (IT & system risk,
  cybersecurity, data protection, cloud & outsourcing, e-transactions/e-signature, digital
  banking/payments, e-KYC, technology operations) — in that country's jurisdiction.

```text
                       one codebase (master)
                              │
     ┌───────────┬────────────┼────────────┬───────────┐
  VN config   MY config   ID config    TH config   SG config
  own sources own sources  (live)     (proposed)  (proposed)
  own scope   own scope
  own citation model per country
     │           │            │            │           │
  banhmi DB   laksa DB      … one Postgres DB per country …
  CloudRun →  CloudRun →     … one Cloud Run + domain per country …
 banhmi.danny.vn  laksa.danny.vn
     └── shared core: pipeline · extract · Qwen3-Embedding · pgvector · MCP ──┘
```

## Language policy (one main language per country)

- Each corpus is in its country's **single main legal language** — VN Vietnamese · MY English ·
  ID Indonesian · TH Thai · SG English. banhmi **indexes, serves, and searches in that language only**.
- The native text is the **binding ground truth**; banhmi **never translates** legal text (translation
  risks legal error and is not authoritative). Translating results is the **user's own responsibility**.
- Official-but-non-binding translations a source publishes (e.g. Krisdika EN, OJK EN) are **never
  indexed**. No multilingual or translated index, ever.

## Share common · customize per country

| Layer | Common (shared, unchanged) | Customized per jurisdiction |
|---|---|---|
| Sources | `ingest.Source` interface; `cmd/pipeline` discover/fetch/drain | the source **set** (`pkg/ingest/<source>/` packages, wired per jurisdiction in `pkg/app`) |
| Structure parse | chunk-walker; go-fitz/OCR mechanics | the **parser** (VN Markdown tree · MY PDF Section tree · new per country) — all emit the same `[]Section` |
| Citation | `gold.chunk` storage; retrieval mechanics | provision **levels + native labels** (Điều/Khoản · Section/Subsection · Pasal/ayat · มาตรา/วรรค) |
| Scope | matcher framework (`pkg/scope`) | scope vocabulary seed (`deploy/seed/scope_term*.csv`) + the central-bank issuer signal |
| Language plumbing | extract gates, OCR batch | content-gate language checks; OCR languages; lexical tokenizer profile (see Thailand) |
| Retrieval | hybrid dense+BM25, RRF, current-law filter | query-router profile; per-jurisdiction eval golden set (`deploy/eval/golden*.json`) |
| MCP | transport; the 5 tools; evidence assembly | compiled **brief** (identity/instructions/guide/tool text) + reply language |
| Deploy | one image; env-driven DB/embedder | `BANHMI_JURISDICTION` + `BANHMI_DATABASE_NAME`; own DB, Cloud Run service, domain |

## Live-jurisdiction safety invariants

1. **Protect every LIVE jurisdiction** (today: VN, MY, ID). Before changing shared code, check who uses it;
   guard with the per-jurisdiction golden-citation regression tests.
2. `gold.chunk.citation` bytes of a live corpus stay **byte-identical** — no re-chunk/re-embed without
   explicit sign-off.
3. **Default every jurisdiction switch to `vn`**; the VN brief/guide/labels stay the **compiled
   fallback** so a missing config row or absent env can never change what a deployment advertises.
4. New-country DDL must be additive/relaxing only (the MY precedent: one silver CHECK relaxed; gold
   untouched).

## Seam registry — SHIPPED

`pkg/base/jurisdiction` is the single registry of per-country descriptors. Adding a country means one
`Descriptor` entry (plus its irreducible new code: source packages in `pkg/app`, a structure parser if
none is reusable, an MCP brief in `pkg/mcp` — each guarded by a registry-coverage test). `vn` is the
compiled fallback; unknown/absent codes never change what a deployment advertises.

Descriptor fields (see the `Descriptor` struct for doc):

| Field | VN | MY | ID | Purpose |
|---|---|---|---|---|
| `Code` | `vn` | `my` | `id` | ISO 3166-1 alpha-2, lower case |
| `DBName` | `banhmi` | `laksa` | `rendang` | default database (env always wins) |
| `OCRLanguages` | *(empty → config default `vi`)* | `en` | `id` | OCR language list |
| `DiacriticDensityGate` | true | false | false | VN-specific content gate |
| `ParagraphLabel` | `Đoạn` | `Paragraph` | `Alinea` | chunk-split citation label |
| `StructureParser` | `vn-markdown` | `my-act` | `id-uu` | keyed parser in pkg/pipeline |
| `UnknownValidityInForce` | false | true | true | curated → default in_force |
| `LexicalRouterBoost` | true | false | false | VN diacritic/sốKH router only |
| `ScopeSeedFile` | `scope_term.csv` | `scope_term_my.csv` | `scope_term_id.csv` | deploy/seed/ CSV |
| `GoldenFile` | `deploy/eval/golden.json` | `deploy/eval/golden_my.json` | `deploy/eval/golden_id.json` | eval golden set |

Consumption sites that used to compare jurisdiction strings now resolve through the descriptor.
`TestSourceBuildersCoverRegistry` and `TestAllComplete` guard drift.

## Phase template (per country)

Mirrors the proven MY build. Do not skip 0 or 1; "candidate" sources are not buildable.

1. **Verify sources live (spike).** Prove each source's fetch contract from a plain client (discovery
   listing, per-doc metadata, file download, validity/relations signal, robots/ToS + bot protection).
   The doc's source list flips candidate → verified only with this evidence (the MY bar:
   "verified live 2026-06-21").
2. **Spike the structure parser** on one real flagship law → exact section inventory, 0 gaps/dupes.
3. **Seam config:** jurisdiction registry entry; scope vocabulary seed (native language); OCR
   languages; content-gate profile.
4. **Sources:** one self-contained `pkg/ingest/<source>/` package each; discovery scope-filtered by
   the config vocabulary (never hardcoded doc ids).
5. **Extract → Normalize:** parser wired by jurisdiction; validity mapped via `config.validity_status`;
   relations from the richest source.
6. **Index + serve:** chunker walks the country's provision levels with native labels; MCP brief;
   per-jurisdiction golden set gating eval.
7. **Deploy:** create the DB on the shared instance → `migrate` + `seed` → run the pipeline
   (`BANHMI_JURISDICTION=<cc>`) → bulk embed (Kaggle) → new Cloud Run service (same image digest,
   env: jurisdiction + DB) → domain → validate over live MCP (the Haiku stand-in
   agent pattern) before announcing. v0.3.0 migrates to AWS CloudFront + ECS.

## Deploy fan-out mechanics

- **DB:** one RDS instance hosts all country DBs until it contends (watch RAM/connections —
  `db.t4g.micro` is already tight with VN + MY; size up or split before loading #3).
- **Cloud Run (current prod):** one service per country, scale-to-zero, `--max-instances` guard; ~$0 idle each.
  v0.3.0 migrates to AWS CloudFront + ECS.
- **Worker:** local, one jurisdiction per run (`BANHMI_JURISDICTION=<cc>` + that country's DB); bulk
  embedding offloads to Kaggle GPU.
- **Env per deployment:** `BANHMI_JURISDICTION`, `BANHMI_DATABASE_NAME` (+ shared DB host/creds,
  `BANHMI_EMBED_QUERY=onnx`). See [`DEPLOYMENT.md`](../../DEPLOYMENT.md).
