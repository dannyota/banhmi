package run

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"time"

	"github.com/google/uuid"
)

// Stage identifies one durable corpus-processing stage.
type Stage string

const (
	Discover  Stage = "discover"
	Fetch     Stage = "fetch"
	Extract   Stage = "extract"
	OCR       Stage = "ocr"
	Normalize Stage = "normalize"
	Index     Stage = "index"
	Dense     Stage = "dense"
	Lexical   Stage = "lexical"
	Evaluate  Stage = "evaluate"
	Publish   Stage = "publish"
	Activate  Stage = "activate"
)

// Valid reports whether stage belongs to the fixed corpus stage vocabulary.
func (s Stage) Valid() bool {
	switch s {
	case Discover, Fetch, Extract, OCR, Normalize, Index, Dense, Lexical, Evaluate, Publish, Activate:
		return true
	default:
		return false
	}
}

// Budget bounds every resource dimension a shard controls directly.
type Budget struct {
	MaxItems      int
	MaxInputBytes int64
	MaxPages      int
	MaxElapsed    time.Duration
	MaxConcurrent int
}

// Validate rejects an absent or unbounded resource dimension.
func (b Budget) Validate() error {
	switch {
	case b.MaxItems <= 0:
		return fmt.Errorf("max items must be positive")
	case b.MaxInputBytes <= 0:
		return fmt.Errorf("max input bytes must be positive")
	case b.MaxPages <= 0:
		return fmt.Errorf("max pages must be positive")
	case b.MaxElapsed <= 0:
		return fmt.Errorf("max elapsed must be positive")
	case b.MaxConcurrent <= 0:
		return fmt.Errorf("max concurrent must be positive")
	default:
		return nil
	}
}

// Execution is the identity and current writer fence for one build attempt.
type Execution struct {
	Country    string
	Owner      string
	RunID      uuid.UUID
	BuildID    uuid.UUID
	LeaseFence int64
}

// Validate rejects an execution that cannot be fenced to one build owner.
func (e Execution) Validate() error {
	if err := e.validateIdentity(); err != nil {
		return err
	}
	if e.LeaseFence <= 0 {
		return fmt.Errorf("lease fence must be positive")
	}
	return nil
}

func (e Execution) validateIdentity() error {
	switch {
	case e.Country == "":
		return fmt.Errorf("country is required")
	case e.Owner == "":
		return fmt.Errorf("owner is required")
	case e.RunID == uuid.Nil:
		return fmt.Errorf("run ID is required")
	case e.BuildID == uuid.Nil:
		return fmt.Errorf("build ID is required")
	default:
		return nil
	}
}

// Shard describes one bounded and retry-safe invocation of a stage.
type Shard struct {
	Execution   Execution
	Stage       Stage
	Cursor      string
	ManifestRef string
	Recipe      string
	Budget      Budget
}

// Validate checks the identity, stage, recipe, and resource bounds of a shard.
func (s Shard) Validate() error {
	if err := s.Execution.Validate(); err != nil {
		return fmt.Errorf("execution: %w", err)
	}
	if !s.Stage.Valid() {
		return fmt.Errorf("unknown stage %q", s.Stage)
	}
	if s.Recipe == "" {
		return fmt.Errorf("recipe is required")
	}
	if err := s.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	return nil
}

// Counts accounts for the bounded input presented to one shard invocation.
// Processed equals the sum of succeeded, pending-retry, terminal-gap,
// rejected, and failed items. Expected is the closed input count for the
// invocation. AcceptedGaps is a policy decision and is not counted twice.
type Counts struct {
	Expected     int
	Processed    int
	Succeeded    int
	PendingRetry int
	TerminalGap  int
	AcceptedGaps int
	Rejected     int
	Failed       int
}

// Validate checks that every processed item has exactly one outcome.
func (c Counts) Validate() error {
	values := []struct {
		name  string
		value int
	}{
		{name: "expected", value: c.Expected},
		{name: "processed", value: c.Processed},
		{name: "succeeded", value: c.Succeeded},
		{name: "pending retry", value: c.PendingRetry},
		{name: "terminal gap", value: c.TerminalGap},
		{name: "accepted gaps", value: c.AcceptedGaps},
		{name: "rejected", value: c.Rejected},
		{name: "failed", value: c.Failed},
	}
	for _, value := range values {
		if value.value < 0 {
			return fmt.Errorf("%s count must not be negative", value.name)
		}
	}
	if c.Processed > c.Expected {
		return fmt.Errorf("processed count %d exceeds expected count %d", c.Processed, c.Expected)
	}
	remaining := c.Processed
	for _, outcome := range []int{c.Succeeded, c.PendingRetry, c.TerminalGap, c.Rejected, c.Failed} {
		if outcome > remaining {
			return fmt.Errorf("outcome counts exceed processed count %d", c.Processed)
		}
		remaining -= outcome
	}
	if remaining != 0 {
		return fmt.Errorf("processed count %d has %d unaccounted items", c.Processed, remaining)
	}
	if c.AcceptedGaps > c.TerminalGap {
		return fmt.Errorf("accepted gap count %d exceeds terminal gap count %d", c.AcceptedGaps, c.TerminalGap)
	}
	return nil
}

// OutputRef identifies immutable, content-addressed stage output.
type OutputRef struct {
	Ref       string
	Digest    string
	SizeBytes int64
	MediaType string
}

// Validate rejects output references that do not identify immutable content.
func (o OutputRef) Validate() error {
	switch {
	case o.Ref == "":
		return fmt.Errorf("reference is required")
	case o.Digest == "":
		return fmt.Errorf("digest is required")
	case o.SizeBytes < 0:
		return fmt.Errorf("size bytes must not be negative")
	default:
		return nil
	}
}

// Result describes the explicit outcome and immutable output references of a
// shard. GapAcceptanceRef names a recorded policy acceptance, never a raw
// source assertion.
type Result struct {
	Shard            Shard
	NextCursor       string
	NextEligible     *time.Time
	Counts           Counts
	GapAcceptanceRef string
	Complete         bool
	Outputs          []OutputRef
}

// Validate checks accounting, completion, gap acceptance, and output refs.
func (r Result) Validate() error {
	if err := r.Shard.Validate(); err != nil {
		return fmt.Errorf("shard: %w", err)
	}
	if err := r.Counts.Validate(); err != nil {
		return fmt.Errorf("counts: %w", err)
	}
	if r.Counts.Expected > r.Shard.Budget.MaxItems {
		return fmt.Errorf("expected count %d exceeds item budget %d", r.Counts.Expected, r.Shard.Budget.MaxItems)
	}
	if r.Counts.Processed > r.Shard.Budget.MaxItems {
		return fmt.Errorf("processed count %d exceeds item budget %d", r.Counts.Processed, r.Shard.Budget.MaxItems)
	}
	if r.Counts.AcceptedGaps > 0 && r.GapAcceptanceRef == "" {
		return fmt.Errorf("gap acceptance reference is required for accepted gaps")
	}
	if r.Counts.AcceptedGaps == 0 && r.GapAcceptanceRef != "" {
		return fmt.Errorf("gap acceptance reference requires accepted gaps")
	}
	if r.Counts.PendingRetry > 0 && r.NextEligible == nil {
		return fmt.Errorf("pending retry requires a next eligible time")
	}
	if r.Counts.PendingRetry == 0 && r.NextEligible != nil {
		return fmt.Errorf("next eligible time requires pending retry input")
	}
	if r.Complete {
		if r.Counts.Processed != r.Counts.Expected {
			return fmt.Errorf("complete result must account for all expected input")
		}
		if r.Counts.PendingRetry != 0 || r.Counts.Rejected != 0 || r.Counts.Failed != 0 {
			return fmt.Errorf("complete result cannot contain pending retry, rejected, or failed input")
		}
		if r.Counts.TerminalGap != r.Counts.AcceptedGaps {
			return fmt.Errorf("complete result must accept every terminal gap")
		}
		if r.Counts.TerminalGap > 0 && r.GapAcceptanceRef == "" {
			return fmt.Errorf("complete result with terminal gaps requires an acceptance reference")
		}
	}
	seen := make(map[string]struct{}, len(r.Outputs))
	for i, output := range r.Outputs {
		if err := output.Validate(); err != nil {
			return fmt.Errorf("output %d: %w", i, err)
		}
		if _, ok := seen[output.Ref]; ok {
			return fmt.Errorf("output %d duplicates immutable reference", i)
		}
		seen[output.Ref] = struct{}{}
	}
	return nil
}

// Executor performs one bounded shard. It must stop promptly when ctx is
// canceled and must not accept durable outputs outside UnitOfWork.Apply.
type Executor interface {
	Execute(context.Context, Shard) (Result, error)
}

// IdempotencyKey derives a stable key from immutable processing inputs. Owner
// and lease fence are mutable scheduler state and are excluded. UnitOfWork
// separately rejects a duplicate key whose stored payload differs.
func IdempotencyKey(shard Shard) string {
	digest := sha256.New()
	writeCanonicalString(digest, "corpus-stage-result-v1")
	writeCanonicalString(digest, shard.Execution.Country)
	writeCanonicalString(digest, shard.Execution.RunID.String())
	writeCanonicalString(digest, shard.Execution.BuildID.String())
	writeCanonicalString(digest, string(shard.Stage))
	writeCanonicalString(digest, shard.Cursor)
	writeCanonicalString(digest, shard.ManifestRef)
	writeCanonicalString(digest, shard.Recipe)
	writeCanonicalUint64(digest, uint64(shard.Budget.MaxItems))
	writeCanonicalUint64(digest, uint64(shard.Budget.MaxInputBytes))
	writeCanonicalUint64(digest, uint64(shard.Budget.MaxPages))
	writeCanonicalUint64(digest, uint64(shard.Budget.MaxElapsed))
	writeCanonicalUint64(digest, uint64(shard.Budget.MaxConcurrent))
	return "sha256:" + hex.EncodeToString(digest.Sum(nil))
}

func writeCanonicalString(dst hash.Hash, value string) {
	writeCanonicalUint64(dst, uint64(len(value)))
	_, _ = dst.Write([]byte(value))
}

func writeCanonicalUint64(dst hash.Hash, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = dst.Write(encoded[:])
}
