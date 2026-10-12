# Chunking helper layout

Status: Source relocation is coded and independently reviewed; hosted evidence remains pending. The owner-approved
[repository layout](../design/repository-layout.md) assigns pure chunk computation to
`internal/corpus/chunking`. This bounded move preserves existing behavior and does not accept
Vietnam M3 or introduce a chunking strategy. The [Vietnam plan](../plans/vietnam.md) owns M0-M6.

The [implementation plan](../plans/chunking-layout.md) owns tasks and evidence gates.
[Repository instructions](../../AGENTS.md), [engineering](../../instructions/engineering.md),
[corpus integrity](../../instructions/corpus.md), and
[verification](../../instructions/verification.md) govern this slice.

## Package contract

Create `internal/corpus/chunking/split.go` with only standard-library dependencies and these APIs:

```go
func RoughTokenCount(s string) int
func SplitLongContent(content string, maxTokens int) []string
```

Move the existing bodies of `roughTokenCount`, `splitLongChunkContent`, and
`splitLongTextByWords` from `pkg/pipeline/index_activities.go`. Rename the two public entry points
and their internal references. Keep `splitLongTextByWords` private in the new package. It has no
caller outside the existing splitter, so it needs no compatibility API.

Retain `roughTokenCount` and `splitLongChunkContent` in `pkg/pipeline/index_activities.go` as
private forwarding wrappers. Existing pipeline callers and tests continue using those names.
Keep `maxDieuTokens = 512` in the pipeline and pass the caller's cap through unchanged.

## Preserved behavior

- Counting uses `utf8.RuneCountInString`, returns zero for empty input, and otherwise returns
  `max(1, runeCount/2)` with integer division. It is an estimate, not a tokenizer.
- Splitting trims outer whitespace and returns `nil` for empty or whitespace-only input.
- A nonpositive cap or an estimate at or below the cap returns the trimmed content as one part.
  This fast path preserves internal whitespace and newlines.
- Above the cap, the splitter trims lines, skips empty lines, and packs lines in order with
  newline separators. A line above the cap flushes the pending part and splits by words.
- Word splitting uses `strings.Fields`, packs whole words in order with single spaces, and never
  cuts a word. A single word above the cap remains oversized. No universal token ceiling is claimed.
- Results remain deterministic. The functions return no errors and add no validation, fallback,
  default cap, language policy, or provider dependency.

Keep prefixes, citation bytes, section-tree handling, chunk IDs, minimum-content floors, database
writes, embeddings, tokenizer selection, jurisdiction rules, and all other pipeline behavior at their
current paths. No corpus reindex, re-embedding, deployment, cloud change, or source read is authorized.

## Acceptance evidence

Synthetic tests beside the new package pin exact results for rune counting, fast paths, line packing,
word packing, and oversized words. Existing pipeline tests remain and exercise the wrappers.
Unicode cases include Vietnamese, Thai, Khmer, and emoji; assertions count runes rather than bytes.
Ceiling assertions use inputs whose individual words fit the cap and separately preserve the
oversized-word exception.

Static inspection must show a standard-library-only new package, two forwarding wrappers, and no
change to surrounding indexing policy or mutation code. GitHub checks formatting, builds, vet,
short tests, and repository hygiene on the exact candidate commit. Independent read-only review
and adversarial corpus-integrity review precede acceptance. Missing or skipped evidence remains a gate.

All six jurisdictions share the helpers. Hosted corpus evaluation for Vietnam, Malaysia, Singapore,
Indonesia, Thailand, and Cambodia remains required under the corpus rules. Synthetic helper tests
prove coded behavior only; they do not prove real-document parity, retrieval parity, or deployment.
