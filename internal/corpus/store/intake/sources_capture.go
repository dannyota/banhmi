package intake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/corpus/store/intake/intakedb"
)

func validateCapture(work run.IntakeWork, w intakedb.IngestSourceWindow, c model.CaptureRef) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("capture receipt: %w", err)
	}
	if err := model.ValidateEnvelopeKey(c.CaptureKey, "capture"); err != nil {
		return fmt.Errorf("capture key: %w", err)
	}
	if c.Country != work.Execution.Country || c.SourceID != w.SourceID {
		return fmt.Errorf("capture crosses country or source")
	}
	b, err := windowBudget(w)
	if err != nil {
		return err
	}
	if c.Asset.ByteSize > b.MaxInputBytes || c.Asset.ByteSize > work.Budget.MaxInputBytes || c.TransportReceipt.ByteSize > work.Budget.MaxInputBytes {
		return fmt.Errorf("capture exceeds frozen byte allowance")
	}
	return nil
}

func captureMatches(key, digest *string, size *int64, receipt json.RawMessage, capture model.CaptureRef) bool {
	encoded, err := sourceJSON(capture)
	return err == nil && key != nil && digest != nil && size != nil && *key == capture.CaptureKey && *digest == string(capture.Asset.SHA256) && *size == capture.Asset.ByteSize && sameSourceJSON(receipt, encoded)
}

func sourceObservation(decoded api.DecodedObservation) (model.Observation, error) {
	if err := decoded.Validate(); err != nil {
		return model.Observation{}, fmt.Errorf("decoded observation: %w", err)
	}
	metadata, err := sourceDigest(decoded.Fields)
	if err != nil {
		return model.Observation{}, err
	}
	tree, err := sourceDigest(decoded.SourceTree)
	if err != nil {
		return model.Observation{}, err
	}
	relations, err := sourceDigest(decoded.Relations)
	if err != nil {
		return model.Observation{}, err
	}
	digest, err := sourceDigest(decoded)
	if err != nil {
		return model.Observation{}, err
	}
	return model.Observation{SourceID: decoded.SourceID, ExternalID: decoded.ExternalID, VersionKey: decoded.VersionKey, ObservationHash: model.Digest(digest), MetadataHash: model.Digest(metadata), SourceTreeHash: model.Digest(tree), RelationsHash: model.Digest(relations), Capture: decoded.Capture, Ordinal: decoded.Ordinal}, nil
}

// insertSourceRecords orders the first Bronze table before any blob insert.
func insertSourceRecords(ctx context.Context, q intakedb.Querier, observations []api.DecodedObservation) error {
	ordered := append([]api.DecodedObservation(nil), observations...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].SourceID != ordered[j].SourceID {
			return ordered[i].SourceID < ordered[j].SourceID
		}
		return ordered[i].ExternalID < ordered[j].ExternalID
	})
	for _, o := range ordered {
		_, err := q.InsertSourceRecord(ctx, intakedb.InsertSourceRecordParams{SourceID: o.SourceID, ExternalID: o.ExternalID})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("insert source record: %w", err)
		}
	}
	return nil
}

// protectSourceCapture collects all owners before inserting retention edges.
// Blob identity order can differ from hash order for existing catalog rows.
func protectSourceCapture(ctx context.Context, tx pgx.Tx, q intakedb.Querier, capture model.CaptureRef, executionKey string, observationKeys []string) error {
	var edges []intakedb.AddBlobRetentionReferenceParams
	for _, ref := range []model.AssetRef{capture.Asset, capture.TransportReceipt} {
		b, err := q.ReadSourceBlob(ctx, string(ref.SHA256))
		if err != nil {
			return grantError("resolve retention blob", err)
		}
		if b.ByteSize != ref.ByteSize {
			return fmt.Errorf("retention blob payload: %w", run.ErrPayloadConflict)
		}
		edges = append(edges, intakedb.AddBlobRetentionReferenceParams{ID: b.ID, OwnerKind: string(model.RetentionOwnerExecution), OwnerKey: executionKey})
		for _, key := range observationKeys {
			edges = append(edges, intakedb.AddBlobRetentionReferenceParams{ID: b.ID, OwnerKind: string(model.RetentionOwnerObservationRevision), OwnerKey: key})
		}
	}
	return insertSourceRetentionBatch(ctx, tx, q, edges)
}

func insertSourceRetentionBatch(ctx context.Context, tx pgx.Tx, q intakedb.Querier, edges []intakedb.AddBlobRetentionReferenceParams) error {
	ordered := append([]intakedb.AddBlobRetentionReferenceParams(nil), edges...)
	for _, edge := range ordered {
		if edge.ID <= 0 || !model.RetentionOwnerKind(edge.OwnerKind).Valid() {
			return fmt.Errorf("invalid retention owner")
		}
		if err := model.ValidateKey(edge.OwnerKey); err != nil {
			return fmt.Errorf("retention owner key: %w", err)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ID != ordered[j].ID {
			return ordered[i].ID < ordered[j].ID
		}
		if ordered[i].OwnerKind != ordered[j].OwnerKind {
			return ordered[i].OwnerKind < ordered[j].OwnerKind
		}
		return ordered[i].OwnerKey < ordered[j].OwnerKey
	})
	for _, edge := range ordered {
		if err := addSourceRetention(ctx, tx, q, edge.ID, model.RetentionOwnerKind(edge.OwnerKind), edge.OwnerKey); err != nil {
			return err
		}
	}
	return nil
}

func (s *Sources) registerCaptureBlob(ctx context.Context, q intakedb.Querier, capture model.CaptureRef, at time.Time) (int64, error) {
	refs := []model.AssetRef{capture.Asset, capture.TransportReceipt}
	sort.Slice(refs, func(i, j int) bool { return refs[i].SHA256 < refs[j].SHA256 })
	var captureID int64
	for _, ref := range refs {
		mediaType := capture.MediaType
		if ref == capture.TransportReceipt {
			mediaType = "application/json"
		}
		_, err := q.RegisterSourceBlob(ctx, intakedb.RegisterSourceBlobParams{Sha256: string(ref.SHA256), ByteSize: ref.ByteSize, MediaType: mediaType, ObjectRef: ref.Path, CaptureSource: capture.SourceID, CaptureRef: capture.CaptureKey, CapturedAt: at})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("register capture blob: %w", err)
		}
		// A new statement resolves a concurrent hash winner invisible to INSERT.
		b, err := q.ReadSourceBlob(ctx, string(ref.SHA256))
		if err != nil {
			return 0, grantError("resolve capture blob", err)
		}
		if b.ByteSize != ref.ByteSize {
			return 0, fmt.Errorf("blob hash payload: %w", run.ErrPayloadConflict)
		}
		if ref.SHA256 == capture.Asset.SHA256 {
			captureID = b.ID
		}
	}
	return captureID, nil
}

func prepareObservation(ctx context.Context, tx pgx.Tx, o model.Observation, window string, blobID int64, executionKey string, result intakedb.IngestExecutionResult, observedAt time.Time) (intakedb.InsertObservationRevisionParams, error) {
	var zero intakedb.InsertObservationRevisionParams
	if err := o.Validate(); err != nil {
		return zero, fmt.Errorf("observation: %w", err)
	}
	if err := model.ValidateKey(o.VersionKey); err != nil {
		return zero, fmt.Errorf("observation version: %w", err)
	}
	// RAW QUERY: ReadSourceRecord. Fresh lookup after insert conflict waits.
	var recordID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM bronze.source_record WHERE source_id = $1 AND external_id = $2`, o.SourceID, o.ExternalID).Scan(&recordID); err != nil {
		return zero, grantError("resolve source record", err)
	}
	key, err := newLedgerKey("observation")
	if err != nil {
		return zero, err
	}
	if err := model.ValidateEnvelopeKey(key, "observation"); err != nil {
		return zero, fmt.Errorf("issued observation key: %w", err)
	}
	p := intakedb.InsertObservationRevisionParams{ObservationKey: key, SourceRecordID: recordID, ObservationDigest: string(o.ObservationHash), MetadataDigest: string(o.MetadataHash), SourceTreeDigest: string(o.SourceTreeHash), RelationsDigest: string(o.RelationsHash), SourceWindowKey: window, DocumentVersionKey: o.VersionKey, ExecutionKey: executionKey, ResultKey: result.ResultKey, CaptureBlobID: blobID, ObservedAt: observedAt}
	return p, nil
}

func registerObservation(ctx context.Context, tx pgx.Tx, q intakedb.Querier, o model.Observation, window string, blobID int64, executionKey string, result intakedb.IngestExecutionResult, observedAt time.Time) (intakedb.BronzeObservationRevision, error) {
	p, err := prepareObservation(ctx, tx, o, window, blobID, executionKey, result, observedAt)
	if err != nil {
		return intakedb.BronzeObservationRevision{}, err
	}
	return insertObservation(ctx, tx, q, p)
}

func insertObservationBatch(ctx context.Context, tx pgx.Tx, q intakedb.Querier, params []intakedb.InsertObservationRevisionParams) ([]intakedb.BronzeObservationRevision, error) {
	order := make([]int, len(params))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := params[order[i]], params[order[j]]
		if a.SourceRecordID != b.SourceRecordID {
			return a.SourceRecordID < b.SourceRecordID
		}
		return a.ObservationDigest < b.ObservationDigest
	})
	revisions := make([]intakedb.BronzeObservationRevision, len(params))
	for _, i := range order {
		stored, err := insertObservation(ctx, tx, q, params[i])
		if err != nil {
			return nil, err
		}
		revisions[i] = stored
	}
	return revisions, nil
}

func insertObservation(ctx context.Context, tx pgx.Tx, q intakedb.Querier, p intakedb.InsertObservationRevisionParams) (intakedb.BronzeObservationRevision, error) {
	var zero intakedb.BronzeObservationRevision
	_, err := q.InsertObservationRevision(ctx, p)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("insert observation revision: %w", err)
	}
	stored, err := q.ReadObservationRevision(ctx, intakedb.ReadObservationRevisionParams{SourceRecordID: p.SourceRecordID, ObservationDigest: p.ObservationDigest})
	if err != nil {
		return zero, grantError("resolve observation revision", err)
	}
	if stored.MetadataDigest != p.MetadataDigest || stored.SourceTreeDigest != p.SourceTreeDigest || stored.RelationsDigest != p.RelationsDigest || stored.CaptureBlobID != p.CaptureBlobID || stored.DocumentVersionKey != p.DocumentVersionKey {
		return zero, fmt.Errorf("observation payload: %w", run.ErrPayloadConflict)
	}
	if err := validateObservationProvenance(ctx, tx, stored); err != nil {
		return zero, err
	}
	// Reuse retains original window/execution/result provenance. Current apply
	// adds its own execution edge rather than rewriting immutable provenance.
	if err := model.ValidateEnvelopeKey(stored.ObservationKey, "observation"); err != nil {
		return zero, fmt.Errorf("stored observation key: %w", err)
	}

	return stored, nil
}

// RAW QUERY: ResolveSourceObservation. Validate all cross-schema owner keys
// against their real source, window and version, not only their key grammar.
func resolveSourceObservation(ctx context.Context, tx pgx.Tx, key, window, version, source string) (intakedb.BronzeObservationRevision, error) {
	var o intakedb.BronzeObservationRevision
	if err := model.ValidateEnvelopeKey(key, "observation"); err != nil {
		return o, fmt.Errorf("parent observation key: %w", err)
	}
	err := tx.QueryRow(ctx, `SELECT o.id, o.observation_key, o.source_record_id, o.observation_digest,
		o.metadata_digest, o.source_tree_digest, o.relations_digest, o.source_window_key,
		o.document_version_key, o.execution_key, o.result_key, o.capture_blob_id, o.observed_at
		FROM bronze.observation_revision o JOIN bronze.source_record s ON s.id = o.source_record_id
		JOIN ingest.execution e ON e.execution_key = o.execution_key
		JOIN ingest.execution_result r ON r.result_key = o.result_key AND r.execution_id = e.id
		WHERE o.observation_key = $1 AND o.source_window_key = $2
		AND o.document_version_key = $3 AND s.source_id = $4
		AND ((e.resource_kind = 'source_window' AND e.resource_key = o.source_window_key AND r.payload->>'SourceOperation' = 'accept_page')
		OR (e.resource_kind = 'document_version' AND e.resource_key = o.document_version_key AND r.payload->>'SourceOperation' = 'accept_capture'))`, key, window, version, source).Scan(&o.ID, &o.ObservationKey, &o.SourceRecordID, &o.ObservationDigest, &o.MetadataDigest, &o.SourceTreeDigest, &o.RelationsDigest, &o.SourceWindowKey, &o.DocumentVersionKey, &o.ExecutionKey, &o.ResultKey, &o.CaptureBlobID, &o.ObservedAt)
	if err != nil {
		return o, grantError("resolve parent observation", err)
	}
	return o, nil
}

// RAW QUERY: ValidateObservationProvenance. Reuse preserves its original
// execution and result, but must still resolve their actual source binding.
func validateObservationProvenance(ctx context.Context, tx pgx.Tx, o intakedb.BronzeObservationRevision) error {
	var id int64
	err := tx.QueryRow(ctx, `SELECT r.id FROM ingest.execution e
		JOIN ingest.execution_result r ON r.execution_id = e.id
		JOIN ingest.source_window w ON w.window_key = $3
		JOIN bronze.source_record s ON s.id = $5 AND s.source_id = w.source_id
		WHERE e.execution_key = $1 AND r.result_key = $2
		AND ((e.resource_kind = 'source_window' AND e.resource_key = $3 AND r.payload->>'SourceOperation' = 'accept_page')
		OR (e.resource_kind = 'document_version' AND e.resource_key = $4 AND r.payload->>'SourceOperation' = 'accept_capture'))`, o.ExecutionKey, o.ResultKey, o.SourceWindowKey, o.DocumentVersionKey, o.SourceRecordID).Scan(&id)
	if err != nil {
		return grantError("validate observation provenance", err)
	}
	return nil
}

// CaptureAcceptance supplies verified staged capture and purpose-specific
// decoded metadata or artifact identity. Detail may be nil: verified transport
// does not assert successful decoding. Artifact is required only for artifact
// requests; it links to the already accepted parent observation.
type CaptureAcceptance struct {
	// RequestKey selects the previously fetched request.
	RequestKey string
	// Capture must match every field of the immutable transport receipt.
	Capture model.CaptureRef
	// Detail optionally accepts decoded detail metadata and inventory.
	Detail *api.DecodedDetailResult
	// Artifact is required only for an artifact request.
	Artifact *model.ArtifactInventoryEntry
	// CaptureURL preserves the screened final artifact locator.
	CaptureURL string
	// Requests are committed with detail observation acceptance.
	Requests []SourceRequestPlan
}

// ApplyCapture verifies staged bytes, then atomically registers Bronze,
// observation/artifact links, retention and the verified request transition.
// A later decoded detail apply may reuse verified bytes under new immutable
// work. It never rewrites the successful transport receipt or spends an attempt.
func (s *Sources) ApplyCapture(ctx context.Context, work run.IntakeWork, acceptance CaptureAcceptance) (run.IntakeReceipt, error) {
	if err := acceptance.Capture.Validate(); err != nil {
		return run.IntakeReceipt{}, fmt.Errorf("capture acceptance: %w", err)
	}
	if len(acceptance.Requests) > work.Budget.MaxItems {
		return run.IntakeReceipt{}, fmt.Errorf("capture request inventory exceeds work budget")
	}
	if acceptance.Detail != nil {
		if err := validateDecodedInventory(work, *acceptance.Detail); err != nil {
			return run.IntakeReceipt{}, err
		}
	}
	for _, request := range acceptance.Requests {
		if err := request.Plan.Validate(); err != nil {
			return run.IntakeReceipt{}, fmt.Errorf("capture inventory plan: %w", err)
		}
	}
	return s.sourceMutation(ctx, work, run.SubjectDocumentVersion, "accept_capture", acceptance, func(tx pgx.Tx, q intakedb.Querier, result intakedb.IngestExecutionResult) error {
		locks, err := sourceLocks(tx)
		if err != nil {
			return err
		}
		// Window precedes request. Read its immutable identity first, without
		// taking a request lock. Candidate discovery already validated ownership.
		initial, err := q.ReadArtifactRequest(ctx, acceptance.RequestKey)
		if err != nil {
			return grantError("read capture request", err)
		}
		if initial.DocumentVersionKey != work.Execution.Scope.Subject.LeaseKey() {
			return fmt.Errorf("capture request ownership: %w", run.ErrStaleGrant)
		}
		var window string
		for key, id := range locks.windows {
			if id == initial.SourceWindowID {
				window = key
				break
			}
		}
		if window == "" {
			return fmt.Errorf("capture window outside source lock set")
		}
		w, err := s.lockWindow(ctx, tx, q, work, window)
		if err != nil {
			return err
		}
		if w.ID != locks.windows[window] {
			return fmt.Errorf("capture window differs from source lock set")
		}
		r, err := readOwnedRequest(ctx, tx, q, work, acceptance.RequestKey)
		if err != nil {
			return err
		}
		if err := validateCapture(work, w, acceptance.Capture); err != nil {
			return err
		}
		if !captureMatches(r.CaptureKey, r.CaptureDigest, r.CaptureBytes, r.TransportReceipt, acceptance.Capture) {
			return fmt.Errorf("Bronze capture differs from transport: %w", run.ErrPayloadConflict)
		}
		if r.State != string(model.ArtifactRequestFetched) && r.State != string(model.ArtifactRequestVerified) {
			return fmt.Errorf("capture acceptance transition: %w", run.ErrStaleGrant)
		}
		if acceptance.Detail != nil {
			// RAW QUERY: ReadAcceptedSourceDetail. Verified transport can accept
			// decoded metadata separately, but a second decode cannot rewrite it.
			var prior json.RawMessage
			err := tx.QueryRow(ctx, `SELECT result.payload->'SourcePayload' FROM ingest.execution_result result
				JOIN ingest.execution e ON e.id = result.execution_id
				WHERE e.resource_kind = 'document_version' AND e.resource_key = $1 AND result.id <> $2
				AND result.payload->>'SourceOperation' = 'accept_capture'
				AND result.payload->'SourcePayload'->>'RequestKey' = $3
				AND jsonb_typeof(result.payload->'SourcePayload'->'Detail') = 'object'
				ORDER BY result.id LIMIT 1`, work.Execution.Scope.Subject.LeaseKey(), result.ID, acceptance.RequestKey).Scan(&prior)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("read accepted detail: %w", err)
			}
			if err == nil {
				exact, err := sourceJSON(acceptance)
				if err != nil {
					return err
				}
				if !sameSourceJSON(prior, exact) {
					return fmt.Errorf("accepted detail payload: %w", run.ErrPayloadConflict)
				}
				return nil
			}
		}
		parent, err := resolveSourceObservation(ctx, tx, r.OwnerKey, w.WindowKey, work.Execution.Scope.Subject.LeaseKey(), w.SourceID)
		if err != nil {
			return err
		}
		for _, request := range acceptance.Requests {
			if request.Plan.WindowKey != w.WindowKey {
				return fmt.Errorf("accepted capture inventory changes source window")
			}
		}
		_, executionKey, err := resolveExecution(ctx, tx, work)
		if err != nil {
			return err
		}
		observedAt, err := sourceCaptureTime(ctx, tx, work, "transport", r.RequestKey, acceptance.Capture.CaptureKey)
		if err != nil {
			return err
		}
		if acceptance.Detail != nil {
			if err := insertSourceRecords(ctx, q, []api.DecodedObservation{acceptance.Detail.Observation}); err != nil {
				return err
			}
		}
		blobID, err := s.registerCaptureBlob(ctx, q, acceptance.Capture, observedAt)
		if err != nil {
			return err
		}
		observationKey := ""
		switch model.RequestPurpose(r.Purpose) {
		case model.PurposeDetail:
			if acceptance.Artifact != nil {
				return fmt.Errorf("detail capture cannot invent an artifact")
			}
			if acceptance.Detail != nil {
				if err := acceptance.Detail.Validate(); err != nil {
					return fmt.Errorf("captured detail: %w", err)
				}
				if acceptance.Detail.Observation.Capture != acceptance.Capture || acceptance.Detail.Observation.VersionKey != r.DocumentVersionKey {
					return fmt.Errorf("detail differs from captured document version")
				}
				o, err := sourceObservation(acceptance.Detail.Observation)
				if err != nil {
					return err
				}
				revision, err := registerObservation(ctx, tx, q, o, w.WindowKey, blobID, executionKey, result, observedAt)
				if err != nil {
					return err
				}
				observationKey = revision.ObservationKey
				requests := append([]SourceRequestPlan(nil), acceptance.Requests...)
				for i := range requests {
					if requests[i].ParentObservation != "" && requests[i].ParentObservation != revision.ObservationKey {
						return fmt.Errorf("inventory request must use accepted detail observation key")
					}
					requests[i].ParentObservation = revision.ObservationKey
				}
				if revision.SourceRecordID != parent.SourceRecordID {
					return fmt.Errorf("captured detail changes parent source identity")
				}
				if err := s.registerRequests(ctx, tx, q, work, *acceptance.Detail, requests); err != nil {
					return err
				}
			} else if len(acceptance.Requests) != 0 {
				return fmt.Errorf("request planning requires decoded detail inventory")
			}
		case model.PurposeArtifact:
			if acceptance.Artifact == nil || acceptance.Detail != nil || len(acceptance.Requests) != 0 {
				return fmt.Errorf("artifact capture requires only its artifact inventory")
			}
			a := *acceptance.Artifact
			if err := a.Validate(); err != nil {
				return fmt.Errorf("captured artifact: %w", err)
			}
			if !model.ArtifactRole(a.Role).Valid() || !model.ArtifactFormatValid(a.Format) || r.ExpectedRole == nil || a.Role != *r.ExpectedRole || a.Required != r.Required || a.Ordinal > int(^uint32(0)>>1) || a.Capture == nil || *a.Capture != acceptance.Capture {
				return fmt.Errorf("artifact differs from frozen expected role or capture")
			}
			var plan frozenSourceRequest
			if err := json.Unmarshal(r.RequestPlan, &plan); err != nil {
				return fmt.Errorf("decode artifact plan: %w", err)
			}
			if a.URL != plan.URL {
				return fmt.Errorf("artifact locator differs from frozen request")
			}
			proposed := a
			proposed.Capture = nil
			if plan.SourceArtifact == nil {
				return fmt.Errorf("artifact request has no frozen inventory")
			}
			frozen := *plan.SourceArtifact
			frozen.Capture = nil
			if !reflect.DeepEqual(proposed, frozen) {
				return fmt.Errorf("artifact differs from decoded inventory: %w", run.ErrPayloadConflict)
			}
			if err := model.ValidatePublicURL(acceptance.CaptureURL); err != nil {
				return fmt.Errorf("artifact capture URL: %w", err)
			}
			if err := registerSourceArtifact(ctx, tx, q, parent, blobID, a, acceptance.CaptureURL); err != nil {
				return err
			}
			observationKey = parent.ObservationKey
		default:
			return fmt.Errorf("unknown capture request purpose")
		}
		var observationKeys []string
		if observationKey != "" {
			observationKeys = append(observationKeys, observationKey)
		}
		if err := protectSourceCapture(ctx, tx, q, acceptance.Capture, executionKey, observationKeys); err != nil {
			return err
		}
		// RAW QUERY: VerifyArtifactRequest. The fetched receipt and host slot
		// were already reconciled by transport. Verification releases no slot.
		if _, err := tx.Exec(ctx, `UPDATE ingest.artifact_request SET state = 'verified' WHERE id = $1 AND state = 'fetched'`, r.ID); err != nil {
			return fmt.Errorf("verify artifact request: %w", err)
		}
		return nil
	})
}

// AcceptDetail accepts decoded detail and inventory against the identical
// fetched or verified transport receipt through ApplyCapture's fenced atomic
// transaction. It never changes transport counters or releases a host slot.
func (s *Sources) AcceptDetail(ctx context.Context, work run.IntakeWork, acceptance CaptureAcceptance) (run.IntakeReceipt, error) {
	if acceptance.Detail == nil || acceptance.Artifact != nil {
		return run.IntakeReceipt{}, fmt.Errorf("decoded detail is required")
	}
	return s.ApplyCapture(ctx, work, acceptance)
}

// RAW QUERY: ReadSourceCaptureTime. Record observation time at the original
// fenced capture receipt, not at a later decode or Bronze acceptance. Historical
// receipt grants are provenance; only the current apply grant authorizes writes.
func sourceCaptureTime(ctx context.Context, tx pgx.Tx, work run.IntakeWork, operation, key, captureKey string) (time.Time, error) {
	kind, resource, err := leaseResource(work.Execution.Scope)
	if err != nil {
		return time.Time{}, err
	}
	var observedAt time.Time
	err = tx.QueryRow(ctx, `SELECT r.created_at FROM ingest.execution_result r
		JOIN ingest.execution e ON e.id = r.execution_id WHERE e.resource_kind = $1 AND e.resource_key = $2
		AND r.payload->>'SourceOperation' = $3
		AND COALESCE(r.payload->'SourcePayload'->>'Key', r.payload->'SourcePayload'->>'RequestKey') = $4
		AND r.payload->'SourcePayload'->'Capture'->>'CaptureKey' = $5
		ORDER BY r.id LIMIT 1`, kind, resource, operation, key, captureKey).Scan(&observedAt)
	if err != nil {
		return observedAt, grantError("resolve source capture time", err)
	}
	return observedAt, nil
}

func addSourceRetention(ctx context.Context, tx pgx.Tx, q intakedb.Querier, blobID int64, kind model.RetentionOwnerKind, key string) error {
	// Callers supply only the execution resolved under the grant or the
	// observation read/inserted in this apply. No public arbitrary owner port.
	_, err := q.AddBlobRetentionReference(ctx, intakedb.AddBlobRetentionReferenceParams{ID: blobID, OwnerKind: string(kind), OwnerKey: key})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("add source retention: %w", err)
	}
	// RAW QUERY: ReadBlobRetentionReference. Fresh lookup after a concurrent
	// edge winner that was invisible while waiting on the catalog row lock.
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM bronze.blob_retention_reference
		WHERE blob_id = $1 AND owner_kind = $2 AND owner_key = $3`, blobID, string(kind), key).Scan(&id); err != nil {
		return grantError("resolve source retention", err)
	}
	return nil
}

func registerSourceArtifact(ctx context.Context, tx pgx.Tx, q intakedb.Querier, parent intakedb.BronzeObservationRevision, blobID int64, a model.ArtifactInventoryEntry, captureURL string) error {
	_, err := q.InsertSourceArtifact(ctx, intakedb.InsertSourceArtifactParams{ArtifactKey: a.ArtifactKey, BlobID: blobID, Role: a.Role, DeclaredFilename: a.DeclaredFilename, Format: a.Format, SourceUrl: a.URL, CaptureUrl: captureURL})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("register source artifact: %w", err)
	}
	// RAW QUERY: ReadSourceArtifact. Fresh snapshot after a key conflict.
	var id, storedBlob int64
	var role, filename, format, sourceURL, storedCaptureURL string
	if err := tx.QueryRow(ctx, `SELECT id, blob_id, role, declared_filename, format, source_url, capture_url
		FROM bronze.artifact WHERE artifact_key = $1`, a.ArtifactKey).Scan(&id, &storedBlob, &role, &filename, &format, &sourceURL, &storedCaptureURL); err != nil {
		return grantError("read source artifact", err)
	}
	if storedBlob != blobID || role != a.Role || filename != a.DeclaredFilename || format != a.Format || sourceURL != a.URL || storedCaptureURL != captureURL {
		return fmt.Errorf("artifact payload: %w", run.ErrPayloadConflict)
	}
	_, err = q.LinkObservationArtifact(ctx, intakedb.LinkObservationArtifactParams{ObservationRevisionID: parent.ID, ID: id, Role: a.Role, Ordinal: int32(a.Ordinal), Required: a.Required})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("link observation artifact: %w", err)
	}
	// RAW QUERY: ReadObservationArtifact. A uniqueness collision must compare
	// the artifact and required flag rather than silently accepting a new link.
	var linkedID int64
	var required bool
	if err := tx.QueryRow(ctx, `SELECT artifact_id, required FROM bronze.observation_artifact
		WHERE observation_revision_id = $1 AND role = $2 AND ordinal = $3`, parent.ID, a.Role, a.Ordinal).Scan(&linkedID, &required); err != nil {
		return grantError("read artifact link", err)
	}
	if linkedID != id || required != a.Required {
		return fmt.Errorf("artifact link payload: %w", run.ErrPayloadConflict)
	}
	return nil
}

// AcceptedObservations resolves ledger-issued observation keys from an owning
// apply receipt, including immutable observation reuse. It reads no document
// bytes and requires a current registered execution for the same resource.
func (s *Sources) AcceptedObservations(ctx context.Context, work run.IntakeWork, resultKey string) ([]intakedb.BronzeObservationRevision, error) {
	if err := model.ValidateEnvelopeKey(resultKey, "result"); err != nil {
		return nil, fmt.Errorf("source result key: %w", err)
	}
	kind := work.Execution.Scope.Subject.Kind
	if kind != run.SubjectSourceWindow && kind != run.SubjectDocumentVersion {
		return nil, fmt.Errorf("source observation scope: %w", run.ErrStaleGrant)
	}
	return sourceTransaction(ctx, s, work, kind, func(tx pgx.Tx, q intakedb.Querier) ([]intakedb.BronzeObservationRevision, error) {
		resourceKind, resourceKey, err := leaseResource(work.Execution.Scope)
		if err != nil {
			return nil, err
		}
		// RAW QUERY: ReadOwnedSourceResult. Bind receipt to the owning resource
		// before resolving any cross-schema observation or request reference.
		var raw json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT r.payload FROM ingest.execution_result r
			JOIN ingest.execution e ON e.id = r.execution_id WHERE r.result_key = $1
			AND e.resource_kind = $2 AND e.resource_key = $3`, resultKey, resourceKind, resourceKey).Scan(&raw); err != nil {
			return nil, grantError("resolve owning source receipt", err)
		}
		var payload sourcePayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, fmt.Errorf("decode source receipt: %w", err)
		}
		var decoded []api.DecodedObservation
		switch payload.SourceOperation {
		case "accept_page":
			var a DiscoveryAcceptance
			if err := json.Unmarshal(payload.SourcePayload, &a); err != nil {
				return nil, fmt.Errorf("decode accepted page: %w", err)
			}
			decoded = a.Page.Observations
		case "accept_capture":
			var a CaptureAcceptance
			if err := json.Unmarshal(payload.SourcePayload, &a); err != nil {
				return nil, fmt.Errorf("decode accepted capture: %w", err)
			}
			if a.Detail != nil {
				decoded = []api.DecodedObservation{a.Detail.Observation}
			}
		default:
			return nil, fmt.Errorf("source receipt does not accept observations")
		}
		if len(decoded) > work.Budget.MaxItems {
			return nil, fmt.Errorf("accepted observations exceed current read budget")
		}
		observations := make([]intakedb.BronzeObservationRevision, 0, len(decoded))
		for _, value := range decoded {
			o, err := sourceObservation(value)
			if err != nil {
				return nil, err
			}
			var recordID int64
			// RAW QUERY: ReadSourceRecord.
			if err := tx.QueryRow(ctx, `SELECT id FROM bronze.source_record WHERE source_id = $1 AND external_id = $2`, o.SourceID, o.ExternalID).Scan(&recordID); err != nil {
				return nil, grantError("resolve accepted source record", err)
			}
			stored, err := q.ReadObservationRevision(ctx, intakedb.ReadObservationRevisionParams{SourceRecordID: recordID, ObservationDigest: string(o.ObservationHash)})
			if err != nil {
				return nil, grantError("read accepted observation", err)
			}
			observations = append(observations, stored)
		}
		return observations, nil
	})
}
