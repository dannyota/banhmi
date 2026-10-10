package oauthserver

import (
	"context"
	"errors"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/ory/fosite"

	dbauth "danny.vn/banhmi/pkg/store/auth"
)

type operationContextKey struct{}
type savepointContextKey struct{}

// FinishOutcome reports how the outer protocol transaction ended.
type FinishOutcome uint8

const (
	OutcomeCommitted FinishOutcome = iota + 1
	OutcomeRolledBack
	OutcomeReplayCommitted
	OutcomeDeniedCommitted
)

type replayCandidate struct {
	grantID   int64
	requestID string
	eventKind string
}

// Operation owns one outer PostgreSQL transaction.
type Operation struct {
	mu        sync.Mutex
	store     *Store
	tx        pgx.Tx
	ctx       context.Context
	binding   Binding
	kind      CredentialKind
	presented string
	authorizeAccountEpoch int64

	finished          bool
	storageFailed     bool
	issuanceAttempted bool
	codeCreated       bool
	pkceCreated       bool
	consentDecision   ConsentDecision
	replay            *replayCandidate
	replayComplete    bool
	openSavepoints    int
}

type savepointState struct {
	op   *Operation
	tx   pgx.Tx
	done bool
}

// Begin starts the outer transaction after validating the trusted binding.
func (s *Store) Begin(ctx context.Context, binding Binding, kind CredentialKind, presented string) (*Operation, error) {
	if s == nil || s.pool == nil || s.clients == nil || s.strategy == nil {
		return nil, errStorageFailure
	}
	if ctx == nil {
		return nil, errors.New("OAuth operation context is nil")
	}
	if _, exists := ctx.Value(operationContextKey{}).(*Operation); exists {
		return nil, errors.New("nested OAuth operations are not allowed")
	}
	if err := s.validateBinding(ctx, binding, kind, presented); err != nil {
		return nil, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, errStorageFailure
	}
	op := &Operation{store: s, tx: tx, binding: binding, kind: kind, presented: presented}
	op.ctx = context.WithValue(ctx, operationContextKey{}, op)
	if binding.Purpose == PurposeAuthorize {
		account, err := dbauth.New(tx).LockOAuthAccount(op.ctx, binding.AccountID)
		if err != nil || account.Status != "active" {
			_ = tx.Rollback(ctx)
			op.presented = ""
			if errors.Is(err, pgx.ErrNoRows) || err == nil {
				return nil, fosite.ErrAccessDenied
			}
			return nil, errStorageFailure
		}
		op.authorizeAccountEpoch = account.AuthorizationEpoch
	}
	return op, nil
}

// Context returns the context that carries this operation.
func (o *Operation) Context() context.Context { return o.ctx }

// Finish applies the reviewed commit policy to the outer transaction.
func (o *Operation) Finish(protocolErr error) (FinishOutcome, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return OutcomeRolledBack, errors.New("OAuth operation is already finished")
	}
	o.finished = true
	o.presented = ""

	commitOutcome := FinishOutcome(0)
	inconsistent := false
	switch {
	case o.storageFailed || o.openSavepoints != 0:
		inconsistent = true
	case o.replayComplete && o.replay != nil && !o.issuanceAttempted && errors.Is(protocolErr, fosite.ErrInvalidGrant):
		commitOutcome = OutcomeReplayCommitted
	case o.consentDecision == ConsentDeny && !o.issuanceAttempted && errors.Is(protocolErr, fosite.ErrAccessDenied):
		commitOutcome = OutcomeDeniedCommitted
	case protocolErr == nil && o.replay == nil && o.approvalComplete():
		commitOutcome = OutcomeCommitted
	case o.replay != nil || o.replayComplete || o.consentDecision != 0 || protocolErr == nil:
		inconsistent = true
	}

	if commitOutcome == 0 {
		if err := o.tx.Rollback(o.ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			return OutcomeRolledBack, errStorageFailure
		}
		if inconsistent {
			return OutcomeRolledBack, errStorageFailure
		}
		return OutcomeRolledBack, nil
	}
	if err := o.tx.Commit(o.ctx); err != nil {
		return OutcomeRolledBack, errStorageFailure
	}
	return commitOutcome, nil
}

func (o *Operation) approvalComplete() bool {
	if o.binding.Purpose != PurposeAuthorize {
		return true
	}
	return o.consentDecision == ConsentApprove && o.codeCreated && o.pkceCreated
}

// Rollback releases an unfinished operation and is safe after Finish.
func (o *Operation) Rollback() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.presented = ""
	if o.finished {
		return nil
	}
	o.finished = true
	if err := o.tx.Rollback(o.ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		return errStorageFailure
	}
	return nil
}

func (s *Store) BeginTX(ctx context.Context) (context.Context, error) {
	op, ok := ctx.Value(operationContextKey{}).(*Operation)
	if !ok || op == nil || op.store != s || op.finished {
		return nil, errOperationRequired
	}
	if _, nested := ctx.Value(savepointContextKey{}).(*savepointState); nested {
		return nil, errors.New("nested Fosite savepoint is already active")
	}
	if op.openSavepoints != 0 {
		return nil, errors.New("a Fosite savepoint is already active")
	}
	tx, err := op.tx.Begin(ctx)
	if err != nil {
		op.storageFailed = true
		return nil, errStorageFailure
	}
	op.openSavepoints++
	state := &savepointState{op: op, tx: tx}
	return context.WithValue(ctx, savepointContextKey{}, state), nil
}

func (s *Store) Commit(ctx context.Context) error {
	state, err := s.savepoint(ctx)
	if err != nil {
		return err
	}
	if err := state.tx.Commit(ctx); err != nil {
		state.op.storageFailed = true
		return errStorageFailure
	}
	state.done = true
	state.op.openSavepoints--
	return nil
}

func (s *Store) Rollback(ctx context.Context) error {
	state, err := s.savepoint(ctx)
	if err != nil {
		return err
	}
	if err := state.tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		state.op.storageFailed = true
		return errStorageFailure
	}
	state.done = true
	state.op.openSavepoints--
	return nil
}

func (s *Store) savepoint(ctx context.Context) (*savepointState, error) {
	op, ok := ctx.Value(operationContextKey{}).(*Operation)
	if !ok || op == nil || op.store != s || op.finished {
		return nil, errOperationRequired
	}
	state, ok := ctx.Value(savepointContextKey{}).(*savepointState)
	if !ok || state == nil || state.op != op || state.done {
		return nil, errors.New("Fosite savepoint context is required")
	}
	return state, nil
}

func (s *Store) validateBinding(ctx context.Context, binding Binding, kind CredentialKind, presented string) error {
	if binding.Resource != s.clients.resource {
		return fosite.ErrAccessDenied
	}
	switch binding.Purpose {
	case PurposeAuthorize:
		if kind != CredentialNone || presented != "" || binding.AccountID <= 0 || binding.ClientID == "" {
			return fosite.ErrAccessDenied
		}
	case PurposeTokenExchange:
		if (kind != CredentialCode && kind != CredentialRefresh) || presented == "" || binding.AccountID != 0 || binding.ClientID == "" {
			return fosite.ErrAccessDenied
		}
	case PurposeRevocation:
		if kind != CredentialAccessOrRefresh || presented == "" || binding.AccountID != 0 || binding.ClientID == "" {
			return fosite.ErrAccessDenied
		}
	case PurposeIntrospection:
		if kind != CredentialAccess || presented == "" || binding.AccountID != 0 || binding.ClientID != "" {
			return fosite.ErrAccessDenied
		}
	default:
		return fosite.ErrAccessDenied
	}
	if binding.ClientID != "" {
		if _, err := s.clients.GetClient(ctx, binding.ClientID); err != nil {
			return fosite.ErrAccessDenied
		}
	}
	return nil
}
