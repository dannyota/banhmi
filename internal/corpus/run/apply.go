package run

import (
	"context"
	"fmt"
	"reflect"
	"time"
)

// ApplyRequest carries immutable outputs and their stage result into one
// atomic commit. Execution identity exists only in Result.Shard.Execution.
type ApplyRequest struct {
	IdempotencyKey string
	Result         Result
	Outputs        []OutputRef
}

// Validate checks the complete transaction request before it reaches an
// adapter. The adapter must still validate country, build, owner, and current
// fence inside its transaction. Idempotency is scoped by country, build,
// stage, and key. An exact duplicate returns its stored result; the same key
// with another payload is an error.
func (r ApplyRequest) Validate() error {
	if r.IdempotencyKey == "" {
		return fmt.Errorf("idempotency key is required")
	}
	if err := r.Result.Validate(); err != nil {
		return fmt.Errorf("result: %w", err)
	}
	if want := IdempotencyKey(r.Result.Shard); r.IdempotencyKey != want {
		return fmt.Errorf("idempotency key does not match immutable shard inputs")
	}
	if len(r.Outputs) != len(r.Result.Outputs) {
		return fmt.Errorf("apply outputs do not match result outputs")
	}
	for i := range r.Outputs {
		if r.Outputs[i] != r.Result.Outputs[i] {
			return fmt.Errorf("apply output %d does not match result output", i)
		}
	}
	return nil
}

// UnitOfWork atomically validates country, build and run association, owner,
// and the current execution fence, then commits immutable outputs and stage
// completion. It also verifies that GapAcceptanceRef names a recorded policy
// acceptance when terminal gaps are accepted. An immutable-payload replay may
// carry a new scheduler owner and fence and returns the committed result. A
// stale fence, invalid acceptance, or repeated key with another immutable
// payload writes nothing.
type UnitOfWork interface {
	Apply(context.Context, ApplyRequest) (Result, error)
}

// SameImmutablePayload reports whether two requests describe the same durable
// stage write. Scheduler owner and fence may change during response-loss
// recovery; every processing input, outcome, and output reference must match.
func SameImmutablePayload(left, right ApplyRequest) bool {
	if !equalOptionalTime(left.Result.NextEligible, right.Result.NextEligible) {
		return false
	}
	left.Result.NextEligible = nil
	right.Result.NextEligible = nil
	left.Result.Shard.Execution.Owner = ""
	left.Result.Shard.Execution.LeaseFence = 0
	right.Result.Shard.Execution.Owner = ""
	right.Result.Shard.Execution.LeaseFence = 0
	return reflect.DeepEqual(left, right)
}

func equalOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func applyRequest(result Result) ApplyRequest {
	outputs := append([]OutputRef(nil), result.Outputs...)
	return ApplyRequest{
		IdempotencyKey: IdempotencyKey(result.Shard),
		Result:         result,
		Outputs:        outputs,
	}
}
