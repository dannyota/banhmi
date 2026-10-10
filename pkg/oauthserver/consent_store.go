package oauthserver

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ory/fosite"

	dbauth "danny.vn/banhmi/pkg/store/auth"
)

// PendingConsent contains the encrypted authorization request and its expiry.
type PendingConsent struct {
	RequestCiphertext []byte
	ExpiresAt         time.Time
}

// ConsentDecision identifies the one-use consent result.
type ConsentDecision uint8

const (
	ConsentApprove ConsentDecision = iota + 1
	ConsentDeny
)

func (s *Store) CreatePendingConsent(ctx context.Context, handleHash, requestCiphertext []byte, expiresAt time.Time) error {
	if err := validateShortConsentCall(ctx, handleHash, nil); err != nil {
		return err
	}
	if len(requestCiphertext) == 0 || expiresAt.IsZero() {
		return errors.New("pending consent values are invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return errStorageFailure
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := dbauth.New(tx).CreatePendingConsentRow(ctx, dbauth.CreatePendingConsentRowParams{
		HandleHash: append([]byte(nil), handleHash...), RequestCiphertext: append([]byte(nil), requestCiphertext...), ExpiresAt: expiresAt,
	}); err != nil {
		return errStorageFailure
	}
	if err := tx.Commit(ctx); err != nil {
		return errStorageFailure
	}
	return nil
}

func (s *Store) BindPendingConsent(ctx context.Context, handleHash, sessionHash []byte, now time.Time) (PendingConsent, error) {
	return s.withShortConsent(ctx, handleHash, sessionHash, now, true)
}

func (s *Store) ReadPendingConsent(ctx context.Context, handleHash, sessionHash []byte, now time.Time) (PendingConsent, error) {
	return s.withShortConsent(ctx, handleHash, sessionHash, now, false)
}

func (s *Store) withShortConsent(ctx context.Context, handleHash, sessionHash []byte, now time.Time, bind bool) (PendingConsent, error) {
	if err := validateShortConsentCall(ctx, handleHash, sessionHash); err != nil {
		return PendingConsent{}, err
	}
	if now.IsZero() {
		return PendingConsent{}, errors.New("pending consent time is invalid")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return PendingConsent{}, errStorageFailure
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := s.lockAndValidateConsent(ctx, dbauth.New(tx), handleHash, sessionHash, now, 0, bind)
	if err != nil {
		return PendingConsent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PendingConsent{}, errStorageFailure
	}
	return result, nil
}

func (s *Store) ConsumePendingConsent(ctx context.Context, handleHash, sessionHash []byte, now time.Time, decision ConsentDecision) (PendingConsent, error) {
	op, q, err := s.operationQueries(ctx, PurposeAuthorize)
	if err != nil {
		return PendingConsent{}, err
	}
	if len(handleHash) == 0 || len(sessionHash) == 0 || now.IsZero() || (decision != ConsentApprove && decision != ConsentDeny) {
		return PendingConsent{}, fosite.ErrAccessDenied
	}
	result, err := s.lockAndValidateConsent(ctx, q, handleHash, sessionHash, now, op.binding.AccountID, false)
	if err != nil {
		return PendingConsent{}, err
	}
	owner, err := q.FindPendingConsentSessionOwner(ctx, dbauth.FindPendingConsentSessionOwnerParams{HandleHash: handleHash, SessionHash: sessionHash})
	if err != nil {
		return PendingConsent{}, op.notFoundOrFailure(err)
	}
	rows, err := q.ConsumePendingConsentRow(ctx, dbauth.ConsumePendingConsentRowParams{ConsentID: owner.ConsentID, WebSessionID: pgtype.Int8{Int64: owner.WebSessionID, Valid: true}, Now: now})
	if err != nil {
		return PendingConsent{}, op.fail(err)
	}
	if rows != 1 {
		return PendingConsent{}, fosite.ErrAccessDenied
	}
	op.consentDecision = decision
	return result, nil
}

func (s *Store) lockAndValidateConsent(ctx context.Context, q *dbauth.Queries, handleHash, sessionHash []byte, now time.Time, requiredAccountID int64, bind bool) (PendingConsent, error) {
	owner, err := q.FindPendingConsentSessionOwner(ctx, dbauth.FindPendingConsentSessionOwnerParams{HandleHash: handleHash, SessionHash: sessionHash})
	if err != nil {
		return PendingConsent{}, consentNotFound(err)
	}
	if requiredAccountID > 0 && owner.AccountID != requiredAccountID {
		return PendingConsent{}, fosite.ErrAccessDenied
	}
	account, err := q.LockOAuthAccount(ctx, owner.AccountID)
	if err != nil {
		return PendingConsent{}, consentNotFound(err)
	}
	webSession, err := q.LockWebSessionForConsent(ctx, owner.WebSessionID)
	if err != nil {
		return PendingConsent{}, consentNotFound(err)
	}
	consent, err := q.LockPendingConsent(ctx, owner.ConsentID)
	if err != nil {
		return PendingConsent{}, consentNotFound(err)
	}
	if owner.AccountID != webSession.AccountID ||
		account.Status != "active" || account.AuthorizationEpoch != webSession.AccountEpoch ||
		webSession.RevokedAt != nil || !now.Before(webSession.ExpiresAt) || consent.Consumed || !now.Before(consent.ExpiresAt) {
		return PendingConsent{}, fosite.ErrAccessDenied
	}
	if consent.BoundWebSessionID == 0 {
		if !bind {
			return PendingConsent{}, fosite.ErrAccessDenied
		}
		rows, err := q.BindPendingConsentRow(ctx, dbauth.BindPendingConsentRowParams{WebSessionID: pgtype.Int8{Int64: webSession.ID, Valid: true}, Now: now, ConsentID: consent.ID})
		if err != nil {
			return PendingConsent{}, errStorageFailure
		}
		if rows != 1 {
			return PendingConsent{}, fosite.ErrAccessDenied
		}
	} else if consent.BoundWebSessionID != webSession.ID {
		return PendingConsent{}, fosite.ErrAccessDenied
	}
	return PendingConsent{RequestCiphertext: bytes.Clone(consent.RequestCiphertext), ExpiresAt: consent.ExpiresAt}, nil
}

func validateShortConsentCall(ctx context.Context, handleHash, sessionHash []byte) error {
	if ctx == nil {
		return errors.New("pending consent context is nil")
	}
	if _, active := ctx.Value(operationContextKey{}).(*Operation); active {
		return errors.New("pending consent short transaction cannot use an OAuth operation")
	}
	if len(handleHash) == 0 || sessionHash != nil && len(sessionHash) == 0 {
		return errors.New("pending consent hashes are invalid")
	}
	return nil
}

func consentNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fosite.ErrAccessDenied
	}
	return errStorageFailure
}
