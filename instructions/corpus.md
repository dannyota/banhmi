# Corpus integrity

## Jurisdictions and language

- Keep one separate corpus and database per jurisdiction on one shared codebase.
- Store, index, and search each jurisdiction's selected source language. Never translate stored legal
  text. Vietnam uses Vietnamese, Malaysia and Singapore use English, Indonesia uses Indonesian,
  Thailand uses Thai, and Cambodia indexes English source publications while Khmer remains binding.
- Preserve jurisdiction-specific sources, provision models, citation labels, parsers, scope rules,
  MCP briefs, OCR languages, and evaluation sets behind shared interfaces.
- Default to Vietnam when the jurisdiction is unspecified. Preserve the current code's handling of an
  invalid explicit jurisdiction unless a reviewed design changes that contract.

## Sources and extraction

- Scope must come from configured vocabulary and verified source behavior. Do not hardcode document
  IDs, one-off known-good shortcuts, or source exceptions to force a result.
- Prefer official DOCX, then official text-bearing HTML, legacy DOC, and born-digital PDF. Use OCR only
  for scans or failed deterministic extraction.
- Keep extraction deterministic. Do not use generated output as the canonical parser or silently repair
  source text with a model.
- Run OCR as a separate batch after the per-file content gate. Never add inline OCR, an OCR sidecar,
  figure extraction, or a repair path without a reviewed design.
- Always store OCR text as non-binding evidence, even when it passes the text gate. Preserve its
  engine, version, confidence, source kind, verification, and review state.
- Crawl official sources politely with bounded concurrency, retries, backoff, stable cursors, content
  hashes, and full discovery and fetch provenance.
- Treat source collections as large. Stream pages and rows instead of accumulating unbounded slices.

## Citations, validity, and relations

- Every retrieval chunk must retain its exact native provision citation and deterministic context.
  Preserve document identity, source URL, validity, relation evidence, and coverage gaps.
- Do not change stored citation bytes or require a live-corpus reindex or re-embed without explicit
  owner approval and a measured migration plan.
- Never present repealed, superseded, expired, or not-yet-effective text as current. Keep non-current
  evidence available with an explicit badge.
- Source metadata is an observation, not ground truth for validity. Check the legal text and the
  repealing or amending provision when source status conflicts with document text.
- `official_structured` and confidence `1` mean that an official source asserted a relation. They do
  not prove the assertion is legally correct. Do not attach a scavenged clause to a structured edge.
- Partial repeal of a provision does not end the whole document. Exclude partial repeals from
  document-level supersession.
- Disambiguate duplicate document numbers by document type and title before changing validity.
- Keep gaps explicit. Some jurisdictions have no relation data, and a missing edge must not become an
  inferred fact.

## Parser and relation traps

- Parse amendment targets from clause grammar. In Vietnamese omnibus clauses, the number after the
  target operator is the target. Numbers after `theo` are recital history. Numbers inside replacement
  text after `như sau:` are quoted content. Substring presence cannot classify those roles.
- Normalize internal whitespace before comparing document numbers. Real text contains forms such as
  `116/ 2025/QH15`.
- Quoted replacement blocks can contain numbered items that look like new provisions. Preserve quote
  state so the parser does not swallow the next real provision or emit false structure.
- Vietnamese National Assembly numbers such as `24/2018/QH14` have no hyphenated suffix. Relation
  matchers must cover that shape, hyphenated ministry numbers, and reject bare dates.
- Source provision trees can omit subdivisions. Preserve the source gap and its provenance instead of
  inventing missing citation structure.
- Re-normalization deletes and recreates section IDs in the current implementation. Gold chunks can
  retain dangling section references. Do not blanket re-normalize a live corpus for relation repair.
- A forced reindex is not parity-safe. It can change chunk boundaries and IDs, orphan embeddings, and
  alter evaluation results. Treat every rebuild as a measured re-baselining event.

## Evaluation

- Run the hosted corpus evaluation for every jurisdiction affected by a corpus, parser, indexing, or
  retrieval change.
- Write golden questions as realistic scenarios in the selected corpus language. Do not use keyword
  dumps, bare document identifiers, stiff test phrasing, or cross-language questions.
- Do not lower evaluation floors or relax goldens to hide a regression. Change accepted metrics,
  floors, or goldens only after a deliberate, evidence-backed measured run.
- Keep retrieval evaluation and workflow evaluation separate. Retrieval measures whether the corpus
  returns the right evidence. Workflow evaluation measures whether an external agent uses that
  evidence correctly.

See [`docs/design/SOURCES.md`](../docs/design/SOURCES.md),
[`docs/design/EXTRACTION.md`](../docs/design/EXTRACTION.md),
[`docs/design/SCHEMA.md`](../docs/design/SCHEMA.md), and
[`docs/design/RAG.md`](../docs/design/RAG.md) for current mechanics and recorded evidence. See
[`docs/design/WORKFLOW-EVAL.md`](../docs/design/WORKFLOW-EVAL.md) for workflow evaluation details.
