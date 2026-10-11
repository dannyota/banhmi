package run

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Lease is the scheduler's current build-writer grant.
type Lease struct {
	Country   string
	BuildID   uuid.UUID
	Owner     string
	Fence     int64
	ExpiresAt time.Time
}

// Validate rejects a lease that cannot fence one owner and build.
func (l Lease) Validate() error {
	switch {
	case l.Country == "":
		return fmt.Errorf("country is required")
	case l.BuildID == uuid.Nil:
		return fmt.Errorf("build ID is required")
	case l.Owner == "":
		return fmt.Errorf("owner is required")
	case l.Fence <= 0:
		return fmt.Errorf("fence must be positive")
	case l.ExpiresAt.IsZero():
		return fmt.Errorf("expiry is required")
	default:
		return nil
	}
}

// SchedulerLease serializes scheduler ownership for one country build.
type SchedulerLease interface {
	Acquire(context.Context, string, uuid.UUID, string, time.Time) (Lease, error)
	Renew(context.Context, Lease, time.Time) (Lease, error)
	Release(context.Context, Lease) error
}

func validateLeaseOwner(lease Lease, execution Execution, now time.Time) error {
	if err := lease.Validate(); err != nil {
		return fmt.Errorf("invalid lease: %w", err)
	}
	switch {
	case lease.Country != execution.Country:
		return fmt.Errorf("country %q does not match execution country %q", lease.Country, execution.Country)
	case lease.BuildID != execution.BuildID:
		return fmt.Errorf("build ID %s does not match execution build ID %s", lease.BuildID, execution.BuildID)
	case lease.Owner != execution.Owner:
		return fmt.Errorf("owner %q does not match execution owner %q", lease.Owner, execution.Owner)
	case !lease.ExpiresAt.After(now):
		return fmt.Errorf("lease expired at %s", lease.ExpiresAt)
	default:
		return nil
	}
}

func validateLeaseIdentity(lease Lease, execution Execution, now time.Time) error {
	if err := validateLeaseOwner(lease, execution, now); err != nil {
		return err
	}
	if lease.Fence != execution.LeaseFence {
		return fmt.Errorf("fence %d does not match execution fence %d", lease.Fence, execution.LeaseFence)
	}
	return nil
}
