package oauthserver

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
	fositestorage "github.com/ory/fosite/storage"

	dbauth "danny.vn/banhmi/pkg/store/auth"
)

const (
	oauthKindCode    = "authorize_code"
	oauthKindPKCE    = "pkce"
	oauthKindAccess  = "access_token"
	oauthKindRefresh = "refresh_token"
)

var (
	errOperationRequired = errors.New("OAuth storage operation is required")
	errStorageFailure    = errors.New("OAuth storage operation failed")
)

// Purpose identifies the protocol operation that owns a storage transaction.
type Purpose uint8

const (
	PurposeAuthorize Purpose = iota + 1
	PurposeTokenExchange
	PurposeRevocation
	PurposeIntrospection
)

// CredentialKind identifies the credential admitted by an operation.
type CredentialKind uint8

const (
	CredentialNone CredentialKind = iota
	CredentialCode
	CredentialRefresh
	CredentialAccess
	CredentialAccessOrRefresh
)

// Binding is the trusted request metadata enforced by the store.
type Binding struct {
	Purpose   Purpose
	ClientID  string
	Resource  string
	AccountID int64
}

// Store persists Fosite state in request-owned PostgreSQL transactions.
type Store struct {
	pool     *pgxpool.Pool
	clients  *ClientRegistry
	strategy oauth2.CoreStrategy
	codec    *RequestCodec
}

type credentialRecord struct {
	GrantID        int64
	RequestPayload []byte
	ExpiresAt      time.Time
	ConsumedAt     *time.Time
	RevokedAt      *time.Time
}

type grantRecord struct {
	ID           int64
	RequestID    string
	AccountID    int64
	AccountEpoch int64
	ClientID     string
	Resource     string
}

var (
	_ fosite.ClientManager          = (*Store)(nil)
	_ oauth2.CoreStorage            = (*Store)(nil)
	_ oauth2.TokenRevocationStorage = (*Store)(nil)
	_ pkce.PKCERequestStorage       = (*Store)(nil)
	_ fositestorage.Transactional   = (*Store)(nil)
)

// NewStore validates and constructs the PostgreSQL Fosite store.
func NewStore(pool *pgxpool.Pool, clients *ClientRegistry, strategy oauth2.CoreStrategy) (*Store, error) {
	if pool == nil {
		return nil, errors.New("OAuth store pool is nil")
	}
	if clients == nil {
		return nil, errors.New("OAuth store client registry is nil")
	}
	if strategy == nil {
		return nil, errors.New("OAuth store strategy is nil")
	}
	return &Store{pool: pool, clients: clients, strategy: strategy, codec: NewRequestCodec(clients)}, nil
}

func (s *Store) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	return s.clients.GetClient(ctx, id)
}

func (s *Store) ClientAssertionJWTValid(ctx context.Context, jti string) error {
	return s.clients.ClientAssertionJWTValid(ctx, jti)
}

func (s *Store) SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error {
	return s.clients.SetClientAssertionJWT(ctx, jti, exp)
}

func (s *Store) CreateAuthorizeCodeSession(ctx context.Context, signature string, request fosite.Requester) error {
	op, q, err := s.operationQueries(ctx, PurposeAuthorize)
	if err != nil {
		return err
	}
	op.issuanceAttempted = true
	payload, session, err := s.encodeRequest(request)
	if err != nil {
		return op.fail(err)
	}
	if session.AccountID != op.binding.AccountID || session.AccountEpoch != op.authorizeAccountEpoch ||
		session.Resource != op.binding.Resource || request.GetClient().GetID() != op.binding.ClientID {
		return fosite.ErrAccessDenied
	}
	expiresAt := session.GetExpiresAt(fosite.AuthorizeCode)
	if expiresAt.IsZero() {
		return op.fail(errors.New("authorization-code expiry is missing"))
	}
	now := time.Now().UTC()
	grant, err := q.CreateOAuthGrant(ctx, dbauth.CreateOAuthGrantParams{
		RequestID: request.GetID(), AccountID: session.AccountID, AccountEpoch: session.AccountEpoch,
		ClientID: request.GetClient().GetID(), Resource: session.Resource, CreatedAt: now, RetainUntil: expiresAt,
	})
	if err != nil {
		return op.fail(err)
	}
	if _, err := q.CreateOAuthSession(ctx, dbauth.CreateOAuthSessionParams{
		GrantID: grant.ID, Kind: oauthKindCode, Signature: signature, CreatedAt: now,
		ExpiresAt: expiresAt, RequestPayload: payload,
	}); err != nil {
		return op.fail(err)
	}
	op.codeCreated = true
	return nil
}

func (s *Store) GetAuthorizeCodeSession(ctx context.Context, signature string, target fosite.Session) (fosite.Requester, error) {
	op, q, err := s.operationQueries(ctx, PurposeTokenExchange)
	if err != nil {
		return nil, err
	}
	if op.kind != CredentialCode {
		return nil, fosite.ErrNotFound
	}
	credential, grant, err := s.lockCredential(ctx, op, q, oauthKindCode, signature)
	if err != nil {
		return nil, err
	}
	request, err := s.codec.Decode(credential.RequestPayload, target)
	if err != nil {
		return nil, op.fail(err)
	}
	if err := validateCredentialSnapshot(request, grant, credential, fosite.AuthorizeCode); err != nil {
		return nil, op.fail(err)
	}
	if credential.ConsumedAt != nil {
		if !s.proveCode(ctx, op, request, signature) {
			return nil, fosite.ErrNotFound
		}
		op.replay = &replayCandidate{grantID: grant.ID, requestID: grant.RequestID, eventKind: "code_replay"}
		return request, fosite.ErrInvalidatedAuthorizeCode
	}
	if credential.RevokedAt != nil {
		return nil, fosite.ErrNotFound
	}
	return request, nil
}

func (s *Store) InvalidateAuthorizeCodeSession(ctx context.Context, signature string) error {
	op, q, err := s.operationQueries(ctx, PurposeTokenExchange)
	if err != nil {
		return err
	}
	if err := s.validateCredentialForMutation(ctx, op, q, oauthKindCode, signature, fosite.AuthorizeCode); err != nil {
		return err
	}
	rows, err := q.ConsumeOAuthSession(ctx, dbauth.ConsumeOAuthSessionParams{Kind: oauthKindCode, Signature: signature, Now: time.Now().UTC()})
	if err != nil {
		return op.fail(err)
	}
	if rows != 1 {
		return fosite.ErrNotFound
	}
	return nil
}

func (s *Store) CreateAccessTokenSession(ctx context.Context, signature string, request fosite.Requester) error {
	op, q, err := s.operationQueriesAny(ctx)
	if err != nil {
		return err
	}
	if op.binding.Purpose != PurposeTokenExchange {
		return errOperationRequired
	}
	op.issuanceAttempted = true
	payload, session, err := s.encodeRequest(request)
	if err != nil {
		return op.fail(err)
	}
	grant, err := s.lockGrantByRequestID(ctx, op, q, request.GetID())
	if err != nil {
		return err
	}
	if err := validateWriteSnapshot(request, session, grant); err != nil {
		return op.fail(err)
	}
	expiresAt := session.GetExpiresAt(fosite.AccessToken)
	if expiresAt.IsZero() {
		return op.fail(errors.New("access-token expiry is missing"))
	}
	now := time.Now().UTC()
	if _, err := q.CreateOAuthSession(ctx, dbauth.CreateOAuthSessionParams{GrantID: grant.ID, Kind: oauthKindAccess, Signature: signature, CreatedAt: now, ExpiresAt: expiresAt, RequestPayload: payload}); err != nil {
		return op.fail(err)
	}
	if err := q.ExtendOAuthGrantRetention(ctx, dbauth.ExtendOAuthGrantRetentionParams{GrantID: grant.ID, RetainUntil: expiresAt}); err != nil {
		return op.fail(err)
	}
	return nil
}

func (s *Store) GetAccessTokenSession(ctx context.Context, signature string, target fosite.Session) (fosite.Requester, error) {
	op, q, err := s.operationQueriesAny(ctx)
	if err != nil {
		return nil, err
	}
	if op.kind != CredentialAccess && op.kind != CredentialAccessOrRefresh {
		return nil, fosite.ErrNotFound
	}
	credential, grant, err := s.lockCredential(ctx, op, q, oauthKindAccess, signature)
	if err != nil {
		return nil, err
	}
	if credential.ConsumedAt != nil || credential.RevokedAt != nil {
		return nil, fosite.ErrNotFound
	}
	request, err := s.codec.Decode(credential.RequestPayload, target)
	if err != nil {
		return nil, op.fail(err)
	}
	if err := validateCredentialSnapshot(request, grant, credential, fosite.AccessToken); err != nil {
		return nil, op.fail(err)
	}
	if op.binding.Purpose == PurposeRevocation && !s.proveAccess(ctx, op, request, signature) {
		return nil, fosite.ErrNotFound
	}
	return request, nil
}

func (s *Store) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	op, q, err := s.operationQueriesAny(ctx)
	if err != nil {
		return err
	}
	if op.binding.Purpose == PurposeIntrospection {
		return errOperationRequired
	}
	if err := s.validateCredentialForMutation(ctx, op, q, oauthKindAccess, signature, fosite.AccessToken); err != nil {
		return err
	}
	_, err = q.RevokeOAuthSession(ctx, dbauth.RevokeOAuthSessionParams{Kind: oauthKindAccess, Signature: signature, Now: time.Now().UTC()})
	if err != nil {
		return op.fail(err)
	}
	return nil
}

func (s *Store) CreateRefreshTokenSession(ctx context.Context, signature, accessSignature string, request fosite.Requester) error {
	op, q, err := s.operationQueriesAny(ctx)
	if err != nil {
		return err
	}
	if op.binding.Purpose != PurposeTokenExchange {
		return errOperationRequired
	}
	op.issuanceAttempted = true
	payload, session, err := s.encodeRequest(request)
	if err != nil {
		return op.fail(err)
	}
	grant, err := s.lockGrantByRequestID(ctx, op, q, request.GetID())
	if err != nil {
		return err
	}
	if err := validateWriteSnapshot(request, session, grant); err != nil {
		return op.fail(err)
	}
	accessRow, err := q.LockOAuthCredential(ctx, dbauth.LockOAuthCredentialParams{Kind: oauthKindAccess, Signature: accessSignature})
	if err != nil {
		return op.failDB(err)
	}
	if accessRow.GrantID != grant.ID || accessRow.RevokedAt != nil {
		return op.fail(errors.New("refresh token access association is invalid"))
	}
	accessRequest, err := s.codec.Decode(accessRow.RequestPayload, nil)
	if err != nil {
		return op.fail(err)
	}
	if err := validateCredentialSnapshot(accessRequest, grant, credentialRecord{
		GrantID: accessRow.GrantID, RequestPayload: accessRow.RequestPayload, ExpiresAt: accessRow.ExpiresAt,
		ConsumedAt: accessRow.ConsumedAt, RevokedAt: accessRow.RevokedAt,
	}, fosite.AccessToken); err != nil {
		return op.fail(err)
	}
	expiresAt := session.GetExpiresAt(fosite.RefreshToken)
	if expiresAt.IsZero() {
		return op.fail(errors.New("refresh-token expiry is missing"))
	}
	now := time.Now().UTC()
	if _, err := q.CreateOAuthRefreshSession(ctx, dbauth.CreateOAuthRefreshSessionParams{GrantID: grant.ID, Signature: signature, AccessTokenID: accessRow.ID, CreatedAt: now, ExpiresAt: expiresAt, RequestPayload: payload}); err != nil {
		return op.fail(err)
	}
	if err := q.ExtendOAuthGrantRetention(ctx, dbauth.ExtendOAuthGrantRetentionParams{GrantID: grant.ID, RetainUntil: expiresAt}); err != nil {
		return op.fail(err)
	}
	return nil
}

func (s *Store) GetRefreshTokenSession(ctx context.Context, signature string, target fosite.Session) (fosite.Requester, error) {
	op, q, err := s.operationQueriesAny(ctx)
	if err != nil {
		return nil, err
	}
	if op.kind != CredentialRefresh && op.kind != CredentialAccessOrRefresh {
		return nil, fosite.ErrNotFound
	}
	credential, grant, err := s.lockCredential(ctx, op, q, oauthKindRefresh, signature)
	if err != nil {
		return nil, err
	}
	request, err := s.codec.Decode(credential.RequestPayload, target)
	if err != nil {
		return nil, op.fail(err)
	}
	if err := validateCredentialSnapshot(request, grant, credential, fosite.RefreshToken); err != nil {
		return nil, op.fail(err)
	}
	if credential.ConsumedAt != nil {
		if op.binding.Purpose != PurposeTokenExchange || op.kind != CredentialRefresh {
			return nil, fosite.ErrInactiveToken
		}
		if !s.proveRefresh(ctx, op, request, signature) {
			return nil, fosite.ErrNotFound
		}
		op.replay = &replayCandidate{grantID: grant.ID, requestID: grant.RequestID, eventKind: "refresh_replay"}
		return request, fosite.ErrInactiveToken
	}
	if credential.RevokedAt != nil {
		return nil, fosite.ErrNotFound
	}
	if op.binding.Purpose == PurposeRevocation && !s.proveRefresh(ctx, op, request, signature) {
		return nil, fosite.ErrNotFound
	}
	return request, nil
}

func (s *Store) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	op, q, err := s.operationQueriesAny(ctx)
	if err != nil {
		return err
	}
	if op.binding.Purpose == PurposeIntrospection {
		return errOperationRequired
	}
	if err := s.validateCredentialForMutation(ctx, op, q, oauthKindRefresh, signature, fosite.RefreshToken); err != nil {
		return err
	}
	_, err = q.RevokeOAuthSession(ctx, dbauth.RevokeOAuthSessionParams{Kind: oauthKindRefresh, Signature: signature, Now: time.Now().UTC()})
	if err != nil {
		return op.fail(err)
	}
	return nil
}

func (s *Store) RotateRefreshToken(ctx context.Context, requestID, signature string) error {
	op, q, err := s.operationQueries(ctx, PurposeTokenExchange)
	if err != nil {
		return err
	}
	grant, err := s.lockAndValidateCredential(ctx, op, q, oauthKindRefresh, signature, fosite.RefreshToken)
	if err != nil {
		return err
	}
	if grant.RequestID != requestID {
		return fosite.ErrNotFound
	}
	rows, err := q.ConsumeOAuthSession(ctx, dbauth.ConsumeOAuthSessionParams{Kind: oauthKindRefresh, Signature: signature, Now: time.Now().UTC()})
	if err != nil {
		return op.fail(err)
	}
	if rows != 1 {
		return fosite.ErrInactiveToken
	}
	if err := q.RevokeOAuthAccessTokensForGrant(ctx, dbauth.RevokeOAuthAccessTokensForGrantParams{GrantID: grant.ID, Now: time.Now().UTC()}); err != nil {
		return op.fail(err)
	}
	return nil
}

func (s *Store) RevokeRefreshToken(ctx context.Context, requestID string) error {
	return s.revokeGrant(ctx, requestID)
}

func (s *Store) RevokeAccessToken(ctx context.Context, requestID string) error {
	return s.revokeGrant(ctx, requestID)
}

func (s *Store) CreatePKCERequestSession(ctx context.Context, signature string, request fosite.Requester) error {
	op, q, err := s.operationQueries(ctx, PurposeAuthorize)
	if err != nil {
		return err
	}
	op.issuanceAttempted = true
	payload, session, err := s.encodeRequest(request)
	if err != nil {
		return op.fail(err)
	}
	grant, err := s.lockGrantByRequestID(ctx, op, q, request.GetID())
	if err != nil {
		return err
	}
	if err := validateWriteSnapshot(request, session, grant); err != nil {
		return op.fail(err)
	}
	expiresAt := session.GetExpiresAt(fosite.AuthorizeCode)
	if expiresAt.IsZero() {
		return op.fail(errors.New("PKCE expiry is missing"))
	}
	if _, err := q.CreateOAuthSession(ctx, dbauth.CreateOAuthSessionParams{GrantID: grant.ID, Kind: oauthKindPKCE, Signature: signature, CreatedAt: time.Now().UTC(), ExpiresAt: expiresAt, RequestPayload: payload}); err != nil {
		return op.fail(err)
	}
	op.pkceCreated = true
	return nil
}

func (s *Store) GetPKCERequestSession(ctx context.Context, signature string, target fosite.Session) (fosite.Requester, error) {
	op, q, err := s.operationQueries(ctx, PurposeTokenExchange)
	if err != nil {
		return nil, err
	}
	credential, grant, err := s.lockCredential(ctx, op, q, oauthKindPKCE, signature)
	if err != nil {
		return nil, err
	}
	if credential.ConsumedAt != nil || credential.RevokedAt != nil {
		return nil, fosite.ErrNotFound
	}
	request, err := s.codec.Decode(credential.RequestPayload, target)
	if err != nil {
		return nil, op.fail(err)
	}
	if err := validateCredentialSnapshot(request, grant, credential, fosite.AuthorizeCode); err != nil {
		return nil, op.fail(err)
	}
	return request, nil
}

func (s *Store) DeletePKCERequestSession(ctx context.Context, signature string) error {
	op, q, err := s.operationQueries(ctx, PurposeTokenExchange)
	if err != nil {
		return err
	}
	if err := s.validateCredentialForMutation(ctx, op, q, oauthKindPKCE, signature, fosite.AuthorizeCode); err != nil {
		return err
	}
	rows, err := q.ConsumeOAuthSession(ctx, dbauth.ConsumeOAuthSessionParams{Kind: oauthKindPKCE, Signature: signature, Now: time.Now().UTC()})
	if err != nil {
		return op.fail(err)
	}
	if rows != 1 {
		return fosite.ErrNotFound
	}
	return nil
}

func (s *Store) encodeRequest(request fosite.Requester) ([]byte, *Session, error) {
	payload, err := s.codec.Encode(request)
	if err != nil {
		return nil, nil, err
	}
	session, ok := request.GetSession().(*Session)
	if !ok || session == nil {
		return nil, nil, errors.New("OAuth session has an unsupported type")
	}
	return payload, session, nil
}

func (s *Store) operationQueries(ctx context.Context, purpose Purpose) (*Operation, *dbauth.Queries, error) {
	op, q, err := s.operationQueriesAny(ctx)
	if err != nil {
		return nil, nil, err
	}
	if op.binding.Purpose != purpose {
		return nil, nil, errOperationRequired
	}
	return op, q, nil
}

func (s *Store) operationQueriesAny(ctx context.Context) (*Operation, *dbauth.Queries, error) {
	op, ok := ctx.Value(operationContextKey{}).(*Operation)
	if !ok || op == nil || op.store != s || op.finished {
		return nil, nil, errOperationRequired
	}
	executor := op.tx
	if state, nested := ctx.Value(savepointContextKey{}).(*savepointState); nested {
		if state == nil || state.op != op || state.done {
			return nil, nil, errors.New("Fosite savepoint is not active")
		}
		executor = state.tx
	}
	return op, dbauth.New(executor), nil
}

func (s *Store) lockCredential(ctx context.Context, op *Operation, q *dbauth.Queries, kind, signature string) (credentialRecord, grantRecord, error) {
	owner, err := q.FindOAuthCredentialOwner(ctx, dbauth.FindOAuthCredentialOwnerParams{Kind: kind, Signature: signature})
	if err != nil {
		return credentialRecord{}, grantRecord{}, op.notFoundOrFailure(err)
	}
	account, err := q.LockOAuthAccount(ctx, owner.AccountID)
	if err != nil {
		return credentialRecord{}, grantRecord{}, op.notFoundOrFailure(err)
	}
	grant, err := q.LockOAuthGrant(ctx, owner.GrantID)
	if err != nil {
		return credentialRecord{}, grantRecord{}, op.notFoundOrFailure(err)
	}
	credential, err := q.LockOAuthCredential(ctx, dbauth.LockOAuthCredentialParams{Kind: kind, Signature: signature})
	if err != nil {
		return credentialRecord{}, grantRecord{}, op.notFoundOrFailure(err)
	}
	if credential.GrantID != grant.ID || account.ID != grant.AccountID {
		return credentialRecord{}, grantRecord{}, op.fail(errors.New("OAuth credential ownership changed"))
	}
	if err := s.validateLockedGrant(ctx, op, account.Status, account.AuthorizationEpoch, grant.AccountEpoch, grant.ClientID, grant.Resource, grant.RevokedAt); err != nil {
		return credentialRecord{}, grantRecord{}, err
	}
	return credentialRecord{
		GrantID:        credential.GrantID,
		RequestPayload: credential.RequestPayload,
		ExpiresAt:      credential.ExpiresAt,
		ConsumedAt:     credential.ConsumedAt,
		RevokedAt:      credential.RevokedAt,
	}, grantRecord{
		ID:           grant.ID,
		RequestID:    grant.RequestID,
		AccountID:    grant.AccountID,
		AccountEpoch: grant.AccountEpoch,
		ClientID:     grant.ClientID,
		Resource:     grant.Resource,
	}, nil
}

func (s *Store) lockGrantByRequestID(ctx context.Context, op *Operation, q *dbauth.Queries, requestID string) (grantRecord, error) {
	owner, err := q.FindOAuthGrantOwner(ctx, requestID)
	if err != nil {
		return grantRecord{}, op.notFoundOrFailure(err)
	}
	account, err := q.LockOAuthAccount(ctx, owner.AccountID)
	if err != nil {
		return grantRecord{}, op.notFoundOrFailure(err)
	}
	grant, err := q.LockOAuthGrantByRequestID(ctx, requestID)
	if err != nil {
		return grantRecord{}, op.notFoundOrFailure(err)
	}
	if grant.ID != owner.GrantID || grant.AccountID != account.ID {
		return grantRecord{}, op.fail(errors.New("OAuth grant ownership changed"))
	}
	if err := s.validateLockedGrant(ctx, op, account.Status, account.AuthorizationEpoch, grant.AccountEpoch, grant.ClientID, grant.Resource, grant.RevokedAt); err != nil {
		return grantRecord{}, err
	}
	return grantRecord{
		ID:           grant.ID,
		RequestID:    grant.RequestID,
		AccountID:    grant.AccountID,
		AccountEpoch: grant.AccountEpoch,
		ClientID:     grant.ClientID,
		Resource:     grant.Resource,
	}, nil
}

func validateWriteSnapshot(request fosite.Requester, session *Session, grant grantRecord) error {
	if request == nil || session == nil || request.GetClient() == nil {
		return errors.New("OAuth request snapshot is incomplete")
	}
	if request.GetID() != grant.RequestID || session.AccountID != grant.AccountID ||
		session.AccountEpoch != grant.AccountEpoch || request.GetClient().GetID() != grant.ClientID ||
		session.Resource != grant.Resource {
		return errors.New("OAuth request snapshot does not match grant")
	}
	return nil
}

func validateCredentialSnapshot(request fosite.Requester, grant grantRecord, credential credentialRecord, tokenType fosite.TokenType) error {
	session, ok := request.GetSession().(*Session)
	if !ok || session == nil {
		return errors.New("OAuth credential snapshot session is invalid")
	}
	if err := validateWriteSnapshot(request, session, grant); err != nil {
		return err
	}
	if credential.GrantID != grant.ID || credential.ExpiresAt.IsZero() ||
		!databaseTimeEqual(session.GetExpiresAt(tokenType), credential.ExpiresAt) {
		return errors.New("OAuth credential snapshot does not match scalar state")
	}
	return nil
}

func databaseTimeEqual(left, right time.Time) bool {
	return left.Truncate(time.Microsecond).Equal(right.Truncate(time.Microsecond))
}

func (s *Store) validateCredentialForMutation(ctx context.Context, op *Operation, q *dbauth.Queries, kind, signature string, tokenType fosite.TokenType) error {
	_, err := s.lockAndValidateCredential(ctx, op, q, kind, signature, tokenType)
	return err
}

func (s *Store) lockAndValidateCredential(ctx context.Context, op *Operation, q *dbauth.Queries, kind, signature string, tokenType fosite.TokenType) (grantRecord, error) {
	credential, grant, err := s.lockCredential(ctx, op, q, kind, signature)
	if err != nil {
		return grantRecord{}, err
	}
	request, err := s.codec.Decode(credential.RequestPayload, nil)
	if err != nil {
		return grantRecord{}, op.fail(err)
	}
	if err := validateCredentialSnapshot(request, grant, credential, tokenType); err != nil {
		return grantRecord{}, op.fail(err)
	}
	return grant, nil
}

func (s *Store) validateLockedGrant(ctx context.Context, op *Operation, status string, accountEpoch, grantEpoch int64, clientID, resource string, revokedAt *time.Time) error {
	if status != "active" || accountEpoch != grantEpoch || revokedAt != nil || resource != s.clients.resource || op.binding.Resource != resource {
		return fosite.ErrNotFound
	}
	if op.binding.Purpose != PurposeIntrospection && op.binding.ClientID != clientID {
		return fosite.ErrNotFound
	}
	if _, err := s.clients.GetClient(ctx, clientID); err != nil {
		return fosite.ErrNotFound
	}
	return nil
}

func (s *Store) proveCode(ctx context.Context, op *Operation, request fosite.Requester, signature string) bool {
	return op.presented != "" && signature == s.strategy.AuthorizeCodeSignature(ctx, op.presented) && s.strategy.ValidateAuthorizeCode(ctx, request, op.presented) == nil
}

func (s *Store) proveRefresh(ctx context.Context, op *Operation, request fosite.Requester, signature string) bool {
	return op.presented != "" && signature == s.strategy.RefreshTokenSignature(ctx, op.presented) && s.strategy.ValidateRefreshToken(ctx, request, op.presented) == nil
}

func (s *Store) proveAccess(ctx context.Context, op *Operation, request fosite.Requester, signature string) bool {
	return op.presented != "" && signature == s.strategy.AccessTokenSignature(ctx, op.presented) && s.strategy.ValidateAccessToken(ctx, request, op.presented) == nil
}

func (s *Store) revokeGrant(ctx context.Context, requestID string) error {
	op, q, err := s.operationQueriesAny(ctx)
	if err != nil {
		return err
	}
	if op.binding.Purpose != PurposeTokenExchange && op.binding.Purpose != PurposeRevocation {
		return errOperationRequired
	}
	grant, err := s.lockGrantForRevocation(ctx, op, q, requestID)
	if err != nil {
		return err
	}
	if op.replay != nil && (op.replay.requestID != requestID || op.replay.grantID != grant.ID) {
		return op.fail(errors.New("OAuth replay candidate does not match grant"))
	}
	if _, err := q.RevokeOAuthGrantFamily(ctx, dbauth.RevokeOAuthGrantFamilyParams{GrantID: grant.ID, Now: time.Now().UTC(), Reason: revocationReason(op)}); err != nil {
		return op.fail(err)
	}
	if op.replay != nil && !op.replayComplete {
		if err := q.RecordOAuthSecurityEvent(ctx, dbauth.RecordOAuthSecurityEventParams{GrantID: grant.ID, EventKind: op.replay.eventKind}); err != nil {
			return op.fail(err)
		}
		op.replayComplete = true
	}
	return nil
}

func (s *Store) lockGrantForRevocation(ctx context.Context, op *Operation, q *dbauth.Queries, requestID string) (grantRecord, error) {
	owner, err := q.FindOAuthGrantOwner(ctx, requestID)
	if err != nil {
		return grantRecord{}, op.notFoundOrFailure(err)
	}
	account, err := q.LockOAuthAccount(ctx, owner.AccountID)
	if err != nil {
		return grantRecord{}, op.notFoundOrFailure(err)
	}
	grant, err := q.LockOAuthGrantByRequestID(ctx, requestID)
	if err != nil {
		return grantRecord{}, op.notFoundOrFailure(err)
	}
	if grant.ID != owner.GrantID || grant.AccountID != account.ID || account.Status != "active" ||
		account.AuthorizationEpoch != grant.AccountEpoch || grant.Resource != s.clients.resource ||
		op.binding.Resource != grant.Resource || op.binding.ClientID != grant.ClientID {
		return grantRecord{}, fosite.ErrNotFound
	}
	if _, err := s.clients.GetClient(ctx, grant.ClientID); err != nil {
		return grantRecord{}, fosite.ErrNotFound
	}
	return grantRecord{ID: grant.ID, RequestID: grant.RequestID}, nil
}

func revocationReason(op *Operation) string {
	if op.replay != nil {
		return op.replay.eventKind
	}
	return "client_revoked"
}

func (op *Operation) fail(err error) error {
	if err == nil {
		return nil
	}
	op.storageFailed = true
	return errStorageFailure
}

func (op *Operation) failDB(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fosite.ErrNotFound
	}
	return op.fail(err)
}

func (op *Operation) notFoundOrFailure(err error) error {
	return op.failDB(err)
}
