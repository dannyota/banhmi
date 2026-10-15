package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

// ConversionRegistration freezes a root, staged dependencies and complete direct
// logical references. Catalog deduplication never rewrites these values.
type ConversionRegistration struct {
	// Manifest is the retained root object.
	Manifest blob.Metadata

	// Members supplies frozen metadata for staged direct dependencies.
	Members []blob.Metadata

	// MemberRefs is the complete direct dependency set, including cataloged
	// dependencies and logical aliases.
	MemberRefs []model.AssetRef
}

type catalogRegistrationPayload struct {
	SchemaVersion string
	Request       run.IntakeApplyRequest
	Registration  ConversionRegistration
}

// CatalogError identifies invalid input, integrity loss or exhausted contention.
// Cause preserves cancellation and database errors without including object data
// in the diagnostic. Grant and replay failures retain run's error classes.
type CatalogError struct {
	// Kind is a payload-free failure class.
	Kind string

	// Cause preserves the originating error without adding it to Error's text.
	Cause error
}

// Error returns a diagnostic containing no document bytes or storage paths.
func (e *CatalogError) Error() string { return "conversion catalog: " + e.Kind }

// Unwrap preserves the underlying failure for errors.Is and errors.As.
func (e *CatalogError) Unwrap() error { return e.Cause }

// Is compares catalog failure classes independently of their causes.
func (e *CatalogError) Is(target error) bool {
	other, ok := target.(*CatalogError)
	return ok && e.Kind == other.Kind
}

var (
	// ErrCatalogInvalid identifies malformed or over-budget registrations.
	ErrCatalogInvalid = &CatalogError{Kind: "invalid registration"}
	// ErrCatalogIntegrity identifies missing protection or inconsistent bytes.
	ErrCatalogIntegrity = &CatalogError{Kind: "integrity failure"}
	// ErrCatalogContention identifies exhausted defensive rediscovery attempts.
	ErrCatalogContention = &CatalogError{Kind: "contention exhausted"}
)

var errCatalogRediscover = errors.New("catalog rediscovery required")

// ConversionCatalog registers retained objects and execution edges atomically.
// Configure trusted limits before use. Object readers obey NewLedger's
// cancellable Read and Close contract.
type ConversionCatalog struct {
	ledger *Ledger
	limits formats.Limits
}

// NewConversionCatalog constructs an adapter without I/O. Limits are trusted
// configuration; formats.DefaultLimits supplies the proposed unmeasured values.
func NewConversionCatalog(db TransactionStarter, writer string, objects blob.Store, limits formats.Limits) (*ConversionCatalog, error) {
	if err := limits.Validate(); err != nil {
		return nil, &CatalogError{Kind: ErrCatalogInvalid.Kind, Cause: err}
	}
	if objects == nil {
		return nil, fmt.Errorf("catalog object store is required")
	}
	l, err := NewLedger(db, writer, objects, nil)
	if err != nil {
		return nil, err
	}
	return &ConversionCatalog{ledger: l, limits: limits}, nil
}

func catalogRequest(w run.IntakeWork, root string, outputs []model.AssetRef) run.IntakeApplyRequest {
	return run.IntakeApplyRequest{
		Work: w, IdempotencyKey: run.IntakeIdempotencyKey(w),
		Outputs: outputs, OutputDigest: model.Digest(root),
		Outcome: run.OutcomeSucceeded,
		Counts:  run.Counts{Expected: 1, Processed: 1, Succeeded: 1},
	}
}

func validateCatalogWork(w run.IntakeWork, root string) error {
	if err := w.Validate(); err != nil {
		return err
	}
	if w.Execution.WorkflowKind != run.DocumentIngest || w.Execution.Scope.Kind != run.ScopeIntake ||
		w.Execution.Scope.Subject.Kind != run.SubjectDocumentVersion || w.Stage != run.Normalize ||
		w.Substep != "conversion-catalog" || w.Shard != root || w.DecisionRevision != 0 {
		return fmt.Errorf("catalog requires decision-free document normalization work")
	}
	return nil
}

// normalizeCatalogRegistration copies inputs before sorting. Duplicate identical
// metadata collapses; logical aliases remain part of immutable replay identity.
func normalizeCatalogRegistration(w run.IntakeWork, input ConversionRegistration, limits formats.Limits) (ConversionRegistration, []string, map[string]int64, map[string]blob.Metadata, error) {
	r := input
	invalid := func() (ConversionRegistration, []string, map[string]int64, map[string]blob.Metadata, error) {
		return r, nil, nil, nil, ErrCatalogInvalid
	}
	if err := validateCatalogWork(w, r.Manifest.SHA256); err != nil {
		return r, nil, nil, nil, &CatalogError{Kind: ErrCatalogInvalid.Kind, Cause: err}
	}
	if int64(len(r.Members)) > limits.MaxRows || int64(len(r.MemberRefs)) > limits.MaxRows ||
		int64(len(r.Members))+int64(len(r.MemberRefs))+1 > limits.MaxRows {
		return invalid()
	}
	r.Members = append([]blob.Metadata{}, input.Members...)
	r.MemberRefs = append([]model.AssetRef{}, input.MemberRefs...)
	sizes := map[string]int64{}
	metadata := map[string]blob.Metadata{}
	paths := map[string]string{}
	var stringBytes int64
	addPath := func(path, hash string) bool {
		if previous, ok := paths[path]; ok && previous != hash {
			return false
		}
		paths[path] = hash
		stringBytes += int64(len(path) + len(hash))
		return stringBytes <= limits.MaxJSONBytes
	}
	addMetadata := func(m blob.Metadata) bool {
		if m.Validate() != nil {
			return false
		}
		switch m.CaptureSource {
		case "candidate", "conversion", "quality", "recipe", "decision":
		default:
			return false
		}
		ref := model.AssetRef{Path: m.ObjectRef, SHA256: model.Digest(m.SHA256), ByteSize: m.ByteSize}
		if ref.Validate() != nil || !addPath(ref.Path, m.SHA256) {
			return false
		}
		stringBytes += int64(len(m.MediaType) + len(m.CaptureSource) + len(m.CaptureRef))
		if stringBytes > limits.MaxJSONBytes {
			return false
		}
		if prior, ok := metadata[m.SHA256]; ok {
			return catalogMetadataEqual(prior, m)
		}
		metadata[m.SHA256] = m
		return true
	}
	if !addMetadata(r.Manifest) {
		return invalid()
	}
	sizes[r.Manifest.SHA256] = r.Manifest.ByteSize
	for _, ref := range r.MemberRefs {
		hash := string(ref.SHA256)
		if ref.Validate() != nil || hash == r.Manifest.SHA256 || !addPath(ref.Path, hash) {
			return invalid()
		}
		if size, exists := sizes[hash]; exists && size != ref.ByteSize {
			return invalid()
		}
		sizes[hash] = ref.ByteSize
	}
	for _, m := range r.Members {
		size, exists := sizes[m.SHA256]
		if !exists || m.SHA256 == r.Manifest.SHA256 || size != m.ByteSize || !addMetadata(m) {
			return invalid()
		}
	}
	// Each distinct object can produce one catalog row and one execution edge.
	// Admission, lease, execution and result contribute four fixed rows.
	if int64(len(sizes)) > limits.MaxBlobs || len(sizes) > w.Budget.MaxItems || int64(len(sizes))*2+4 > limits.MaxRows {
		return invalid()
	}
	total := int64(0)
	hashes := make([]string, 0, len(sizes))
	for hash, size := range sizes {
		if size > limits.MaxObjectBytes-total || size > w.Budget.MaxInputBytes-total {
			return invalid()
		}
		total += size
		hashes = append(hashes, hash)
	}
	sort.Strings(hashes)
	sort.Slice(r.Members, func(i, j int) bool { return r.Members[i].SHA256 < r.Members[j].SHA256 })
	members := r.Members[:0]
	for _, m := range r.Members {
		if len(members) == 0 || members[len(members)-1].SHA256 != m.SHA256 {
			members = append(members, m)
		}
	}
	r.Members = members
	sort.Slice(r.MemberRefs, func(i, j int) bool {
		if r.MemberRefs[i].SHA256 != r.MemberRefs[j].SHA256 {
			return r.MemberRefs[i].SHA256 < r.MemberRefs[j].SHA256
		}
		return r.MemberRefs[i].Path < r.MemberRefs[j].Path
	})
	// Bound the local result envelope before opening a transaction. Physical
	// references get the same independent bound after catalog resolution.
	refs := []model.AssetRef{{Path: r.Manifest.ObjectRef, SHA256: model.Digest(r.Manifest.SHA256), ByteSize: r.Manifest.ByteSize}}
	refs = append(refs, r.MemberRefs...)
	if _, err := encodeCatalogPayload(catalogRegistrationPayload{"catalog-registration-v1", catalogRequest(w, r.Manifest.SHA256, refs), r}, limits); err != nil {
		return r, nil, nil, nil, err
	}
	return r, hashes, sizes, metadata, nil
}

func catalogMetadataEqual(a, b blob.Metadata) bool {
	return a.SHA256 == b.SHA256 && a.ByteSize == b.ByteSize && a.MediaType == b.MediaType &&
		a.ObjectRef == b.ObjectRef && a.CaptureSource == b.CaptureSource &&
		a.CaptureRef == b.CaptureRef && a.CapturedAt.Format(time.RFC3339Nano) == b.CapturedAt.Format(time.RFC3339Nano)
}

func encodeCatalogPayload(p catalogRegistrationPayload, limits formats.Limits) ([]byte, error) {
	remaining := limits.MaxJSONBytes
	if !catalogStringBound(reflect.ValueOf(p), &remaining) {
		return nil, ErrCatalogInvalid
	}
	raw, err := json.Marshal(p)
	if err != nil || int64(len(raw)) > limits.MaxJSONBytes {
		return nil, &CatalogError{Kind: ErrCatalogInvalid.Kind, Cause: err}
	}
	return raw, nil
}

func catalogStringBound(v reflect.Value, remaining *int64) bool {
	switch v.Kind() {
	case reflect.String:
		if int64(v.Len()) > *remaining {
			return false
		}
		*remaining -= int64(v.Len())
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).PkgPath == "" && !catalogStringBound(v.Field(i), remaining) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !catalogStringBound(v.Index(i), remaining) {
				return false
			}
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return catalogStringBound(v.Elem(), remaining)
		}
	}
	return true
}

func decodeCatalogPayload(raw []byte) (catalogRegistrationPayload, error) {
	var p catalogRegistrationPayload
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return p, ErrCatalogIntegrity
	}
	if p.SchemaVersion != "catalog-registration-v1" || p.Request.Validate() != nil {
		return p, ErrCatalogIntegrity
	}
	// Decode uses structural bounds from the frozen request rather than the
	// caller's current configuration. Registration is independently checked at
	// the call boundary under the trusted limits.
	limits := formats.Limits{MaxBlobs: 1024, MaxRows: int64(len(raw)) + 1,
		MaxJSONBytes: int64(len(raw))*4 + 1, MaxObjectBytes: p.Request.Work.Budget.MaxInputBytes, MaxElapsedMS: 1}
	normalized, hashes, sizes, _, err := normalizeCatalogRegistration(p.Request.Work, p.Registration, limits)
	if err != nil || !sameCatalogRegistration(normalized, p.Registration) ||
		p.Request.OutputDigest != model.Digest(p.Registration.Manifest.SHA256) ||
		p.Request.Outcome != run.OutcomeSucceeded || p.Request.Complete ||
		p.Request.Counts != (run.Counts{Expected: 1, Processed: 1, Succeeded: 1}) ||
		p.Request.Cursor != "" || len(p.Request.Outputs) != len(hashes) {
		return p, ErrCatalogIntegrity
	}
	if !run.SameImmutableIntakePayload(p.Request, catalogRequest(p.Request.Work, p.Registration.Manifest.SHA256, p.Request.Outputs)) {
		return p, ErrCatalogIntegrity
	}
	for i, ref := range p.Request.Outputs {
		if string(ref.SHA256) != hashes[i] || ref.ByteSize != sizes[hashes[i]] {
			return p, ErrCatalogIntegrity
		}
	}
	return p, nil
}

func sameCatalogRegistration(a, b ConversionRegistration) bool {
	// JSON drops monotonic clock state while retaining timestamp encoding,
	// paths, aliases and the complete dependency set.
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func classifyCatalogPayload(stored catalogRegistrationPayload, work run.IntakeWork, registration ConversionRegistration) error {
	if !sameCatalogRegistration(stored.Registration, registration) {
		return run.ErrPayloadConflict
	}
	proposed := stored.Request
	proposed.Work = work
	proposed.IdempotencyKey = run.IntakeIdempotencyKey(work)
	raw, err := json.Marshal(proposed)
	if err != nil {
		return err
	}
	var canonical run.IntakeApplyRequest
	if err := json.Unmarshal(raw, &canonical); err != nil {
		return err
	}
	return run.ValidateIntakeReplay(stored.Request, canonical, true)
}

// catalogReceiptFromResult reads original execution and transport provenance
// and decodes the nested Request instead of ledger's flat result format.
func catalogReceiptFromResult(ctx context.Context, tx pgx.Tx, result intakedb.IngestExecutionResult) (run.IntakeReceipt, error) {
	p, err := decodeCatalogPayload(result.Payload)
	if err != nil {
		return run.IntakeReceipt{}, err
	}
	r := p.Request
	if result.IdempotencyKey != r.IdempotencyKey || result.Stage != string(r.Work.Stage) ||
		result.Substep != r.Work.Substep || result.Shard != r.Work.Shard ||
		result.InputDigest != string(r.Work.InputDigest) || result.OutputDigest != string(r.OutputDigest) ||
		result.RecipeDigest != string(r.Work.RecipeDigest) || result.BudgetDigest != string(r.Work.BudgetDigest) ||
		result.DecisionRevision != 0 || result.Outcome != string(run.OutcomeSucceeded) ||
		result.Expected != 1 || result.Processed != 1 || result.Succeeded != 1 ||
		result.PendingRetry != 0 || result.TerminalGap != 0 || result.Rejected != 0 ||
		result.Failed != 0 || result.AcceptedGaps != 0 || result.Cursor != "" ||
		result.NextEligibleAt != nil || result.GapAcceptanceRef != nil || result.Complete {
		return run.IntakeReceipt{}, ErrCatalogIntegrity
	}
	provenance, err := intakedb.New(tx).ReadCatalogResultExecution(ctx, result.ExecutionID)
	if err != nil {
		return run.IntakeReceipt{}, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
	}
	var runs []string
	if err := json.Unmarshal(provenance.RunReceipts, &runs); err != nil {
		return run.IntakeReceipt{}, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
	}
	receipt := run.IntakeReceipt{ExecutionID: provenance.ExecutionKey, ResultID: result.ResultKey, Result: p.Request}
	if len(runs) > 0 {
		receipt.RunReceipt = runs[0]
	}
	return receipt, nil
}

// RegisterConversionOutputs registers one complete bounded object set under a
// live document lease. Exact replay verifies bytes and original protection,
// inserts nothing, and returns the original receipt even after lease reclaim.
func (c *ConversionCatalog) RegisterConversionOutputs(ctx context.Context, work run.IntakeWork, registration ConversionRegistration) (run.IntakeReceipt, error) {
	started := time.Now()
	r, hashes, sizes, metadata, err := normalizeCatalogRegistration(work, registration, c.limits)
	if err != nil {
		return run.IntakeReceipt{}, err
	}
	elapsed := work.Budget.MaxElapsed
	if ms := c.limits.MaxElapsedMS; ms <= int64(elapsed/time.Millisecond) {
		elapsed = time.Duration(ms) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, elapsed-time.Since(started))
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		receipt, err := c.register(ctx, work, r, hashes, sizes, metadata)
		if !catalogRetryOnly(err) || ctx.Err() != nil {
			return receipt, err
		}
	}
	return run.IntakeReceipt{}, ErrCatalogContention
}

func catalogRetryOnly(err error) bool {
	if err == errCatalogRediscover {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !catalogRetryOnly(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return catalogRetryOnly(wrapped.Unwrap())
	}
	return false
}

func (c *ConversionCatalog) register(ctx context.Context, work run.IntakeWork, registration ConversionRegistration, hashes []string, sizes map[string]int64, metadata map[string]blob.Metadata) (run.IntakeReceipt, error) {
	return ledgerTransaction(ctx, c.ledger, func(tx pgx.Tx, q intakedb.Querier) (receipt run.IntakeReceipt, cause error) {
		leaseHeld := false
		// Business errors after any wait receive a final grant check. An
		// aborted transaction or cancellation must issue no more statements.
		defer func() {
			if !leaseHeld {
				return
			}
			var sqlError interface{ SQLState() string }
			if ctx.Err() != nil {
				cause = errors.Join(cause, ctx.Err())
				return
			}
			if cause != nil && errors.As(cause, &sqlError) {
				return
			}
			if _, err := lockLease(ctx, tx, work.Execution); err != nil {
				receipt, cause = run.IntakeReceipt{}, err
			}
		}()
		if err := c.ledger.lockAdmission(ctx, tx, work.Execution); err != nil {
			return receipt, err
		}
		if _, err := lockLease(ctx, tx, work.Execution); err != nil {
			return receipt, err
		}
		leaseHeld = true
		executionID, executionOwner, err := resolveExecution(ctx, tx, work)
		if err != nil {
			return receipt, err
		}
		stored, lookupErr := q.FindExecutionResult(ctx, run.IntakeIdempotencyKey(work))
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return receipt, lookupErr
		}
		if _, err := lockLease(ctx, tx, work.Execution); err != nil {
			return receipt, err
		}
		replay := lookupErr == nil
		if replay {
			// PostgreSQL jsonb adds separator spaces. Bound that representation
			// independently at twice the compact budget, without overflowing.
			databaseBytes := int64(len(stored.Payload))
			if databaseBytes > c.limits.MaxJSONBytes && databaseBytes-c.limits.MaxJSONBytes > c.limits.MaxJSONBytes {
				return receipt, ErrCatalogIntegrity
			}
			p, err := decodeCatalogPayload(stored.Payload)
			if err != nil {
				return receipt, err
			}
			// Use the frozen Go envelope on both fresh and replay paths.
			if _, err := encodeCatalogPayload(p, c.limits); err != nil {
				return receipt, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
			}
			if err := classifyCatalogPayload(p, work, registration); err != nil {
				return receipt, err
			}
			original, err := catalogReceiptFromResult(ctx, tx, stored)
			if err != nil {
				return receipt, err
			}
			executionOwner = original.ExecutionID
		}
		if err := q.AcquireBlobCatalogGuard(ctx); err != nil {
			return receipt, err
		}
		if _, err := lockLease(ctx, tx, work.Execution); err != nil {
			return receipt, err
		}
		ids := make(map[string]int64, len(hashes))
		var missing []string
		for _, hash := range hashes {
			id, err := q.LockCatalogBlob(ctx, hash)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return receipt, err
			}
			if _, grantErr := lockLease(ctx, tx, work.Execution); grantErr != nil {
				return receipt, grantErr
			}
			if errors.Is(err, pgx.ErrNoRows) {
				if _, staged := metadata[hash]; !staged || replay {
					return receipt, ErrCatalogIntegrity
				}
				missing = append(missing, hash)
			} else {
				ids[hash] = id
			}
		}
		for _, hash := range missing {
			m := metadata[hash]
			id, err := q.InsertCatalogBlob(ctx, intakedb.InsertCatalogBlobParams{
				Sha256: m.SHA256, ByteSize: m.ByteSize, MediaType: m.MediaType,
				ObjectRef: m.ObjectRef, CaptureSource: m.CaptureSource, CaptureRef: m.CaptureRef, CapturedAt: m.CapturedAt,
			})
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return receipt, err
			}
			if _, grantErr := lockLease(ctx, tx, work.Execution); grantErr != nil {
				return receipt, grantErr
			}
			if errors.Is(err, pgx.ErrNoRows) {
				return receipt, errCatalogRediscover
			}
			ids[hash] = id
		}
		outputs := make([]model.AssetRef, 0, len(hashes))
		physical := map[string]string{}
		for _, hash := range hashes {
			row, err := q.ReadSourceBlob(ctx, hash)
			if err != nil {
				return receipt, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
			}
			if row.ID != ids[hash] || row.ByteSize != sizes[hash] {
				return receipt, ErrCatalogIntegrity
			}
			if prior, exists := physical[row.ObjectRef]; exists && prior != hash {
				return receipt, ErrCatalogIntegrity
			}
			ref := model.AssetRef{Path: row.ObjectRef, SHA256: model.Digest(hash), ByteSize: row.ByteSize}
			if ref.Validate() != nil || int64(len(ref.Path)) > c.limits.MaxJSONBytes {
				return receipt, ErrCatalogIntegrity
			}
			physical[ref.Path] = hash
			outputs = append(outputs, ref)
		}
		request := catalogRequest(work, registration.Manifest.SHA256, outputs)
		if err := request.Validate(); err != nil {
			return receipt, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
		}
		payload, err := encodeCatalogPayload(catalogRegistrationPayload{"catalog-registration-v1", request, registration}, c.limits)
		if err != nil {
			return receipt, err
		}
		for _, ref := range outputs {
			if err := c.ledger.verifyOutputs(ctx, []model.AssetRef{ref}); err != nil {
				return receipt, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
			}
			if _, err := lockLease(ctx, tx, work.Execution); err != nil {
				return receipt, err
			}
		}
		if replay {
			p, err := decodeCatalogPayload(stored.Payload)
			if err != nil || !reflect.DeepEqual(p.Request.Outputs, outputs) {
				return receipt, ErrCatalogIntegrity
			}
		}
		for _, hash := range hashes {
			if !replay {
				_, err := q.AddBlobRetentionReference(ctx, intakedb.AddBlobRetentionReferenceParams{ID: ids[hash], OwnerKind: "execution", OwnerKey: executionOwner})
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return receipt, err
				}
			}
			_, err := q.FindCatalogExecutionEdge(ctx, intakedb.FindCatalogExecutionEdgeParams{BlobID: ids[hash], OwnerKey: executionOwner})
			if err != nil {
				return receipt, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
			}
		}
		if !replay {
			p, err := resultParams(request, c.ledger.writer, executionID)
			if err != nil {
				return receipt, err
			}
			p.Payload = payload
			inserted, insertErr := q.InsertExecutionResult(ctx, p)
			if insertErr != nil && !errors.Is(insertErr, pgx.ErrNoRows) {
				return receipt, insertErr
			}
			stored, err = q.FindExecutionResult(ctx, request.IdempotencyKey)
			if err != nil {
				return receipt, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
			}
			if _, err := lockLease(ctx, tx, work.Execution); err != nil {
				return receipt, err
			}
			if insertErr != nil || inserted.ResultKey != p.ResultKey {
				return receipt, errCatalogRediscover
			}
		}
		// Even fresh calls decode a fresh result read, never a caller-built
		// receipt. Replay likewise detects a damaged result before commit.
		stored, err = q.FindExecutionResult(ctx, request.IdempotencyKey)
		if err != nil {
			return receipt, &CatalogError{Kind: ErrCatalogIntegrity.Kind, Cause: err}
		}
		return catalogReceiptFromResult(ctx, tx, stored)
	})
}
