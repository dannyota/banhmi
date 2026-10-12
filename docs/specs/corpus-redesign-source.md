# Corpus Redesign Source Contracts

These are the existing source-pass contracts for the corpus pipeline redesign in
[the design](../design/corpus-pipeline-redesign.md). The paired [implementation plan](../plans/corpus-redesign-source.md)
owns implementation tasks and evidence. The [Vietnam plan](../plans/vietnam.md) owns acceptance for
the new Vietnam system.

## Fixed contracts

Task 1 owns neutral evidence contracts. Evidence packages do not import corpus or release packages.

```go
package api

type Operations interface {
	Guide(context.Context, GuideInput) (GuideOutput, error)
	CorpusStatus(context.Context, CorpusStatusInput) (CorpusStatusOutput, error)
	QualityGaps(context.Context, QualityGapsInput) (QualityGapsOutput, error)
	Search(context.Context, SearchInput) (SearchOutput, error)
	Document(context.Context, DocumentInput) (DocumentOutput, error)
}

type StartupMetadata struct {
	Country, Coverage, IndexedLanguage, BindingLanguage string
	IssuerVocabulary, DocumentTypeVocabulary            []string
}

type ReleasePin interface {
	ReleaseID() string
	Operations() Operations
	Close() error
}

type ReleaseResolver interface {
	Pin(context.Context, string, string) (ReleasePin, error) // country, requested release
}
```

`SearchOutput` and `DocumentOutput` gain `release_id`. Both inputs gain optional `release_id`.
One request defers `ReleasePin.Close` after search, relation hydration, or document lookup completes.
Closing the final pin permits old pool drain and retention. Unknown or expired releases return an
explicit error and never substitute another country or release.

Pin every corpus-reading operation, including `corpus_status` and `quality_gaps`. Guide may use
startup metadata. Until durable release adapters exist, a compatibility pin uses the real current
pool and operations, returns an empty release ID, and rejects explicit release requests. Closing a
pin is idempotent. This compatibility path does not claim an immutable corpus snapshot.

Task 4 owns one stage vocabulary and the transaction boundary used later by durable adapters.

```go
package run

type Stage string

const (
	Discover Stage = "discover"
	Fetch    Stage = "fetch"
	Extract  Stage = "extract"
	OCR      Stage = "ocr"
	Normalize Stage = "normalize"
	Index    Stage = "index"
	Dense    Stage = "dense"
	Lexical  Stage = "lexical"
	Evaluate Stage = "evaluate"
	Publish  Stage = "publish"
	Activate Stage = "activate"
)

type Budget struct {
	MaxItems int; MaxInputBytes int64; MaxPages int
	MaxElapsed time.Duration; MaxConcurrent int
}

type Execution struct {
	Country, Owner string; RunID, BuildID uuid.UUID; LeaseFence int64
}

type Shard struct {
	Execution Execution; Stage Stage
	Cursor, ManifestRef, Recipe string; Budget Budget
}

type Counts struct {
	Expected, Processed, Succeeded, PendingRetry, TerminalGap, AcceptedGaps, Rejected, Failed int
}

type Result struct {
	Shard Shard; NextCursor string; NextEligible *time.Time
	Counts Counts; GapAcceptanceRef string; Complete bool; Outputs []OutputRef
}

type Executor interface { Execute(context.Context, Shard) (Result, error) }

type ApplyRequest struct {
	IdempotencyKey string; Result Result; Outputs []OutputRef
}

type UnitOfWork interface {
	Apply(context.Context, ApplyRequest) (Result, error)
}

type SchedulerLease interface {
	Acquire(context.Context, string, uuid.UUID, string, time.Time) (Lease, error)
	Renew(context.Context, Lease, time.Time) (Lease, error)
	Release(context.Context, Lease) error
}
```

`UnitOfWork.Apply` must validate country, build, run association, owner, and current fence, then commit
immutable outputs and the stage result in one transaction. Scope idempotency by country, build, stage,
and key. A repeated key with the same payload returns the committed result; a payload mismatch fails.
A stale fence writes nothing. Every budget field is positive. `Complete` requires all bounded input to be
accounted for and no pending retry, rejected input, or failure. When `TerminalGap` is nonzero,
`AcceptedGaps` must equal `TerminalGap` and `GapAcceptanceRef` must identify a valid recorded
coverage-policy acceptance. Task 7's unit of work verifies that record; stage output cannot assert
policy acceptance by itself.
