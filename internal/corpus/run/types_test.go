package run

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestShardValidateRejectsEveryNonPositiveBudget(t *testing.T) {
	valid := testFencedShard()
	tests := []struct {
		name   string
		mutate func(*Budget)
	}{
		{name: "items", mutate: func(b *Budget) { b.MaxItems = 0 }},
		{name: "bytes", mutate: func(b *Budget) { b.MaxInputBytes = 0 }},
		{name: "pages", mutate: func(b *Budget) { b.MaxPages = 0 }},
		{name: "elapsed", mutate: func(b *Budget) { b.MaxElapsed = 0 }},
		{name: "concurrency", mutate: func(b *Budget) { b.MaxConcurrent = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shard := valid
			tt.mutate(&shard.Budget)
			if err := shard.Validate(); err == nil || !strings.Contains(err.Error(), "must be positive") {
				t.Fatalf("Validate() error = %v, want positive-budget error", err)
			}
		})
	}
}

func TestResultValidateRequiresAccountedInputForComplete(t *testing.T) {
	result := testResult()
	result.Complete = true
	result.Counts = Counts{Expected: 3, Processed: 2, Succeeded: 2}
	if err := result.Validate(); err == nil || !strings.Contains(err.Error(), "all expected input") {
		t.Fatalf("Validate() error = %v, want incomplete-accounting error", err)
	}
}

func TestResultValidateRequiresRecordedAcceptanceForTerminalGaps(t *testing.T) {
	result := testResult()
	result.Complete = true
	result.Counts = Counts{
		Expected: 2, Processed: 2, Succeeded: 1, TerminalGap: 1, AcceptedGaps: 1,
	}
	if err := result.Validate(); err == nil || !strings.Contains(err.Error(), "acceptance reference") {
		t.Fatalf("Validate() error = %v, want acceptance-reference error", err)
	}

	result.GapAcceptanceRef = "coverage-policy/v3/acceptances/41"
	if err := result.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want accepted terminal gap", err)
	}
}

func TestResultValidateRejectsUnacceptedTerminalGapAndOutstandingOutcome(t *testing.T) {
	tests := []struct {
		name   string
		counts Counts
	}{
		{
			name: "terminal gap",
			counts: Counts{
				Expected: 2, Processed: 2, Succeeded: 1, TerminalGap: 1,
			},
		},
		{
			name: "pending retry",
			counts: Counts{
				Expected: 2, Processed: 2, Succeeded: 1, PendingRetry: 1,
			},
		},
		{
			name: "rejected",
			counts: Counts{
				Expected: 2, Processed: 2, Succeeded: 1, Rejected: 1,
			},
		},
		{
			name: "failed",
			counts: Counts{
				Expected: 2, Processed: 2, Succeeded: 1, Failed: 1,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := testResult()
			result.Complete = true
			result.Counts = tt.counts
			if err := result.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want incomplete result")
			}
		})
	}
}

func TestIdempotencyKeyUsesCanonicalImmutableInputs(t *testing.T) {
	shard := testShard()
	want := IdempotencyKey(shard)

	changedOwner := shard
	changedOwner.Execution.Owner = "recovery-worker"
	changedOwner.Execution.LeaseFence++
	if got := IdempotencyKey(changedOwner); got != want {
		t.Fatalf("owner and fence changed key from %q to %q", want, got)
	}

	changedRecipe := shard
	changedRecipe.Recipe = shard.Recipe + ":new"
	if got := IdempotencyKey(changedRecipe); got == want {
		t.Fatalf("recipe change retained key %q", got)
	}

	ambiguousA := shard
	ambiguousA.Cursor = "ab"
	ambiguousA.ManifestRef = "c"
	ambiguousB := shard
	ambiguousB.Cursor = "a"
	ambiguousB.ManifestRef = "bc"
	if IdempotencyKey(ambiguousA) == IdempotencyKey(ambiguousB) {
		t.Fatal("length-distinct canonical inputs produced the same key")
	}
}

func TestApplyRequestRejectsKeyOrOutputPayloadMismatch(t *testing.T) {
	result := testResult()
	request := applyRequest(result)

	request.IdempotencyKey = "sha256:wrong"
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "immutable shard inputs") {
		t.Fatalf("key mismatch error = %v, want immutable-input error", err)
	}

	request = applyRequest(result)
	request.Outputs[0].Digest = "sha256:different"
	if err := request.Validate(); err == nil || !strings.Contains(err.Error(), "does not match result output") {
		t.Fatalf("output mismatch error = %v, want payload-mismatch error", err)
	}
}

func TestSameImmutablePayloadIgnoresSchedulerIdentityAndTimeRepresentation(t *testing.T) {
	instant := time.Date(2026, time.October, 11, 9, 30, 0, 0, time.UTC)
	leftResult := testResult()
	leftResult.Counts = Counts{Expected: 1, Processed: 1, PendingRetry: 1}
	leftResult.NextEligible = &instant
	left := applyRequest(leftResult)

	right := left
	right.Result.Shard.Execution.Owner = "recovery-worker"
	right.Result.Shard.Execution.LeaseFence = 8
	sameInstant := instant.In(time.FixedZone("ICT", 7*60*60))
	right.Result.NextEligible = &sameInstant
	if !SameImmutablePayload(left, right) {
		t.Fatal("same instant and immutable payload reported different")
	}

	differentInstant := sameInstant.Add(time.Second)
	right.Result.NextEligible = &differentInstant
	if SameImmutablePayload(left, right) {
		t.Fatal("different next-eligible instant reported equal")
	}
}

func TestCountsValidateRejectsOverflowingOutcomeSum(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	counts := Counts{
		Expected: maxInt, Processed: maxInt, Succeeded: maxInt, Failed: maxInt,
	}
	if err := counts.Validate(); err == nil || !strings.Contains(err.Error(), "exceed processed") {
		t.Fatalf("Validate() error = %v, want overflow-safe accounting error", err)
	}
}

func TestResultValidateRejectsCountBeyondItemBudget(t *testing.T) {
	result := testResult()
	result.Counts = Counts{Expected: 26, Processed: 26, Succeeded: 26}
	if err := result.Validate(); err == nil || !strings.Contains(err.Error(), "exceeds item budget") {
		t.Fatalf("Validate() error = %v, want item-budget error", err)
	}
}

func TestResultValidateRejectsOneReferenceWithDifferentDigests(t *testing.T) {
	result := testResult()
	result.Outputs = append(result.Outputs, OutputRef{
		Ref: "blob://sha256/output", Digest: "sha256:different", SizeBytes: 10,
	})
	if err := result.Validate(); err == nil || !strings.Contains(err.Error(), "duplicates immutable reference") {
		t.Fatalf("Validate() error = %v, want duplicate-reference error", err)
	}
}

func testShard() Shard {
	return Shard{
		Execution: Execution{
			Country: "vn",
			Owner: "direct-worker-1",
			RunID: uuid.MustParse("10000000-0000-0000-0000-000000000001"),
			BuildID: uuid.MustParse("20000000-0000-0000-0000-000000000002"),
		},
		Stage: Discover,
		Cursor: "source=vbpl;after=100",
		ManifestRef: "sha256:manifest",
		Recipe: "discover/v2",
		Budget: Budget{
			MaxItems: 25,
			MaxInputBytes: 1 << 20,
			MaxPages: 5,
			MaxElapsed: 30 * time.Second,
			MaxConcurrent: 2,
		},
	}
}

func testResult() Result {
	shard := testFencedShard()
	return Result{
		Shard: shard,
		NextCursor: "source=vbpl;after=125",
		Counts: Counts{Expected: 1, Processed: 1, Succeeded: 1},
		Outputs: []OutputRef{{Ref: "blob://sha256/output", Digest: "sha256:output", SizeBytes: 10}},
	}
}

func testFencedShard() Shard {
	shard := testShard()
	shard.Execution.LeaseFence = 7
	return shard
}
