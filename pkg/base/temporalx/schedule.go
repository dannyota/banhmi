package temporalx

import (
	"context"
	"fmt"

	"go.temporal.io/sdk/client"
)

// EnsureSchedule creates the schedule if it does not already exist, returning
// whether it was created. It never overwrites an existing schedule, so an
// operator's pause/resume state and edits survive worker restarts. Schedules are
// expected to be created with ScheduleOptions.Paused = true so a fresh deployment
// does not crawl government sites before an operator opts in.
func EnsureSchedule(ctx context.Context, c client.Client, opts client.ScheduleOptions) (created bool, err error) {
	if _, err = c.ScheduleClient().Create(ctx, opts); err == nil {
		return true, nil
	}
	// Create failed: if the schedule already exists, that is success (a prior run
	// or a concurrent worker created it). Otherwise surface the create error.
	if _, derr := c.ScheduleClient().GetHandle(ctx, opts.ID).Describe(ctx); derr == nil {
		return false, nil
	}
	return false, fmt.Errorf("ensure schedule %s: %w", opts.ID, err)
}
