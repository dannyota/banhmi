# Chunking helper layout implementation plan

**Goal:** Move existing pure token estimation and text splitting into the approved corpus package
without changing callers or outputs.

**Architecture:** A standard-library-only package owns the three existing algorithm bodies.
Two private pipeline wrappers preserve old entry points. The pipeline retains the 512-token policy.

**Tech stack:** Go, the existing module and toolchain, standard-library `strings` and `unicode/utf8`.

**Spec:** [Chunking helper layout](../specs/chunking-layout.md).
The [approved layout](../design/repository-layout.md) supplies the destination; the
[Vietnam plan](vietnam.md) retains milestone authority.

## Ownership and constraints

The implementation worker owns only `internal/corpus/chunking/split.go`,
`internal/corpus/chunking/split_test.go`, and the helper bodies and import block in
`pkg/pipeline/index_activities.go`. The worker retains existing pipeline tests without edits.
The primary owns document indexes, shared integration files, Git operations, and hosted evidence.
A separate reviewer owns read-only review and fix confirmation.

Do not move prefixes, citations, section trees, Index mutations, country rules, tokenizer providers,
embeddings, defaults, or floors. Do not change dependencies, CI, generated code, SQL, or infrastructure.
Do not run local builds, tests, formatters, generators, dependency installs, or the app. Do not read
credentials or cloud resources. Workers never commit, push, or deploy.

## Review focus

- Multibyte text keeps the rune-based estimate, including odd lengths and one rune.
- Empty input keeps a nil result; nonpositive caps keep one trimmed part.
- The fast path retains internal tabs, blank lines, and spaces.
- Split paths retain newline grouping and `strings.Fields` word normalization in order.
- One oversized word remains intact even when its estimate exceeds the cap.

## Task 1: Pin the existing contract with synthetic tests

Create `internal/corpus/chunking/split_test.go` using external package `chunking_test` and the
two APIs named in the spec. Keep existing tests in `pkg/pipeline/index_test.go` and
`pkg/pipeline/legalparse_test.go` as wrapper regressions.

- [x] Add `TestRoughTokenCount` cases: `"" => 0`, `"a" => 1`, `"abc" => 1`,
  `"Điều 1" => 3`, `"กขคง" => 2`, `"កខគឃ" => 2`, and `"😀😀😀" => 1`.
- [x] Add `TestSplitLongContent` exact-slice cases: whitespace-only gives nil;
  `"  a\n\n b  "` with caps `0`, `-1`, and `100` gives `[]string{"a\n\n b"}`;
  `"abcd"` with cap `2` gives `[]string{"abcd"}`;
  `"  aa\tbb  cc  "` with cap `100` gives `[]string{"aa\tbb  cc"}`.
- [x] Pin line grouping: `" aa \n\n bb \n cc "` with cap `3` gives
  `[]string{"aa\nbb", "cc"}`; `"aa\nbbbbbb\ncc"` with cap `2` gives
  `[]string{"aa", "bbbbbb", "cc"}`.
- [x] Pin word splitting: `"aa\tbb  cc"` with cap `2` gives
  `[]string{"aa bb", "cc"}`; `"abcdef"` with cap `1` gives `[]string{"abcdef"}`.
- [x] For repeated Vietnamese, Thai, Khmer, and emoji words that each fit cap `8`, assert
  nonempty parts, every estimate at most `8`, unchanged ordered fields, and deterministic output.

All test execution occurs on GitHub. The planned preimplementation failure is the absent new API;
the primary records hosted evidence when a test-first candidate is published. Do not run locally.

## Task 2: Relocate the bodies and keep compatibility

- [x] Copy `splitLongChunkContent` and `splitLongTextByWords` from
  `pkg/pipeline/index_activities.go:950-1017` into `internal/corpus/chunking/split.go`.
  Export the first as `SplitLongContent(content string, maxTokens int) []string`.
- [x] Copy `roughTokenCount` from `pkg/pipeline/index_activities.go:1047-1061` as
  `RoughTokenCount(s string) int`. Change only names and internal calls. Use plain API comments.
- [x] Replace the two old entry points with forwarding bodies. Remove the old private word
  splitter, which has no other caller. Add the chunking import and remove `unicode/utf8` only if
  no remaining use exists in the file. Keep the 512-token constant and all caller arguments.
- [x] Inspect the complete diff and compare old and new bodies. Confirm no unrelated body,
  citation, floor, mutation, or policy changed. Confirm the new package imports only the standard library.

## Task 3: Review and hosted acceptance

- [x] Primary updates owned documentation indexes and current-path facts once code exists.
- [x] Obtain separate read-only implementation review and adversarial corpus-integrity review.
  Return defects to the implementer; require independent fix confirmation.
- [x] Primary publishes only when authorized and reads exact-commit GitHub evidence under
  [verification](../../instructions/verification.md): formatting, repository hygiene,
  `go build ./...`, `go vet ./...`, and `go test -short ./...` must pass.
- [ ] Record exact commit, workflow run, inputs, affected-country coverage, and unavailable checks.
  Obtain isolated hosted corpus evaluations for all six affected jurisdictions. Missing evaluation
  inputs remain a corpus-parity gate; short tests cannot substitute for them.

## Evidence and remaining gates

Source relocation and synthetic tests are coded. Independent plan, implementation, and adversarial
corpus-integrity reviews found no blocking issue. [GitHub run 38219116830](https://github.com/dannyota/banhmi/actions/runs/38219116830)
checked head `102ad7029ae6df90c4eba0f08058114919bea84b`, PR merge
`cff5998083d44ed1056f369bb9729a4ebe46090b`. Generation and formatting, repository hygiene, Go
build/vet/short tests, browser integration, and auth PostgreSQL integration passed. The short suite
reports the new chunking package and legacy pipeline tests passing with synthetic inputs. Security
scanner jobs failed, so the aggregate CI remains red. All six affected-country evaluations remain pending.
No local execution checks ran. No corpus, deployed, or M3 acceptance claim follows from this change.
