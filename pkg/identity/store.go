package identity

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	dbauth "danny.vn/banhmi/pkg/store/auth"
)

var _ Store = (*PostgresStore)(nil)

// PostgresStore persists identities through the generated auth queries.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore constructs an identity store over the runtime auth pool.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) CreateLogin(ctx context.Context, transaction LoginTransaction) error {
	if err := validateLoginTransaction(transaction); err != nil {
		return err
	}

	err := dbauth.New(s.pool).CreateLogin(ctx, dbauth.CreateLoginParams{
		StateHash:              transaction.StateHash,
		BrowserHash:            transaction.BrowserHash,
		NonceHash:              transaction.NonceHash,
		PkceVerifierCiphertext: transaction.PKCEVerifierCiphertext,
		ReturnPath:             transaction.ReturnPath,
		PendingConsentHash:     transaction.PendingConsentHash,
		ExpiresAt:              transaction.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("create login transaction: %w", err)
	}
	return nil
}

func (s *PostgresStore) ConsumeLogin(
	ctx context.Context,
	stateHash []byte,
	browserHash []byte,
	now time.Time,
) (LoginTransaction, error) {
	if len(stateHash) == 0 || len(browserHash) == 0 || now.IsZero() {
		return LoginTransaction{}, ErrUnauthenticated
	}

	row, err := dbauth.New(s.pool).ConsumeLogin(ctx, dbauth.ConsumeLoginParams{
		StateHash:   stateHash,
		BrowserHash: browserHash,
		Now:         now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginTransaction{}, ErrUnauthenticated
	}
	if err != nil {
		return LoginTransaction{}, fmt.Errorf("consume login transaction: %w", err)
	}

	return LoginTransaction{
		StateHash:              row.StateHash,
		BrowserHash:            row.BrowserHash,
		NonceHash:              row.NonceHash,
		PKCEVerifierCiphertext: row.PkceVerifierCiphertext,
		ReturnPath:             row.ReturnPath,
		PendingConsentHash:     row.PendingConsentHash,
		ExpiresAt:              row.ExpiresAt,
	}, nil
}

func (s *PostgresStore) CompleteLogin(
	ctx context.Context,
	profile Profile,
	sessionHash []byte,
	now time.Time,
	expiresAt time.Time,
) (Principal, error) {
	profile = normalizeProfile(profile)
	if err := validateProfile(profile); err != nil {
		return Principal{}, err
	}
	if len(sessionHash) == 0 {
		return Principal{}, fmt.Errorf("session hash is required")
	}
	if now.IsZero() || !expiresAt.After(now) {
		return Principal{}, fmt.Errorf("session times are invalid")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Principal{}, fmt.Errorf("begin login completion: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := dbauth.New(s.pool).WithTx(tx)

	account, err := queries.UpsertUserAccount(ctx, dbauth.UpsertUserAccountParams{
		Issuer:      profile.Issuer,
		Subject:     profile.Subject,
		Email:       profile.Email,
		DisplayName: profile.DisplayName,
		Now:         now,
	})
	if err != nil {
		return Principal{}, fmt.Errorf("upsert user account: %w", err)
	}
	account, err = queries.LockUserAccount(ctx, account.ID)
	if err != nil {
		return Principal{}, fmt.Errorf("lock user account: %w", err)
	}
	if account.Status == "blocked" {
		return Principal{}, ErrBlocked
	}
	if account.Status != "active" {
		return Principal{}, fmt.Errorf("unsupported account status")
	}

	err = queries.CreateWebSession(ctx, dbauth.CreateWebSessionParams{
		SessionHash:  sessionHash,
		AccountID:    account.ID,
		AccountEpoch: account.AuthorizationEpoch,
		IssuedAt:     now,
		ExpiresAt:    expiresAt,
	})
	if err != nil {
		return Principal{}, fmt.Errorf("create web session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Principal{}, fmt.Errorf("commit login completion: %w", err)
	}

	return principalFromAccount(account, sessionHash), nil
}

func (s *PostgresStore) Session(
	ctx context.Context,
	sessionHash []byte,
	now time.Time,
) (Principal, error) {
	if len(sessionHash) == 0 || now.IsZero() {
		return Principal{}, ErrUnauthenticated
	}

	row, err := dbauth.New(s.pool).CurrentWebSession(ctx, sessionHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return Principal{}, ErrUnauthenticated
	}
	if err != nil {
		return Principal{}, fmt.Errorf("load web session: %w", err)
	}
	if row.Status == "blocked" {
		return Principal{}, ErrBlocked
	}
	if row.Status != "active" || row.RevokedAt != nil || !row.ExpiresAt.After(now) ||
		row.AccountEpoch != row.AuthorizationEpoch {
		return Principal{}, ErrUnauthenticated
	}

	return Principal{
		AccountID:          row.AccountID,
		Email:              row.Email,
		DisplayName:        row.DisplayName,
		AuthorizationEpoch: row.AuthorizationEpoch,
		SessionHash:        append([]byte(nil), row.SessionHash...),
	}, nil
}

func (s *PostgresStore) RevokeSession(ctx context.Context, sessionHash []byte, now time.Time) error {
	if len(sessionHash) == 0 || now.IsZero() {
		return ErrUnauthenticated
	}
	if _, err := dbauth.New(s.pool).RevokeWebSession(ctx, dbauth.RevokeWebSessionParams{
		SessionHash: sessionHash,
		Now:         now,
	}); err != nil {
		return fmt.Errorf("revoke web session: %w", err)
	}
	return nil
}

func validateLoginTransaction(transaction LoginTransaction) error {
	if len(transaction.StateHash) == 0 {
		return fmt.Errorf("state hash is required")
	}
	if len(transaction.BrowserHash) == 0 {
		return fmt.Errorf("browser hash is required")
	}
	if len(transaction.NonceHash) == 0 {
		return fmt.Errorf("nonce hash is required")
	}
	if len(transaction.PKCEVerifierCiphertext) == 0 {
		return fmt.Errorf("PKCE verifier ciphertext is required")
	}
	if !validReturnPath(transaction.ReturnPath) {
		return fmt.Errorf("return path is invalid")
	}
	if transaction.ExpiresAt.IsZero() {
		return fmt.Errorf("login transaction expiry is required")
	}
	return nil
}

func validReturnPath(path string) bool {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "\\") {
		return false
	}
	parsed, err := url.ParseRequestURI(path)
	return err == nil && parsed.IsAbs() == false && parsed.Fragment == ""
}

func normalizeProfile(profile Profile) Profile {
	profile.Issuer = strings.TrimSpace(profile.Issuer)
	profile.Subject = strings.TrimSpace(profile.Subject)
	profile.Email = strings.ToLower(strings.TrimSpace(profile.Email))
	profile.DisplayName = strings.TrimSpace(profile.DisplayName)
	return profile
}

func validateProfile(profile Profile) error {
	if profile.Issuer == "" {
		return fmt.Errorf("profile issuer is required")
	}
	if profile.Subject == "" {
		return fmt.Errorf("profile subject is required")
	}
	if profile.Email == "" {
		return fmt.Errorf("profile email is required")
	}
	return nil
}

func principalFromAccount(account dbauth.AuthUserAccount, sessionHash []byte) Principal {
	return Principal{
		AccountID:          account.ID,
		Email:              account.Email,
		DisplayName:        account.DisplayName,
		AuthorizationEpoch: account.AuthorizationEpoch,
		SessionHash:        append([]byte(nil), sessionHash...),
	}
}
