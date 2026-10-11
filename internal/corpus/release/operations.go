package release

import (
	"context"

	"danny.vn/banhmi/internal/evidence/api"
)

type guardedOperations struct {
	next  api.Operations
	lease context.Context
}

func (g guardedOperations) Guide(ctx context.Context, input api.GuideInput) (api.GuideOutput, error) {
	bound, cancel := bindLease(ctx, g.lease)
	defer cancel()
	if err := context.Cause(bound); err != nil {
		return api.GuideOutput{}, err
	}
	output, err := g.next.Guide(bound, input)
	if cause := operationCause(bound, g.lease); err == nil && cause != nil {
		return api.GuideOutput{}, cause
	}
	return output, err
}

func (g guardedOperations) CorpusStatus(ctx context.Context, input api.CorpusStatusInput) (api.CorpusStatusOutput, error) {
	bound, cancel := bindLease(ctx, g.lease)
	defer cancel()
	if err := context.Cause(bound); err != nil {
		return api.CorpusStatusOutput{}, err
	}
	output, err := g.next.CorpusStatus(bound, input)
	if cause := operationCause(bound, g.lease); err == nil && cause != nil {
		return api.CorpusStatusOutput{}, cause
	}
	return output, err
}

func (g guardedOperations) QualityGaps(ctx context.Context, input api.QualityGapsInput) (api.QualityGapsOutput, error) {
	bound, cancel := bindLease(ctx, g.lease)
	defer cancel()
	if err := context.Cause(bound); err != nil {
		return api.QualityGapsOutput{}, err
	}
	output, err := g.next.QualityGaps(bound, input)
	if cause := operationCause(bound, g.lease); err == nil && cause != nil {
		return api.QualityGapsOutput{}, cause
	}
	return output, err
}

func (g guardedOperations) Search(ctx context.Context, input api.SearchInput) (api.SearchOutput, error) {
	bound, cancel := bindLease(ctx, g.lease)
	defer cancel()
	if err := context.Cause(bound); err != nil {
		return api.SearchOutput{}, err
	}
	output, err := g.next.Search(bound, input)
	if cause := operationCause(bound, g.lease); err == nil && cause != nil {
		return api.SearchOutput{}, cause
	}
	return output, err
}

func (g guardedOperations) Document(ctx context.Context, input api.DocumentInput) (api.DocumentOutput, error) {
	bound, cancel := bindLease(ctx, g.lease)
	defer cancel()
	if err := context.Cause(bound); err != nil {
		return api.DocumentOutput{}, err
	}
	output, err := g.next.Document(bound, input)
	if cause := operationCause(bound, g.lease); err == nil && cause != nil {
		return api.DocumentOutput{}, cause
	}
	return output, err
}

func leaseCause(lease context.Context) error {
	if lease.Err() == nil {
		return nil
	}
	if cause := context.Cause(lease); cause != nil {
		return cause
	}
	return ErrPinLost
}

func operationCause(bound, lease context.Context) error {
	if cause := context.Cause(bound); cause != nil {
		return cause
	}
	return leaseCause(lease)
}

func bindLease(request, lease context.Context) (context.Context, context.CancelFunc) {
	bound, cancelCause := context.WithCancelCause(request)
	cancelFromLease := func() {
		cause := context.Cause(lease)
		if cause == nil {
			cause = ErrPinLost
		}
		cancelCause(cause)
	}
	if lease.Err() != nil {
		cancelFromLease()
		return bound, func() { cancelCause(context.Canceled) }
	}
	stop := context.AfterFunc(lease, func() {
		cancelFromLease()
	})
	return bound, func() {
		stop()
		cancelCause(context.Canceled)
	}
}
