package document

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/document/documentdb"
)

// TransactionStarter supplies independent READ COMMITTED transactions, never
// nested transactions. A pgx connection or pool implements this port.
type TransactionStarter interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// Grant binds a registered intake execution and its immutable input digests.
// The adapter resolves every field against the database, including on replay.
type Grant struct {
	// Execution identifies the owning workflow, country, lease and fence.
	Execution    run.ScopedExecution
	// ExecutionID is the registered writer-database execution ID.
	ExecutionID  int64
	// InputDigest pins the execution's immutable input manifest.
	InputDigest  model.Digest
	// RecipeDigest pins the registered processing recipe.
	RecipeDigest model.Digest
	// BudgetDigest pins the registered effective budget manifest.
	BudgetDigest model.Digest
}

// ProvisionLimits bounds the bulk transport and each item's retained evidence.
// These are operator-supplied ceilings, not document-specific policy defaults.
type ProvisionLimits struct {
	// MaxItems bounds the number of provisions in one transaction.
	MaxItems         int
	// MaxPayloadBytes bounds the complete encoded bulk transport.
	MaxPayloadBytes  int
	// MaxCitationBytes bounds each exact native citation before hex encoding.
	MaxCitationBytes int
	// MaxAnchorBytes bounds each item's encoded anchor JSON.
	MaxAnchorBytes   int
	// MaxAnchors bounds each item's top-level anchor array.
	MaxAnchors       int
}

// RejectionError identifies invalid adapter input without retaining its content.
type RejectionError struct {
	// Reason is a machine code, never input content.
	Reason string
}

// Error returns a machine reason without document or citation bytes.
func (e *RejectionError) Error() string { return "document input rejected: " + e.Reason }

func reject(reason string) error { return &RejectionError{Reason: reason} }

// Revisions implements fenced identity and immutable revision persistence.
// Construction performs no I/O. Reads require the same current grant as writes.
type Revisions struct {
	db     TransactionStarter
	writer string
	limits ProvisionLimits
}

// NewRevisions constructs an adapter with explicit positive resource ceilings.
func NewRevisions(db TransactionStarter, writer string, limits ProvisionLimits) (*Revisions, error) {
	if db == nil {
		return nil, reject("transaction_starter_required")
	}
	if err := model.ValidateKey(writer); err != nil {
		return nil, fmt.Errorf("writer identity: %w", err)
	}
	if limits.MaxItems <= 0 || limits.MaxPayloadBytes <= 0 || limits.MaxCitationBytes <= 0 || limits.MaxAnchorBytes <= 0 || limits.MaxAnchors <= 0 {
		return nil, reject("positive_provision_limits_required")
	}
	return &Revisions{db: db, writer: writer, limits: limits}, nil
}

func validateGrant(g Grant) error {
	if err := g.Execution.Validate(); err != nil {
		return fmt.Errorf("execution grant: %w", err)
	}
	if g.Execution.Scope.Kind != run.ScopeIntake || g.Execution.Scope.Subject.Kind != run.SubjectDocumentVersion || g.ExecutionID <= 0 {
		return fmt.Errorf("document version grant required: %w", run.ErrStaleGrant)
	}
	for _, d := range []model.Digest{g.InputDigest, g.RecipeDigest, g.BudgetDigest} {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("grant digest: %w", err)
		}
	}
	return nil
}

func revisionTransaction[T any](ctx context.Context, r *Revisions, g Grant, fn func(pgx.Tx, documentdb.Querier) (T, error)) (T, error) {
	var zero T
	if err := validateGrant(g); err != nil {
		return zero, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return zero, databaseError("begin revisions", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	rollback := func(cause error) (T, error) {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			// Do not retain the retry sentinel if rollback did not complete.
			// Retrying with a possibly open transaction is forbidden.
			return zero, databaseError("rollback revisions", err)
		}
		return zero, cause
	}
	if err := r.lockGrant(ctx, tx, g); err != nil {
		return rollback(err)
	}
	value, err := fn(tx, documentdb.New(tx))
	if err != nil {
		// Payload classification may follow a lock or a long read. A stale
		// grant takes precedence over a conflict, including on replay. The
		// bulk sentinel is excluded because it forbids any further statement.
		if errors.Is(err, run.ErrPayloadConflict) {
			if grantErr := checkDocumentLease(ctx, tx, g); grantErr != nil {
				return rollback(grantErr)
			}
		}
		return rollback(err)
	}
	if err := checkDocumentLease(ctx, tx, g); err != nil {
		return rollback(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return zero, databaseError("commit revisions", err)
	}
	return value, nil
}

// Database diagnostics can include document values in DETAIL. Retain the cause
// for errors.Is/As without rendering its content in the adapter's error string.
type databaseCause struct{ cause error }

func (e databaseCause) Error() string {
	var state interface{ SQLState() string }
	if errors.As(e.cause, &state) {
		return "database state " + state.SQLState()
	}
	return "database operation failed"
}

func (e databaseCause) Unwrap() error { return e.cause }

func databaseError(op string, err error) error {
	return fmt.Errorf("%s: %w", op, databaseCause{cause: err})
}

func grantError(op string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", op, run.ErrStaleGrant)
	}
	return databaseError(op, err)
}

func conflict(op string) error { return fmt.Errorf("%s: %w", op, run.ErrPayloadConflict) }

// RAW QUERY: LockDocumentAdmission. Acquire admission before any lease lock;
// compare the locked row after a possible admission-rotation wait.
func (r *Revisions) lockGrant(ctx context.Context, tx pgx.Tx, g Grant) error {
	var country, writer string
	var epoch int64
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT a.country_code, a.admission_epoch, a.admitted_writer, a.enabled
		FROM ingest.country_admission a WHERE a.id = 1 FOR SHARE OF a`).Scan(&country, &epoch, &writer, &enabled)
	if err != nil {
		return grantError("lock document admission", err)
	}
	e := g.Execution
	if country != e.Country || writer != r.writer || epoch != e.AdmissionEpoch || !enabled {
		return fmt.Errorf("document admission: %w", run.ErrStaleGrant)
	}
	if err := checkDocumentLease(ctx, tx, g); err != nil {
		return err
	}
	// RAW QUERY: ResolveDocumentExecution. Resolve the supplied local ID against
	// all registered immutable execution identity fields, not only its hashes.
	var id int64
	err = tx.QueryRow(ctx, `SELECT e.id FROM ingest.execution e WHERE e.id = $1
		AND e.workflow_id = $2 AND e.workflow_kind = $3 AND e.processing_attempt_key = $4
		AND e.attempt_fence = $5 AND e.admission_epoch = $6 AND e.scope_kind = 'intake'
		AND e.resource_kind = 'document_version' AND e.resource_key = $7
		AND e.input_digest = $8 AND e.recipe_digest = $9 AND e.budget_digest = $10`,
		g.ExecutionID, e.WorkflowID, string(e.WorkflowKind), e.AttemptKey, e.AttemptFence,
		e.AdmissionEpoch, e.Scope.Subject.LeaseKey(), string(g.InputDigest), string(g.RecipeDigest), string(g.BudgetDigest)).Scan(&id)
	if err != nil {
		return grantError("resolve document execution", err)
	}
	return nil
}

// RAW QUERIES: LockDocumentLease and ReadDocumentGrantTime. Read the clock in a
// separate statement after locking, so unchanged-row waits cannot hide expiry.
func checkDocumentLease(ctx context.Context, tx pgx.Tx, g Grant) error {
	var owner string
	var fence, epoch int64
	var expiry time.Time
	var cancelled *time.Time
	err := tx.QueryRow(ctx, `SELECT l.owner, l.attempt_fence, l.admission_epoch, l.expires_at, l.cancelled_at
		FROM ingest.execution_lease l WHERE l.resource_kind = 'document_version' AND l.resource_key = $1 FOR UPDATE OF l`,
		g.Execution.Scope.Subject.LeaseKey()).Scan(&owner, &fence, &epoch, &expiry, &cancelled)
	if err != nil {
		return grantError("lock document lease", err)
	}
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return databaseError("read document grant time", err)
	}
	e := g.Execution
	if owner != e.Owner || fence != e.AttemptFence || epoch != e.AdmissionEpoch || cancelled != nil || !now.Before(expiry) {
		return fmt.Errorf("document lease: %w", run.ErrStaleGrant)
	}
	return nil
}

func validateDocumentKey(key, country string, revision bool) error {
	if len(key) >= 2 && key[:2] != country {
		return reject("wrong_country")
	}
	if err := model.ValidateRevisionKey(key, country); err != nil {
		return fmt.Errorf("document key: %w", err)
	}
	if (len(key) == 112) != revision {
		return reject("wrong_document_key_kind")
	}
	return nil
}

func validateText(values ...string) error {
	for _, v := range values {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return reject("invalid_text_encoding")
		}
	}
	return nil
}

// ObservedAssertion is the natural identity assertion key. Number and type may
// be empty when the source supplied no value; they are never inferred here.
type ObservedAssertion struct {
	// ObservationKey is a retained, ledger-issued observation key.
	ObservationKey   string
	// SourceID identifies the observation's source.
	SourceID         string
	// ExternalID is the source's retained document identifier.
	ExternalID       string
	// NormalizedNumber retains the observed canonical number, when available.
	NormalizedNumber string
	// DocumentType retains the observed type, when available.
	DocumentType     string
	// TitleHash pins the title evidence without carrying its text.
	TitleHash        model.Digest
}

func (a ObservedAssertion) validate() error {
	if err := model.ValidateEnvelopeKey(a.ObservationKey, "observation"); err != nil {
		return fmt.Errorf("observation key: %w", err)
	}
	if a.SourceID == "" || a.ExternalID == "" {
		return reject("source_and_external_id_required")
	}
	if err := a.TitleHash.Validate(); err != nil {
		return fmt.Errorf("title hash: %w", err)
	}
	return validateText(a.SourceID, a.ExternalID, a.NormalizedNumber, a.DocumentType)
}

func (a ObservedAssertion) params() documentdb.ReadIdentityByAssertionParams {
	return documentdb.ReadIdentityByAssertionParams{ObservationKey: a.ObservationKey, SourceID: a.SourceID,
		ExternalID: a.ExternalID, NormalizedNumber: a.NormalizedNumber, DocumentType: a.DocumentType, TitleHash: string(a.TitleHash)}
}

// RAW QUERY: ResolveDocumentObservation. Prove source and version ownership
// before classifying a replay. An assertion key alone cannot authorize reuse.
func verifyObservation(ctx context.Context, tx pgx.Tx, g Grant, a ObservedAssertion) error {
	var id int64
	err := tx.QueryRow(ctx, `SELECT o.id FROM bronze.observation_revision o
		JOIN bronze.source_record s ON s.id = o.source_record_id
		WHERE o.observation_key = $1 AND o.document_version_key = $2 AND s.source_id = $3 AND s.external_id = $4`,
		a.ObservationKey, g.Execution.Scope.Subject.LeaseKey(), a.SourceID, a.ExternalID).Scan(&id)
	if err != nil {
		return grantError("resolve document observation", err)
	}
	return nil
}

// RAW QUERY: ReadDocumentIdentityByID. The generated mint returns a local ID,
// which can refer to a reused identity with a different proposed mint key.
func identityByID(ctx context.Context, tx pgx.Tx, id int64) (documentdb.ReadDocumentIdentityRow, error) {
	var row documentdb.ReadDocumentIdentityRow
	err := tx.QueryRow(ctx, `SELECT d.id, d.identity_key, d.entity_kind, d.created_by_execution_key
		FROM silver.document_identity d WHERE d.id = $1`, id).Scan(&row.ID, &row.IdentityKey, &row.EntityKind, &row.CreatedByExecutionKey)
	if err != nil {
		return row, databaseError("read document identity by ID", err)
	}
	return row, nil
}

func observedIdentity(rows []documentdb.ReadIdentityByAssertionRow) (int64, error) {
	var id int64
	for _, row := range rows {
		if row.Kind != string(model.IdentityAssertionObserved) {
			continue
		}
		if id != 0 && id != row.ID {
			return 0, conflict("ambiguous observed identity")
		}
		id = row.ID
	}
	return id, nil
}

// MintDocumentIdentity mints from a retained observation, or returns its original
// observed identity. A different proposed mint key does not replace that key.
func (r *Revisions) MintDocumentIdentity(ctx context.Context, g Grant, key string, a ObservedAssertion) (documentdb.ReadDocumentIdentityRow, error) {
	var zero documentdb.ReadDocumentIdentityRow
	if err := validateDocumentKey(key, g.Execution.Country, false); err != nil {
		return zero, err
	}
	if err := a.validate(); err != nil {
		return zero, err
	}
	return revisionTransaction(ctx, r, g, func(tx pgx.Tx, q documentdb.Querier) (documentdb.ReadDocumentIdentityRow, error) {
		if err := verifyObservation(ctx, tx, g, a); err != nil {
			return zero, err
		}
		rows, err := q.ReadIdentityByAssertion(ctx, a.params())
		if err != nil {
			return zero, databaseError("read mint assertion", err)
		}
		id, err := observedIdentity(rows)
		if err != nil {
			return zero, err
		}
		if id != 0 {
			return identityByID(ctx, tx, id)
		}
		if _, err := q.ReadDocumentIdentity(ctx, key); err == nil {
			return zero, conflict("mint key already used")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return zero, databaseError("read proposed mint key", err)
		}
		// A unique-key loser must restore the transaction before the wrapper
		// rechecks expiry, which takes precedence over payload conflict.
		if _, err := tx.Exec(ctx, "SAVEPOINT document_mint"); err != nil {
			return zero, databaseError("savepoint document mint", err)
		}
		e := g.Execution
		_, insertErr := q.MintDocumentIdentity(ctx, documentdb.MintDocumentIdentityParams{
			AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: r.writer, ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(),
			Column5: e.Owner, Column6: e.AttemptFence, ID: g.ExecutionID, InputDigest: string(g.InputDigest), RecipeDigest: string(g.RecipeDigest), BudgetDigest: string(g.BudgetDigest),
			PKey: key, ObservationKey: a.ObservationKey, SourceID: a.SourceID, ExternalID: a.ExternalID, PNumber: a.NormalizedNumber, PType: a.DocumentType, PTitle: string(a.TitleHash)})
		if insertErr != nil && !errors.Is(insertErr, pgx.ErrNoRows) {
			var state interface{ SQLState() string }
			if errors.As(insertErr, &state) && state.SQLState() == "23505" {
				if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT document_mint"); err != nil {
					return zero, databaseError("restore document mint", err)
				}
				return zero, conflict("mint key collision")
			}
			return zero, databaseError("mint document identity", insertErr)
		}
		if err := checkDocumentLease(ctx, tx, g); err != nil {
			return zero, err
		}
		// Fresh statement snapshot after mint's natural-key advisory wait.
		rows, err = q.ReadIdentityByAssertion(ctx, a.params())
		if err != nil {
			return zero, databaseError("resolve minted assertion", err)
		}
		id, err = observedIdentity(rows)
		if err != nil {
			return zero, err
		}
		if id == 0 {
			return zero, grantError("mint returned no identity", pgx.ErrNoRows)
		}
		return identityByID(ctx, tx, id)
	})
}

// IdentityAssertion is an immutable identity decision. RelatedIdentityID is
// required only for merge and split; those operations preserve both old keys.
type IdentityAssertion struct {
	// ObservedAssertion binds the decision to its natural observation key.
	ObservedAssertion
	// DocumentIdentityID identifies the decision's subject.
	DocumentIdentityID int64
	// Kind distinguishes observation, merge, split and continuity evidence.
	Kind               model.IdentityAssertionKind
	// RelatedIdentityID is present exactly for merge and split assertions.
	RelatedIdentityID  *int64
	// ReviewState is the immutable assessment of this evidence.
	ReviewState        model.IdentityReviewState
	// EvidenceHash pins the decision's supporting evidence.
	EvidenceHash       model.Digest
}

// AssertionReceipt retains the original assertion ID and execution provenance.
type AssertionReceipt struct {
	// ID is the committed assertion's writer-database ID.
	ID                 int64
	// Assertion retains every immutable natural-key field.
	Assertion          IdentityAssertion
	// IntakeExecutionKey retains the original authoring execution on replay.
	IntakeExecutionKey string
}

func (a IdentityAssertion) validate() error {
	if err := a.ObservedAssertion.validate(); err != nil {
		return err
	}
	if a.DocumentIdentityID <= 0 || !a.Kind.Valid() || !a.ReviewState.Valid() {
		return reject("invalid_assertion_identity_or_vocabulary")
	}
	if (a.Kind == model.IdentityAssertionMerge || a.Kind == model.IdentityAssertionSplit) != (a.RelatedIdentityID != nil) {
		return reject("assertion_related_identity_pair")
	}
	if a.RelatedIdentityID != nil && (*a.RelatedIdentityID <= 0 || *a.RelatedIdentityID == a.DocumentIdentityID) {
		return reject("invalid_related_identity")
	}
	if err := a.EvidenceHash.Validate(); err != nil {
		return fmt.Errorf("assertion evidence hash: %w", err)
	}
	return nil
}

// RAW QUERY: ReadIdentityAssertionReceipt. All natural-key fields participate in
// uniqueness, including review state and evidence. Reassessment is a new row.
func assertionReceipt(ctx context.Context, tx pgx.Tx, a IdentityAssertion) (AssertionReceipt, error) {
	v := AssertionReceipt{Assertion: a}
	err := tx.QueryRow(ctx, `SELECT a.id, a.intake_execution_key FROM silver.identity_assertion a
		WHERE a.document_identity_id = $1 AND a.observation_key = $2 AND a.source_id = $3 AND a.external_id = $4
		AND a.normalized_number = $5 AND a.document_type = $6 AND a.title_hash = $7 AND a.kind = $8
		AND a.related_identity_id IS NOT DISTINCT FROM $9::bigint AND a.review_state = $10 AND a.evidence_hash = $11`,
		a.DocumentIdentityID, a.ObservationKey, a.SourceID, a.ExternalID, a.NormalizedNumber, a.DocumentType, string(a.TitleHash),
		string(a.Kind), a.RelatedIdentityID, string(a.ReviewState), string(a.EvidenceHash)).Scan(&v.ID, &v.IntakeExecutionKey)
	return v, err
}

// AppendIdentityAssertion appends an observed, merge, split or continuity
// assertion. Identical natural-key replays retain the original receipt.
func (r *Revisions) AppendIdentityAssertion(ctx context.Context, g Grant, a IdentityAssertion) (AssertionReceipt, error) {
	var zero AssertionReceipt
	if err := a.validate(); err != nil {
		return zero, err
	}
	return revisionTransaction(ctx, r, g, func(tx pgx.Tx, q documentdb.Querier) (AssertionReceipt, error) {
		if err := verifyObservation(ctx, tx, g, a.ObservedAssertion); err != nil {
			return zero, err
		}
		for _, id := range []*int64{&a.DocumentIdentityID, a.RelatedIdentityID} {
			if id == nil {
				continue
			}
			row, err := identityByID(ctx, tx, *id)
			if err != nil {
				return zero, err
			}
			if err := validateDocumentKey(row.IdentityKey, g.Execution.Country, false); err != nil {
				return zero, err
			}
		}
		stored, err := assertionReceipt(ctx, tx, a)
		if err == nil {
			return stored, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, databaseError("read assertion receipt", err)
		}
		e := g.Execution
		_, err = q.AppendIdentityAssertion(ctx, documentdb.AppendIdentityAssertionParams{
			AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: r.writer, ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(),
			Column5: e.Owner, Column6: e.AttemptFence, ID: g.ExecutionID, InputDigest: string(g.InputDigest), RecipeDigest: string(g.RecipeDigest), BudgetDigest: string(g.BudgetDigest),
			DocumentIdentityID: a.DocumentIdentityID, ObservationKey: a.ObservationKey, SourceID: a.SourceID, ExternalID: a.ExternalID,
			NormalizedNumber: a.NormalizedNumber, DocumentType: a.DocumentType, TitleHash: string(a.TitleHash), Kind: string(a.Kind), RelatedIdentityID: a.RelatedIdentityID,
			ReviewState: string(a.ReviewState), EvidenceHash: string(a.EvidenceHash)})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return zero, databaseError("append assertion", err)
		}
		if err := checkDocumentLease(ctx, tx, g); err != nil {
			return zero, err
		}
		// A fresh snapshot resolves a winner hidden by ON CONFLICT.
		stored, err = assertionReceipt(ctx, tx, a)
		if err != nil {
			return zero, grantError("resolve assertion receipt", err)
		}
		return stored, nil
	})
}

// ReadDocumentIdentity reads an exact country-qualified identity under a grant.
func (r *Revisions) ReadDocumentIdentity(ctx context.Context, g Grant, key string) (documentdb.ReadDocumentIdentityRow, error) {
	var zero documentdb.ReadDocumentIdentityRow
	if err := validateDocumentKey(key, g.Execution.Country, false); err != nil {
		return zero, err
	}
	return revisionTransaction(ctx, r, g, func(_ pgx.Tx, q documentdb.Querier) (documentdb.ReadDocumentIdentityRow, error) {
		v, err := q.ReadDocumentIdentity(ctx, key)
		if err != nil {
			return v, databaseError("read document identity", err)
		}
		return v, nil
	})
}

// ReadIdentityByAssertion exposes every retained decision for an assertion key.
func (r *Revisions) ReadIdentityByAssertion(ctx context.Context, g Grant, a ObservedAssertion) ([]documentdb.ReadIdentityByAssertionRow, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}
	return revisionTransaction(ctx, r, g, func(_ pgx.Tx, q documentdb.Querier) ([]documentdb.ReadIdentityByAssertionRow, error) {
		v, err := q.ReadIdentityByAssertion(ctx, a.params())
		if err != nil {
			return nil, databaseError("read identities by assertion", err)
		}
		return v, nil
	})
}

// LegacyMapping is one entry in a hash-pinned, country-qualified export manifest.
// The importing execution's input digest must equal ExportManifestDigest.
type LegacyMapping struct {
	// Country comes from the pinned export manifest.
	Country              string
	// ExportManifestDigest identifies the immutable export input.
	ExportManifestDigest model.Digest
	// EntityKind admits document mappings only.
	EntityKind           model.IdentityEntityKind
	// LegacyID is the identifier within the exported legacy catalog.
	LegacyID             string
	// State preserves resolved, ambiguous or unavailable identity evidence.
	State                model.LegacyIdentityMappingState
	// DocumentIdentityID is present exactly when State is resolved.
	DocumentIdentityID   *int64
	// CandidateReportHash is present exactly when State is ambiguous.
	CandidateReportHash  *model.Digest
}

func (m LegacyMapping) validate(g Grant) error {
	if m.Country != g.Execution.Country {
		return reject("wrong_country")
	}
	if err := m.ExportManifestDigest.Validate(); err != nil {
		return fmt.Errorf("export manifest digest: %w", err)
	}
	if m.ExportManifestDigest != g.InputDigest {
		return conflict("import execution manifest digest")
	}
	if !m.EntityKind.Valid() || !m.State.Valid() || m.LegacyID == "" {
		return reject("invalid_legacy_mapping_vocabulary_or_id")
	}
	if (m.State == model.LegacyIdentityResolved) != (m.DocumentIdentityID != nil) ||
		(m.State == model.LegacyIdentityAmbiguous) != (m.CandidateReportHash != nil) {
		return reject("legacy_mapping_state_fields")
	}
	if m.DocumentIdentityID != nil && *m.DocumentIdentityID <= 0 {
		return reject("invalid_legacy_identity_id")
	}
	if m.CandidateReportHash != nil {
		if err := m.CandidateReportHash.Validate(); err != nil {
			return fmt.Errorf("candidate report hash: %w", err)
		}
	}
	return validateText(m.LegacyID)
}

func mappingMatch(v documentdb.ReadLegacyIdentityMappingsRow, m LegacyMapping) bool {
	var report *string
	if m.CandidateReportHash != nil {
		value := string(*m.CandidateReportHash)
		report = &value
	}
	return v.State == string(m.State) && reflect.DeepEqual(v.DocumentIdentityID, m.DocumentIdentityID) && reflect.DeepEqual(v.CandidateReportHash, report)
}

func findMapping(ctx context.Context, q documentdb.Querier, m LegacyMapping) (documentdb.ReadLegacyIdentityMappingsRow, error) {
	rows, err := q.ReadLegacyIdentityMappings(ctx, documentdb.ReadLegacyIdentityMappingsParams{EntityKind: string(m.EntityKind), LegacyID: m.LegacyID})
	if err != nil {
		return documentdb.ReadLegacyIdentityMappingsRow{}, databaseError("read legacy mapping receipt", err)
	}
	for _, v := range rows {
		if v.ExportManifestDigest == string(m.ExportManifestDigest) {
			if !mappingMatch(v, m) {
				return v, conflict("legacy mapping payload")
			}
			return v, nil
		}
	}
	return documentdb.ReadLegacyIdentityMappingsRow{}, pgx.ErrNoRows
}

// InsertLegacyIdentityMapping retains resolved, ambiguous or unavailable import
// evidence. It does not select an identity for an ambiguous entry.
func (r *Revisions) InsertLegacyIdentityMapping(ctx context.Context, g Grant, m LegacyMapping) (documentdb.ReadLegacyIdentityMappingsRow, error) {
	var zero documentdb.ReadLegacyIdentityMappingsRow
	if err := m.validate(g); err != nil {
		return zero, err
	}
	return revisionTransaction(ctx, r, g, func(tx pgx.Tx, q documentdb.Querier) (documentdb.ReadLegacyIdentityMappingsRow, error) {
		if m.DocumentIdentityID != nil {
			v, err := identityByID(ctx, tx, *m.DocumentIdentityID)
			if err != nil {
				return zero, err
			}
			if err := validateDocumentKey(v.IdentityKey, m.Country, false); err != nil {
				return zero, err
			}
		}
		v, err := findMapping(ctx, q, m)
		if !errors.Is(err, pgx.ErrNoRows) {
			return v, err
		}
		var report *string
		if m.CandidateReportHash != nil {
			h := string(*m.CandidateReportHash)
			report = &h
		}
		e := g.Execution
		_, err = q.InsertLegacyIdentityMapping(ctx, documentdb.InsertLegacyIdentityMappingParams{
			AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: r.writer, ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(),
			Column5: e.Owner, Column6: e.AttemptFence, ID: g.ExecutionID, InputDigest: string(g.InputDigest), RecipeDigest: string(g.RecipeDigest), BudgetDigest: string(g.BudgetDigest),
			ExportManifestDigest: string(m.ExportManifestDigest), EntityKind: string(m.EntityKind), LegacyID: m.LegacyID, State: string(m.State),
			DocumentIdentityID: m.DocumentIdentityID, CandidateReportHash: report, Column17: m.Country})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return zero, databaseError("insert legacy mapping", err)
		}
		if err := checkDocumentLease(ctx, tx, g); err != nil {
			return zero, err
		}
		v, err = findMapping(ctx, q, m)
		if errors.Is(err, pgx.ErrNoRows) {
			return zero, grantError("resolve legacy mapping receipt", err)
		}
		return v, err
	})
}

func validateLegacyLookup(kind model.IdentityEntityKind, id string) error {
	if !kind.Valid() || id == "" {
		return reject("invalid_legacy_lookup")
	}
	return validateText(id)
}

// ResolveLegacyIdentity returns an identity only if all retained mappings are
// resolved and agree. No agreement returns an error wrapping pgx.ErrNoRows.
func (r *Revisions) ResolveLegacyIdentity(ctx context.Context, g Grant, kind model.IdentityEntityKind, id string) (documentdb.ResolveLegacyIdentityRow, error) {
	var zero documentdb.ResolveLegacyIdentityRow
	if err := validateLegacyLookup(kind, id); err != nil {
		return zero, err
	}
	return revisionTransaction(ctx, r, g, func(_ pgx.Tx, q documentdb.Querier) (documentdb.ResolveLegacyIdentityRow, error) {
		v, err := q.ResolveLegacyIdentity(ctx, documentdb.ResolveLegacyIdentityParams{EntityKind: string(kind), LegacyID: id})
		if err != nil {
			return v, databaseError("resolve legacy identity agreement", err)
		}
		return v, nil
	})
}

// ReadLegacyIdentityMappings exposes retained mappings and ambiguity reports.
func (r *Revisions) ReadLegacyIdentityMappings(ctx context.Context, g Grant, kind model.IdentityEntityKind, id string) ([]documentdb.ReadLegacyIdentityMappingsRow, error) {
	if err := validateLegacyLookup(kind, id); err != nil {
		return nil, err
	}
	return revisionTransaction(ctx, r, g, func(_ pgx.Tx, q documentdb.Querier) ([]documentdb.ReadLegacyIdentityMappingsRow, error) {
		v, err := q.ReadLegacyIdentityMappings(ctx, documentdb.ReadLegacyIdentityMappingsParams{EntityKind: string(kind), LegacyID: id})
		if err != nil {
			return nil, databaseError("read legacy mappings", err)
		}
		return v, nil
	})
}

// RevisionInput contains the immutable document payload, without caller-supplied
// execution provenance. The adapter obtains provenance from the current grant.
type RevisionInput struct {
	// DocumentIdentityID identifies the parent identity.
	DocumentIdentityID        int64
	// RevisionKey embeds the identity key and immutable revision digest.
	RevisionKey               string
	// SourceObservationSetHash pins the selected source observations.
	SourceObservationSetHash  model.Digest
	// TextHash pins the normalized document text.
	TextHash                  model.Digest
	// MetadataHash pins the immutable metadata payload.
	MetadataHash              model.Digest
	// ConversionRecipeDigest pins conversion inputs and implementation.
	ConversionRecipeDigest    model.Digest
	// NormalizationRecipeDigest pins text normalization.
	NormalizationRecipeDigest model.Digest
	// ParserRecipeDigest pins structure parsing.
	ParserRecipeDigest        model.Digest
	// PolicyRecipeDigest pins the selected policy bundle.
	PolicyRecipeDigest        model.Digest
	// NormalizedNumber retains the observed number without inventing a value.
	NormalizedNumber          string
	// DocumentType retains the observed type without inventing a value.
	DocumentType              string
}

func (v RevisionInput) validate(country string) error {
	if v.DocumentIdentityID <= 0 {
		return reject("revision_identity_required")
	}
	if err := validateDocumentKey(v.RevisionKey, country, true); err != nil {
		return err
	}
	for _, d := range []model.Digest{v.SourceObservationSetHash, v.TextHash, v.MetadataHash, v.ConversionRecipeDigest, v.NormalizationRecipeDigest, v.ParserRecipeDigest, v.PolicyRecipeDigest} {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("revision hash: %w", err)
		}
	}
	return validateText(v.NormalizedNumber, v.DocumentType)
}

func revisionMatch(stored documentdb.SilverDocumentRevision, v RevisionInput, g Grant) bool {
	return stored.DocumentIdentityID == v.DocumentIdentityID && stored.DocumentVersionKey == g.Execution.Scope.Subject.LeaseKey() &&
		stored.RevisionKey == v.RevisionKey && stored.SourceObservationSetHash == string(v.SourceObservationSetHash) &&
		stored.TextHash == string(v.TextHash) && stored.MetadataHash == string(v.MetadataHash) &&
		stored.ConversionRecipeDigest == string(v.ConversionRecipeDigest) && stored.NormalizationRecipeDigest == string(v.NormalizationRecipeDigest) &&
		stored.ParserRecipeDigest == string(v.ParserRecipeDigest) && stored.PolicyRecipeDigest == string(v.PolicyRecipeDigest) &&
		stored.NormalizedNumber == v.NormalizedNumber && stored.DocumentType == v.DocumentType
}

// InsertDocumentRevision inserts once or returns the original revision receipt.
// Any changed immutable field, including document version, is a payload conflict.
func (r *Revisions) InsertDocumentRevision(ctx context.Context, g Grant, v RevisionInput) (documentdb.SilverDocumentRevision, error) {
	var zero documentdb.SilverDocumentRevision
	if err := v.validate(g.Execution.Country); err != nil {
		return zero, err
	}
	return revisionTransaction(ctx, r, g, func(tx pgx.Tx, q documentdb.Querier) (documentdb.SilverDocumentRevision, error) {
		identity, err := q.ReadDocumentIdentity(ctx, strings.Split(v.RevisionKey, ":rev:")[0])
		if err != nil {
			return zero, databaseError("resolve revision identity", err)
		}
		if identity.ID != v.DocumentIdentityID {
			return zero, conflict("revision identity parent")
		}
		stored, err := q.ReadDocumentRevision(ctx, v.RevisionKey)
		if err == nil {
			if !revisionMatch(stored, v, g) {
				return zero, conflict("document revision payload")
			}
			return stored, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, databaseError("read revision receipt", err)
		}
		e := g.Execution
		_, err = q.InsertDocumentRevision(ctx, documentdb.InsertDocumentRevisionParams{
			AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: r.writer, ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(),
			Column5: e.Owner, Column6: e.AttemptFence, ID: g.ExecutionID, InputDigest: string(g.InputDigest), RecipeDigest: string(g.RecipeDigest), BudgetDigest: string(g.BudgetDigest),
			DocumentIdentityID: v.DocumentIdentityID, RevisionKey: v.RevisionKey, SourceObservationSetHash: string(v.SourceObservationSetHash), TextHash: string(v.TextHash), MetadataHash: string(v.MetadataHash),
			ConversionRecipeDigest: string(v.ConversionRecipeDigest), NormalizationRecipeDigest: string(v.NormalizationRecipeDigest), ParserRecipeDigest: string(v.ParserRecipeDigest), PolicyRecipeDigest: string(v.PolicyRecipeDigest),
			NormalizedNumber: v.NormalizedNumber, DocumentType: v.DocumentType})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return zero, databaseError("insert document revision", err)
		}
		if err := checkDocumentLease(ctx, tx, g); err != nil {
			return zero, err
		}
		stored, err = q.ReadDocumentRevision(ctx, v.RevisionKey)
		if err != nil {
			return zero, grantError("resolve revision receipt", err)
		}
		if !revisionMatch(stored, v, g) {
			return zero, conflict("document revision payload")
		}
		return stored, nil
	})
}

// ReadDocumentRevision reads an exact immutable revision under a current grant.
func (r *Revisions) ReadDocumentRevision(ctx context.Context, g Grant, key string) (documentdb.SilverDocumentRevision, error) {
	var zero documentdb.SilverDocumentRevision
	if err := validateDocumentKey(key, g.Execution.Country, true); err != nil {
		return zero, err
	}
	return revisionTransaction(ctx, r, g, func(_ pgx.Tx, q documentdb.Querier) (documentdb.SilverDocumentRevision, error) {
		v, err := q.ReadDocumentRevision(ctx, key)
		if err != nil {
			return v, databaseError("read document revision", err)
		}
		return v, nil
	})
}

// ProvisionInput retains exact citation bytes; hex is used only inside the SQL
// transport. Typed fields exclude string ordinals and non-string hashes. Anchors
// must be a JSON array of objects. Stable UUID and continuity ID occur together.
type ProvisionInput struct {
	// ProvisionKey embeds the parent revision key and LocalKey.
	ProvisionKey          string
	// LocalKey identifies a provision within this document revision.
	LocalKey              string
	// Ordinal is the nonnegative, revision-local source order.
	Ordinal               int64
	// NativeCitation retains arbitrary exact bytes without normalization.
	NativeCitation        []byte
	// SourceAnchors is a bounded JSON array of source-anchor objects.
	SourceAnchors         json.RawMessage
	// TextHash pins the provision's immutable text.
	TextHash              model.Digest
	// StableProvisionUUID requires the confirmed ContinuityAssertionID.
	StableProvisionUUID   *string
	// ContinuityAssertionID must name confirmed continuity for the parent identity.
	ContinuityAssertionID *int64
}

type provisionTransport struct {
	ProvisionKey          string          `json:"provision_key"`
	LocalKey              string          `json:"local_key"`
	Ordinal               int64           `json:"ordinal"`
	NativeCitationHex     string          `json:"native_citation_hex"`
	SourceAnchors         json.RawMessage `json:"source_anchors"`
	TextHash              string          `json:"text_hash"`
	StableProvisionUUID   *string         `json:"stable_provision_uuid"`
	ContinuityAssertionID *int64          `json:"continuity_assertion_id"`
}

func uuidBytes(value string) ([16]byte, error) {
	var result [16]byte
	if len(value) != 36 {
		return result, reject("invalid_stable_uuid")
	}
	if err := model.ValidateRevisionKey("vn:doc:"+value, "vn"); err != nil {
		return result, reject("invalid_stable_uuid")
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	if err != nil {
		return result, reject("invalid_stable_uuid")
	}
	copy(result[:], decoded)
	return result, nil
}

type anchorNumber string

// JSONB compares numeric values rather than their lexical spelling. Normalize
// numbers without float rounding and reject values outside PostgreSQL bounds.
func canonicalAnchor(value any) (any, error) {
	switch v := value.(type) {
	case string:
		if strings.ContainsRune(v, 0) {
			return nil, reject("anchor_nul_character")
		}
	case json.Number:
		s := v.String()
		mantissa, exponent := s, 0
		if index := strings.IndexAny(s, "eE"); index >= 0 {
			mantissa = s[:index]
			var err error
			exponent, err = strconv.Atoi(s[index+1:])
			if err != nil || exponent < -16383 || exponent > 131071 {
				return nil, reject("anchor_numeric_range")
			}
		}
		before, after, _ := strings.Cut(strings.TrimPrefix(mantissa, "-"), ".")
		if len(before)+exponent > 131072 || len(after)-exponent > 16383 {
			return nil, reject("anchor_numeric_range")
		}
		number, ok := new(big.Rat).SetString(s)
		if !ok {
			return nil, reject("anchor_numeric_range")
		}
		return anchorNumber(number.RatString()), nil
	case []any:
		for i, child := range v {
			canonical, err := canonicalAnchor(child)
			if err != nil {
				return nil, err
			}
			v[i] = canonical
		}
	case map[string]any:
		for key, child := range v {
			if strings.ContainsRune(key, 0) {
				return nil, reject("anchor_nul_character")
			}
			canonical, err := canonicalAnchor(child)
			if err != nil {
				return nil, err
			}
			v[key] = canonical
		}
	}
	return value, nil
}

// Validate all lexical values before object decoding discards duplicate keys.
func validateAnchorTokens(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return reject("invalid_anchor_json")
		}
		if _, err := canonicalAnchor(token); err != nil {
			return err
		}
	}
}

func jsonValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, reject("invalid_anchor_json")
	}
	return canonicalAnchor(value)
}

// encoding/json accepts unpaired UTF-16 escapes and substitutes a replacement
// rune. PostgreSQL JSONB rejects them; reject before the SQL transport as well.
func validateAnchorEscapes(raw json.RawMessage) error {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		if i+1 >= len(raw) || raw[i+1] != 'u' {
			i++
			continue
		}
		if i+6 > len(raw) {
			return reject("invalid_anchor_unicode_escape")
		}
		code, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
		if err != nil || (code >= 0xdc00 && code <= 0xdfff) {
			return reject("invalid_anchor_unicode_escape")
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+12 > len(raw) || raw[i+6] != '\\' || raw[i+7] != 'u' {
				return reject("invalid_anchor_unicode_escape")
			}
			low, err := strconv.ParseUint(string(raw[i+8:i+12]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return reject("invalid_anchor_unicode_escape")
			}
			i += 6
		}
		i += 5
	}
	return nil
}

func (r *Revisions) provisionPayload(key, country string, items []ProvisionInput) (json.RawMessage, error) {
	if err := validateDocumentKey(key, country, true); err != nil {
		return nil, err
	}
	if len(items) == 0 || len(items) > r.limits.MaxItems {
		return nil, reject("provision_item_bound")
	}
	seenKeys, seenOrdinals := make(map[string]bool), make(map[int64]bool)
	transport := make([]provisionTransport, 0, len(items))
	// Bound allocations before hex encoding or JSON marshaling. The complete
	// encoded payload is checked again, including JSON escape expansion.
	remaining := r.limits.MaxPayloadBytes
	for _, v := range items {
		if err := model.ValidateKey(v.LocalKey); err != nil {
			return nil, fmt.Errorf("provision local key: %w", err)
		}
		if v.ProvisionKey != key+":provision:"+v.LocalKey {
			return nil, reject("provision_parent_key")
		}
		if v.Ordinal < 0 || v.Ordinal > 2147483647 {
			return nil, reject("provision_ordinal_range")
		}
		if seenKeys[v.ProvisionKey] || seenOrdinals[v.Ordinal] {
			return nil, reject("duplicate_provision_key_or_ordinal")
		}
		seenKeys[v.ProvisionKey], seenOrdinals[v.Ordinal] = true, true
		if err := v.TextHash.Validate(); err != nil {
			return nil, fmt.Errorf("provision text hash: %w", err)
		}
		if len(v.NativeCitation) > r.limits.MaxCitationBytes {
			return nil, reject("provision_citation_bound")
		}
		if len(v.SourceAnchors) > r.limits.MaxAnchorBytes {
			return nil, reject("provision_anchor_byte_bound")
		}
		if !utf8.Valid(v.SourceAnchors) || !json.Valid(v.SourceAnchors) {
			return nil, reject("invalid_anchor_json")
		}
		if err := validateAnchorEscapes(v.SourceAnchors); err != nil {
			return nil, err
		}
		if err := validateAnchorTokens(v.SourceAnchors); err != nil {
			return nil, err
		}
		anchors, err := jsonValue(v.SourceAnchors)
		if err != nil {
			return nil, err
		}
		array, ok := anchors.([]any)
		if !ok || len(array) > r.limits.MaxAnchors {
			return nil, reject("provision_anchor_array_bound")
		}
		for _, anchor := range array {
			if _, ok := anchor.(map[string]any); !ok {
				return nil, reject("provision_anchor_object_required")
			}
		}
		if (v.StableProvisionUUID != nil) != (v.ContinuityAssertionID != nil) {
			return nil, reject("provision_continuity_pair")
		}
		if v.StableProvisionUUID != nil {
			if _, err := uuidBytes(*v.StableProvisionUUID); err != nil {
				return nil, err
			}
			if *v.ContinuityAssertionID <= 0 {
				return nil, reject("positive_continuity_id_required")
			}
		}
		for _, n := range []int{len(v.ProvisionKey), len(v.LocalKey), len(v.TextHash), len(v.SourceAnchors), len(v.NativeCitation), len(v.NativeCitation)} {
			if n > remaining {
				return nil, reject("provision_payload_bound")
			}
			remaining -= n
		}
		transport = append(transport, provisionTransport{v.ProvisionKey, v.LocalKey, v.Ordinal, hex.EncodeToString(v.NativeCitation), v.SourceAnchors,
			string(v.TextHash), v.StableProvisionUUID, v.ContinuityAssertionID})
	}
	payload, err := json.Marshal(transport)
	if err != nil {
		return nil, reject("invalid_provision_transport")
	}
	if len(payload) > r.limits.MaxPayloadBytes {
		return nil, reject("provision_payload_bound")
	}
	return payload, nil
}

func provisionMatch(stored documentdb.ReadProvisionRevisionsRow, v ProvisionInput) bool {
	if stored.ProvisionKey != v.ProvisionKey || stored.LocalKey != v.LocalKey || int64(stored.Ordinal) != v.Ordinal ||
		!bytes.Equal(stored.NativeCitation, v.NativeCitation) || stored.TextHash != string(v.TextHash) ||
		!reflect.DeepEqual(stored.ContinuityAssertionID, v.ContinuityAssertionID) || stored.StableProvisionUuid.Valid != (v.StableProvisionUUID != nil) {
		return false
	}
	if v.StableProvisionUUID != nil {
		value, err := uuidBytes(*v.StableProvisionUUID)
		if err != nil || value != stored.StableProvisionUuid.Bytes {
			return false
		}
	}
	a, errA := jsonValue(stored.SourceAnchors)
	b, errB := jsonValue(v.SourceAnchors)
	return errA == nil && errB == nil && reflect.DeepEqual(a, b)
}

func classifyProvisions(rows []documentdb.ReadProvisionRevisionsRow, items []ProvisionInput) ([]documentdb.ReadProvisionRevisionsRow, bool, error) {
	byKey := make(map[string]documentdb.ReadProvisionRevisionsRow, len(rows))
	byOrdinal := make(map[int32]string, len(rows))
	byLocal := make(map[string]string, len(rows))
	for _, row := range rows {
		byKey[row.ProvisionKey], byOrdinal[row.Ordinal], byLocal[row.LocalKey] = row, row.ProvisionKey, row.ProvisionKey
	}
	result := make([]documentdb.ReadProvisionRevisionsRow, 0, len(items))
	for _, item := range items {
		row, exists := byKey[item.ProvisionKey]
		if exists {
			if !provisionMatch(row, item) {
				return nil, false, conflict("provision revision payload")
			}
			result = append(result, row)
		} else if byOrdinal[int32(item.Ordinal)] != "" || byLocal[item.LocalKey] != "" {
			return nil, false, conflict("provision ordinal or local key")
		}
	}
	return result, len(result) == len(items), nil
}

func provisionReceipts(ctx context.Context, q documentdb.Querier, parentID int64, items []ProvisionInput) ([]documentdb.ReadProvisionRevisionsRow, error) {
	params := documentdb.ReadProvisionReceiptsParams{DocumentRevisionID: parentID,
		ProvisionKeys: make([]string, len(items)), LocalKeys: make([]string, len(items)), Ordinals: make([]int32, len(items))}
	for i, item := range items {
		params.ProvisionKeys[i], params.LocalKeys[i], params.Ordinals[i] = item.ProvisionKey, item.LocalKey, int32(item.Ordinal)
	}
	rows, err := q.ReadProvisionReceipts(ctx, params)
	if err != nil {
		return nil, err
	}
	result := make([]documentdb.ReadProvisionRevisionsRow, len(rows))
	for i, row := range rows {
		result[i] = documentdb.ReadProvisionRevisionsRow(row)
	}
	return result, nil
}

var errBulkEmpty = errors.New("bulk result requires rollback")

// InsertProvisionRevisions inserts a bounded batch for one document revision.
// An empty SQL result rolls back before a fresh transaction reads or retries.
// Returned rows are the batch receipts in input order, including original IDs.
func (r *Revisions) InsertProvisionRevisions(ctx context.Context, g Grant, key string, items []ProvisionInput) ([]documentdb.ReadProvisionRevisionsRow, error) {
	payload, err := r.provisionPayload(key, g.Execution.Country, items)
	if err != nil {
		return nil, err
	}
	// Retry once after rollback. A final read-only transaction checks the grant
	// and classifies the last empty result without making another insertion.
	for attempt := 0; attempt < 3; attempt++ {
		rows, err := revisionTransaction(ctx, r, g, func(tx pgx.Tx, q documentdb.Querier) ([]documentdb.ReadProvisionRevisionsRow, error) {
			parent, err := q.ReadDocumentRevision(ctx, key)
			if err != nil {
				return nil, databaseError("resolve provision parent", err)
			}
			if parent.DocumentVersionKey != g.Execution.Scope.Subject.LeaseKey() {
				return nil, fmt.Errorf("provision parent version: %w", run.ErrStaleGrant)
			}
			// RAW QUERY: ResolveConfirmedProvisionContinuity. A stable UUID
			// requires a retained confirmed assertion for this parent identity.
			for _, item := range items {
				if item.ContinuityAssertionID == nil {
					continue
				}
				var id int64
				err := tx.QueryRow(ctx, `SELECT a.id FROM silver.identity_assertion a WHERE a.id = $1
					AND a.kind = 'continuity' AND a.review_state = 'confirmed' AND a.document_identity_id = $2`,
					*item.ContinuityAssertionID, parent.DocumentIdentityID).Scan(&id)
				if errors.Is(err, pgx.ErrNoRows) {
					return nil, reject("confirmed_parent_continuity_required")
				}
				if err != nil {
					return nil, databaseError("resolve provision continuity", err)
				}
			}
			prior, err := provisionReceipts(ctx, q, parent.ID, items)
			if err != nil {
				return nil, databaseError("read provision receipts", err)
			}
			result, complete, err := classifyProvisions(prior, items)
			if err != nil || complete {
				return result, err
			}
			if attempt == 2 {
				return nil, conflict("bulk provision winner unresolved after rollback")
			}
			e := g.Execution
			_, err = q.InsertProvisionRevisions(ctx, documentdb.InsertProvisionRevisionsParams{
				AdmissionEpoch: e.AdmissionEpoch, AdmittedWriter: r.writer, ResourceKind: "document_version", ResourceKey: e.Scope.Subject.LeaseKey(),
				Column5: e.Owner, Column6: e.AttemptFence, ID: g.ExecutionID, InputDigest: string(g.InputDigest), RecipeDigest: string(g.RecipeDigest), BudgetDigest: string(g.BudgetDigest),
				ID_2: parent.ID, Column12: payload})
			if errors.Is(err, pgx.ErrNoRows) {
				// No statement, not even a grant read, may follow this result in
				// this transaction. The transaction wrapper rolls it back.
				return nil, errBulkEmpty
			}
			if err != nil {
				return nil, databaseError("insert provision revisions", err)
			}
			if err := checkDocumentLease(ctx, tx, g); err != nil {
				return nil, err
			}
			prior, err = provisionReceipts(ctx, q, parent.ID, items)
			if err != nil {
				return nil, databaseError("resolve provision receipts", err)
			}
			result, complete, err = classifyProvisions(prior, items)
			if err != nil {
				return nil, err
			}
			if !complete {
				return nil, errBulkEmpty
			}
			return result, nil
		})
		if !errors.Is(err, errBulkEmpty) {
			return rows, err
		}
	}
	return nil, conflict("bulk provision winner unresolved after rollback")
}

// MaxProvisionPageItems bounds a public read regardless of retained parent size.
const MaxProvisionPageItems int32 = 100

// ReadProvisionRevisions reads at most limit receipts in ordinal order.
// Start with afterOrdinal = -1; pass the last returned Ordinal as the next cursor.
// An empty page ends traversal. Each page independently validates the grant.
// Concurrent appends below the cursor require a new traversal to observe them.
func (r *Revisions) ReadProvisionRevisions(ctx context.Context, g Grant, key string, afterOrdinal, limit int32) ([]documentdb.ReadProvisionRevisionsRow, error) {
	if afterOrdinal < -1 || limit <= 0 || limit > MaxProvisionPageItems {
		return nil, reject("provision_page_bound")
	}
	if err := validateDocumentKey(key, g.Execution.Country, true); err != nil {
		return nil, err
	}
	return revisionTransaction(ctx, r, g, func(_ pgx.Tx, q documentdb.Querier) ([]documentdb.ReadProvisionRevisionsRow, error) {
		parent, err := q.ReadDocumentRevision(ctx, key)
		if err != nil {
			return nil, databaseError("read provision parent", err)
		}
		rows, err := q.ReadProvisionRevisionsPage(ctx, documentdb.ReadProvisionRevisionsPageParams{
			DocumentRevisionID: parent.ID, AfterOrdinal: afterOrdinal, PageLimit: limit})
		if err != nil {
			return nil, databaseError("read provision revisions", err)
		}
		result := make([]documentdb.ReadProvisionRevisionsRow, len(rows))
		for i, row := range rows {
			result[i] = documentdb.ReadProvisionRevisionsRow(row)
		}
		return result, nil
	})
}
