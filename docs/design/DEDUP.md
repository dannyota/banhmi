# Cross-source dedup — decision record

## Decision: no dedup stage (2026-07-09)

**Evaluated and dropped.** A full dedup pipeline stage was designed, reviewed by two independent
agents (correctness + simplicity), and rejected. This doc records the decision and the data so the
reasoning is not repeated.

## Problem

Multiple VN sources discover the same document (same số ký hiệu). Without dedup, every source's
version goes through fetch → extract → normalize. Silver merges by `doc_key` (upsert), so the
final result is correct — the redundant work only wastes fetch/extract cycles.

## Why not build it

1. **Tiny overlap.** Only 36 doc groups (2.3%) span multiple sources, ~28 fetch_doc rows would be
   deferred. The savings are ~30 fetches — negligible.
2. **Silver merge already works.** The normalize step upserts `silver.document` by `doc_key`. When
   multiple sources fetch the same doc, the last to normalize wins. Cross-source merge (vbpl
   metadata + congbao text) works correctly without a dedup stage.
3. **Grouping key has data-loss bugs.** VN document numbers can collide:
   - Year-less numbers (`630/QĐ-NHNN`) restart yearly — different documents share type+number.
   - `DocTypeCode` is only set by vbpl; other sources don't provide it, so cross-source groups
     either never form or mis-group (Luật vs Nghị quyết with the same QH number).
   A false collision silently loses a law from the corpus — unacceptable for legal data.
4. **Convergence repairs self-inflicted damage.** The 2-round promotion mechanism exists solely to
   undo deferral when vbpl's `hasContent` flag lied. Without dedup, both sources are fetched and
   silver merge picks the best — the same end state, without the machinery.
5. **Priority suppresses born-digital text.** `vbpl > congbao` defers congbao's gazette DOCX in
   favor of vbpl's HTML transcription — the extraction cascade prefers DOCX, so dedup would make
   the corpus worse.

## What we do instead

1. **Pass `HasContent` into the discovery-time bronze upsert** (one-line fix) so the 13 no-content
   vbpl docs (9 Template.pdf only, 4 nothing) are visible early.
2. **Route Template.pdf-only docs through `content_recheck`** so they surface as quality gaps via
   `quality_gaps` MCP tool.
3. **Let silver merge handle cross-source overlap** — it already works correctly.

## Data (VN, 2026-07-09)

**Cross-source overlap:** 36 doc groups out of ~1,580 (2.3%), 28 deferrable fetch_doc rows.

**vbpl content usability (1,601 docs):**

| Status | Docs | % |
|---|---|---|
| Usable: has HTML body | 1,561 | 97.5% |
| Usable: has real file (no HTML) | 27 | 1.7% |
| Not usable: Template.pdf only | 9 | 0.6% |
| Not usable: nothing | 4 | 0.2% |

## Revisit conditions

Revisit if a future jurisdiction has **heavy source overlap** (>20% of docs discovered by multiple
sources) AND the redundant fetches cause real cost/time problems. The schema proposal
(`doc_group`, `dedup_state` on `fetch_doc`, `ClaimArtifacts` filter) is archived in git history
for reference.
