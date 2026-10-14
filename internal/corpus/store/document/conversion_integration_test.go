package document

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

func conversionAdapterFixture(t *testing.T) (identityDatabase, *pgx.Conn, *Conversion, Grant, conversionSeed) {
	t.Helper()
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	seed := seedConversion(t, f.ctx, tx)
	if err := tx.Commit(f.ctx); err != nil { t.Fatal(err) }
	u := *f.u
	u.User = url.User("banhmi_corpus_writer_app")
	app, err := pgx.Connect(f.ctx, u.String())
	if err != nil { t.Fatal("connect conversion adapter app failed") }
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), revisionRollbackTimeout)
		defer cancel()
		_ = app.Close(ctx)
	})
	c, err := NewConversion(app, "writer-a")
	if err != nil { t.Fatal(err) }
	return f, app, c, adapterGrant(seed.execution), seed
}

func conversionAdapterBlob(hash string) blob.Metadata {
	return blob.Metadata{SHA256: hash, ByteSize: 16, MediaType: "application/octet-stream", ObjectRef: "synthetic-object",
		CaptureSource: "source-a", CaptureRef: "capture-00000000000000000000000000000001", CapturedAt: time.Unix(1, 0)}
}

func conversionAdapterArtifact() ConversionArtifactInput {
	return ConversionArtifactInput{Role: model.ConversionRawText, Blob: conversionAdapterBlob(identityDigest), Manifest: conversionAdapterBlob(identityDigest),
		SchemaVersion: "converted-v1", OriginalHash: identityDigest, ConversionRecipeDigest: identityDigest, NormalizationRecipeDigest: identityDigest,
		DetectorDigest: identityDigest, Members: []ConversionArtifactMember{{Role: model.ConversionRawText, Hash: identityDigest, ByteSize: 16}}}
}

func conversionAdapterAsset() model.AssetRef { return model.AssetRef{Path: "synthetic", SHA256: identityDigest, ByteSize: 16} }

func conversionAdapterSegment(ordinal int32) ConversionSegment {
	return ConversionSegment{MapHash: identityDigest, Ordinal: ordinal, Transform: "nfc",
		Source: model.Span{ArtifactHash: identityDigest, BlockID: "block-a", Space: model.RawText, Range: &model.ByteRange{Start: 0, End: 1}},
		Destination: model.Span{ArtifactHash: identityDigest, BlockID: "block-b", Space: model.NormalizedText, Range: &model.ByteRange{Start: 0, End: 1}}}
}

func conversionAdapterEdit() ConversionEditInput {
	return ConversionEditInput{LedgerHash: identityDigest, Operation: model.EditReplace, Rule: "nfc", RuleVersion: "1",
		Input: conversionAdapterAsset(), Output: conversionAdapterAsset(), InputRange: model.ByteRange{Start: 0, End: 1}, OutputRange: model.ByteRange{Start: 0, End: 1},
		RemovedBytes: []byte("a"), ReplacementBytes: []byte("a"), EncodingEvidence: []byte(`{}`), FontEvidence: []byte(`{}`), SourceAnchors: []byte(`[]`)}
}

func conversionAdapterReport() ConversionQualityInput {
	severity, scope, scopeKey, reason := model.SeverityWarning, model.GapArtifact, "artifact-a", "BYTE_MAPPING_UNAVAILABLE"
	hash := model.Digest(identityDigest)
	return ConversionQualityInput{Report: conversionAdapterBlob(identityDigest), ArtifactHash: &hash, PolicyDigest: identityDigest, ThresholdVersion: "1",
		Outcome: model.QualityQuarantined, RecordCount: 1, Records: []ConversionQualityRecord{{Kind: model.QualityRecordFinding, Code: reason, Severity: &severity,
			Scope: &scope, ScopeKey: &scopeKey, Reason: &reason, Evidence: []byte(`[]`), SourceSpans: []byte(`[]`)}}}
}

func conversionAdapterTarget() ConversionTarget { return ConversionTarget{RevisionKey: revisionKey, CandidateDigest: identityDigest} }

func conversionAdapterCounts(t *testing.T, f identityDatabase) [6]int64 {
	t.Helper()
	var result [6]int64
	for i, table := range []string{"conversion_artifact", "offset_map_segment", "edit_ledger", "quality_report", "quality_finding", "revision_comparison"} {
		if err := f.conn.QueryRow(f.ctx, "SELECT count(*) FROM silver."+table).Scan(&result[i]); err != nil { t.Fatal(err) }
	}
	return result
}

func conversionAdapterReplay(t *testing.T, f identityDatabase, write func() (ConversionReceipt, error), change func() error) ConversionReceipt {
	t.Helper()
	first, err := write(); if err != nil || first.ID <= 0 { t.Fatalf("conversion write: %v", err) }
	before := conversionAdapterCounts(t, f)
	replay, err := write()
	if err != nil || !reflect.DeepEqual(first, replay) || before != conversionAdapterCounts(t, f) { t.Fatalf("conversion replay changed immutable receipt: %v", err) }
	if err := change(); !errors.Is(err, run.ErrPayloadConflict) || before != conversionAdapterCounts(t, f) { t.Fatalf("changed payload did not conflict atomically: %v", err) }
	return first
}

func TestConversionAdapterWritesAndBoundedReads(t *testing.T) {
	f, _, c, g, _ := conversionAdapterFixture(t)
	target := conversionAdapterTarget()
	artifact := conversionAdapterArtifact()
	a := conversionAdapterReplay(t, f, func() (ConversionReceipt, error) { return c.RegisterConversionArtifact(f.ctx, g, target, artifact) }, func() error {
		changed := artifact; changed.DetectorDigest = conversionOtherDigest
		_, err := c.RegisterConversionArtifact(f.ctx, g, target, changed); return err
	})
	rows, err := c.ReadConversionArtifacts(f.ctx, g, revisionKey, artifact.Role, 0, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != a.ID { t.Fatalf("artifact page: %v", err) }
	rows, err = c.ReadConversionArtifacts(f.ctx, g, revisionKey, artifact.Role, a.ID, 1)
	if err != nil || len(rows) != 0 { t.Fatalf("artifact cursor: %v", err) }
	changedArtifact := artifact
	changedArtifact.Blob = conversionAdapterBlob(conversionOtherDigest)
	changedArtifact.Members = []ConversionArtifactMember{{Role: artifact.Role, Hash: conversionOtherDigest, ByteSize: 16}}
	if _, err := c.RegisterConversionArtifact(f.ctx, g, target, changedArtifact); !errors.Is(err, run.ErrPayloadConflict) { t.Fatalf("same-key digest mismatch: %v", err) }
	asset := conversionAdapterAsset()
	segments := []ConversionSegment{conversionAdapterSegment(1), conversionAdapterSegment(0)}
	first, err := c.InsertOffsetMapSegments(f.ctx, g, target, asset, asset, segments); if err != nil || len(first) != 2 { t.Fatalf("map batch: %v", err) }
	before := conversionAdapterCounts(t, f)
	replay, err := c.InsertOffsetMapSegments(f.ctx, g, target, asset, asset, segments)
	if err != nil || !reflect.DeepEqual(first, replay) { t.Fatalf("map replay: %v", err) }
	changed := append([]ConversionSegment{}, segments...); changed[0].Transform = "different"
	_, err = c.InsertOffsetMapSegments(f.ctx, g, target, asset, asset, changed)
	if !errors.Is(err, run.ErrPayloadConflict) || before != conversionAdapterCounts(t, f) { t.Fatalf("map conflict: %v", err) }
	page, err := c.ReadOffsetMapSegments(f.ctx, g, revisionKey, "block-a", 0, 1)
	if err != nil || len(page) != 1 { t.Fatalf("segment page: %v", err) }
	next, err := c.ReadOffsetMapSegments(f.ctx, g, revisionKey, "block-a", page[0].ID, 1)
	if err != nil || len(next) != 1 || next[0].ID <= page[0].ID { t.Fatalf("segment cursor: %v", err) }
	edit := conversionAdapterEdit()
	e := conversionAdapterReplay(t, f, func() (ConversionReceipt, error) { return c.AppendEditLedgerEntry(f.ctx, g, target, edit) }, func() error {
		changed := edit; changed.RemovedBytes = []byte("b")
		_, err := c.AppendEditLedgerEntry(f.ctx, g, target, changed); return err
	})
	ledger, err := c.ReadEditLedgerByHashes(f.ctx, g, target, identityDigest, identityDigest, 0, 1)
	if err != nil || len(ledger) != 1 || ledger[0].ID != e.ID || string(ledger[0].RemovedBytes) != "a" { t.Fatalf("ledger page: %v", err) }
	report := conversionAdapterReport()
	r := conversionAdapterReplay(t, f, func() (ConversionReceipt, error) { return c.RecordConversionQualityReport(f.ctx, g, target, report) }, func() error {
		changed := report; changed.ThresholdVersion = "2"
		_, err := c.RecordConversionQualityReport(f.ctx, g, target, changed); return err
	})
	reports, err := c.ReadConversionQualityReports(f.ctx, g, target, 0, 1)
	if err != nil || len(reports) != 1 || reports[0].ID != r.ID || reports[0].RecordCount != 1 { t.Fatalf("report page: %v", err) }
	findings, err := c.ReadConversionQualityFindings(f.ctx, g, target, r.ID, 0, 1)
	if err != nil || len(findings) != 1 { t.Fatalf("finding page: %v", err) }
	other := target; other.CandidateDigest = conversionOtherDigest
	findings, err = c.ReadConversionQualityFindings(f.ctx, g, other, r.ID, 0, 1)
	if err != nil || len(findings) != 0 { t.Fatalf("finding candidate binding: %v", err) }
	before = conversionAdapterCounts(t, f)
	badCount := report; badCount.RecordCount++
	_, err = c.RecordConversionQualityReport(f.ctx, g, target, badCount)
	var rejection *RejectionError
	if !errors.As(err, &rejection) || before != conversionAdapterCounts(t, f) { t.Fatalf("report count mismatch wrote rows: %v", err) }
	// Changing a child while retaining the report hash must conflict too.
	changedReport := report; changedReport.Records = append([]ConversionQualityRecord{}, report.Records...); changedReport.Records[0].Code = "CHANGED"
	_, err = c.RecordConversionQualityReport(f.ctx, g, target, changedReport)
	if !errors.Is(err, run.ErrPayloadConflict) || before != conversionAdapterCounts(t, f) { t.Fatalf("report child conflict: %v", err) }
}

func TestConversionAdapterComparisonStaleness(t *testing.T) {
	f, _, c, g, seed := conversionAdapterFixture(t)
	target := conversionAdapterTarget()
	prior, err := c.RecordConversionQualityReport(f.ctx, g, target, conversionAdapterReport()); if err != nil { t.Fatal(err) }
	setup := identityTx(t, f, false)
	seedConversionAcceptance(t, f.ctx, setup, seed, identityDigest, 1)
	if err := setup.Commit(f.ctx); err != nil { t.Fatal(err) }
	target.CandidateDigest = conversionOtherDigest
	candidate, err := c.RecordConversionQualityReport(f.ctx, g, target, conversionAdapterReport()); if err != nil { t.Fatal(err) }
	v := ConversionComparisonInput{PriorReportID: prior.ID, CandidateReportID: candidate.ID, PriorRevisionKey: revisionKey,
		PriorCandidateDigest: identityDigest, RecipeDigest: identityDigest, Result: conversionAdapterBlob(identityDigest), AcceptanceWatermark: 1}
	comparison := conversionAdapterReplay(t, f, func() (ConversionReceipt, error) { return c.RecordRevisionComparison(f.ctx, g, target, v) }, func() error {
		changed := v; changed.Result = conversionAdapterBlob(conversionOtherDigest)
		_, err := c.RecordRevisionComparison(f.ctx, g, target, changed); return err
	})
	rows, err := c.ReadRevisionComparisons(f.ctx, g, identityDigest, conversionOtherDigest, 0, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != comparison.ID { t.Fatalf("eligible comparison page: %v", err) }
	setup = identityTx(t, f, false)
	seedConversionAcceptance(t, f.ctx, setup, seed, conversionNewDigest, 2)
	if err := setup.Commit(f.ctx); err != nil { t.Fatal(err) }
	before := conversionAdapterCounts(t, f)
	rows, err = c.ReadRevisionComparisons(f.ctx, g, identityDigest, conversionOtherDigest, 0, 1)
	if err != nil || len(rows) != 0 { t.Fatalf("new candidate did not derive staleness: %v", err) }
	_, err = c.RecordRevisionComparison(f.ctx, g, target, v)
	var rejected *RejectionError
	if !errors.As(err, &rejected) || before != conversionAdapterCounts(t, f) { t.Fatalf("stale replay mutated provenance: %v", err) }
	var watermark int64
	if err := f.conn.QueryRow(f.ctx, "SELECT acceptance_watermark FROM silver.revision_comparison WHERE id = $1", comparison.ID).Scan(&watermark); err != nil || watermark != 1 { t.Fatalf("staleness changed baseline: %v", err) }
}

func TestConversionAdapterPreRevisionReport(t *testing.T) {
	f, _, c, g, _ := conversionAdapterFixture(t)
	target := ConversionTarget{RevisionKey: identityKey + ":rev:" + conversionOtherDigest, CandidateDigest: conversionOtherDigest}
	r, err := c.RecordConversionQualityReport(f.ctx, g, target, conversionAdapterReport()); if err != nil { t.Fatal(err) }
	rows, err := c.ReadConversionQualityReports(f.ctx, g, target, 0, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != r.ID || rows[0].DocumentRevisionID != nil { t.Fatalf("quarantine fabricated a revision: %v", err) }
	before := conversionAdapterCounts(t, f)
	for _, write := range []func() error{
		func() error { _, err := c.RegisterConversionArtifact(f.ctx, g, target, conversionAdapterArtifact()); return err },
		func() error { _, err := c.AppendEditLedgerEntry(f.ctx, g, target, conversionAdapterEdit()); return err },
		func() error { _, err := c.InsertOffsetMapSegments(f.ctx, g, target, conversionAdapterAsset(), conversionAdapterAsset(), []ConversionSegment{conversionAdapterSegment(0)}); return err },
	} {
		var rejection *RejectionError
		if err := write(); !errors.As(err, &rejection) || before != conversionAdapterCounts(t, f) { t.Fatalf("dangling provenance wrote rows: %v", err) }
	}
}

func TestConversionAdapterStaleGrantsAndRuntimeDenial(t *testing.T) {
	for _, name := range []string{"epoch", "owner", "fence", "expired", "cancelled", "disabled", "execution", "input", "recipe", "budget", "writer", "context", "deadline"} {
		t.Run(name, func(t *testing.T) {
			f, app, c, g, _ := conversionAdapterFixture(t)
			target := conversionAdapterTarget()
			if _, err := c.RegisterConversionArtifact(f.ctx, g, target, conversionAdapterArtifact()); err != nil { t.Fatal(err) }
			before := conversionAdapterCounts(t, f)
			ctx := f.ctx
			var want error = run.ErrStaleGrant
			switch name {
			case "epoch": g.Execution.AdmissionEpoch++
			case "owner": g.Execution.Owner = "owner-b"
			case "fence": g.Execution.AttemptFence++
			case "execution": g.ExecutionID += 9999
			case "input": g.InputDigest = conversionOtherDigest
			case "recipe": g.RecipeDigest = conversionOtherDigest
			case "budget": g.BudgetDigest = conversionOtherDigest
			case "writer": c.revisions.writer = "writer-other"
			case "context": var cancel context.CancelFunc; ctx, cancel = context.WithCancel(f.ctx); cancel(); want = context.Canceled
			case "deadline": var cancel context.CancelFunc; ctx, cancel = context.WithDeadline(f.ctx, time.Unix(1, 0)); defer cancel(); want = context.DeadlineExceeded
			default:
				query := map[string]string{"expired": "UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second'",
					"cancelled": "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()",
					"disabled": "UPDATE ingest.country_admission SET enabled = false, admission_epoch = admission_epoch + 1"}[name]
				if _, err := f.conn.Exec(f.ctx, query); err != nil { t.Fatal(err) }
			}
			for _, write := range []func() error{
				func() error { _, err := c.RegisterConversionArtifact(ctx, g, target, conversionAdapterArtifact()); return err },
				func() error { _, err := c.InsertOffsetMapSegments(ctx, g, target, conversionAdapterAsset(), conversionAdapterAsset(), []ConversionSegment{conversionAdapterSegment(0)}); return err },
				func() error { _, err := c.AppendEditLedgerEntry(ctx, g, target, conversionAdapterEdit()); return err },
				func() error { _, err := c.RecordConversionQualityReport(ctx, g, target, conversionAdapterReport()); return err },
				func() error { _, err := c.ReadConversionArtifacts(ctx, g, revisionKey, model.ConversionRawText, 0, 1); return err },
				func() error { _, err := c.ReadOffsetMapSegments(ctx, g, revisionKey, "block-a", 0, 1); return err },
				func() error { _, err := c.ReadEditLedgerByHashes(ctx, g, target, identityDigest, identityDigest, 0, 1); return err },
				func() error { _, err := c.ReadConversionQualityReports(ctx, g, target, 0, 1); return err },
				func() error { _, err := c.ReadConversionQualityFindings(ctx, g, target, 1, 0, 1); return err },
				func() error { _, err := c.ReadRevisionComparisons(ctx, g, identityDigest, conversionOtherDigest, 0, 1); return err },
				func() error { _, err := c.RecordRevisionComparison(ctx, g, target, ConversionComparisonInput{PriorReportID: 1, CandidateReportID: 2,
					PriorRevisionKey: revisionKey, PriorCandidateDigest: conversionOtherDigest, RecipeDigest: identityDigest, Result: conversionAdapterBlob(identityDigest)}); return err },
			} { if err := write(); !errors.Is(err, want) || before != conversionAdapterCounts(t, f) { t.Fatalf("stale operation: %v", err) } }
			// Actual app login, no role switching. Failed standalone statements
			// do not poison the connection or require savepoint restoration.
			for _, table := range []string{"conversion_artifact", "offset_map_segment", "edit_ledger", "quality_report", "quality_finding", "revision_comparison"} {
				for _, query := range []string{"UPDATE silver."+table+" SET id = id", "DELETE FROM silver."+table} {
					_, err := app.Exec(f.ctx, query)
					var state interface{ SQLState() string }
					if !errors.As(err, &state) || state.SQLState() != "42501" { t.Fatalf("runtime mutation permitted: %v", err) }
				}
			}
		})
	}
}

type conversionEmptyStarter struct {
	conn *pgx.Conn
	t *testing.T
	last *conversionEmptyTx
	inserts int
	begins int
	force int
	rollbackFailure error
	cancel context.CancelFunc
	mutation string
	countTable string
	corruptCount bool
	expectedRows int64
	mutationStarted chan struct{}
	rollbackReady chan struct{}
	classify chan struct{}
}

func (s *conversionEmptyStarter) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if s.last != nil && s.last.empty && !s.last.rolledBack { s.t.Fatal("retry preceded rollback") }
	tx, err := s.conn.BeginTx(ctx, options); if err != nil { return nil, err }
	s.begins++
	if s.begins == 2 {
		var count int64
		table := s.countTable
		if table == "" { table = "offset_map_segment" }
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM silver."+table).Scan(&count); err != nil || count != s.expectedRows { s.t.Fatalf("empty batch leaked rows: %v", err) }
	}
	s.last = &conversionEmptyTx{Tx: tx, starter: s}
	return s.last, nil
}

type conversionEmptyTx struct { pgx.Tx; starter *conversionEmptyStarter; empty bool; rolledBack bool }

func (tx *conversionEmptyTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx.empty { tx.starter.t.Fatal("statement followed empty map result") }
	mutation := tx.starter.mutation
	if mutation == "" { mutation = "InsertOffsetMapSegments" }
	if strings.HasPrefix(sql, "-- name: "+mutation) {
		tx.starter.inserts++
		if tx.starter.inserts == 1 && tx.starter.mutationStarted != nil { close(tx.starter.mutationStarted) }
		row := tx.Tx.QueryRow(ctx, sql, args...)
		return conversionEmptyRow{row, tx, tx.starter.inserts <= tx.starter.force}
	}
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func (tx *conversionEmptyTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx.empty { tx.starter.t.Fatal("read followed empty map result") }
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx *conversionEmptyTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx.empty { tx.starter.t.Fatal("statement followed empty mutation result") }
	return tx.Tx.Exec(ctx, sql, args...)
}

func (tx *conversionEmptyTx) Rollback(ctx context.Context) error {
	if ctx.Err() != nil { tx.starter.t.Error("rollback inherited cancellation") }
	if _, ok := ctx.Deadline(); !ok { tx.starter.t.Error("rollback was unbounded") }
	if tx.starter.rollbackFailure != nil { err := tx.starter.rollbackFailure; tx.starter.rollbackFailure = nil; return err }
	err := tx.Tx.Rollback(ctx)
	if err == nil || errors.Is(err, pgx.ErrTxClosed) {
		first := !tx.rolledBack
		tx.rolledBack = true
		if first && tx.empty && tx.starter.rollbackReady != nil {
			close(tx.starter.rollbackReady)
			select { case <-tx.starter.classify: case <-ctx.Done(): return ctx.Err() }
		}
	}
	return err
}

type conversionEmptyRow struct { row pgx.Row; tx *conversionEmptyTx; force bool }

func (r conversionEmptyRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.force) {
		r.tx.empty = true
		if r.tx.starter.cancel != nil { r.tx.starter.cancel() }
		if err == nil && r.tx.starter.corruptCount {
			if len(dest) != 2 { r.tx.starter.t.Fatal("report count injection received wrong projection") }
			count, ok := dest[1].(*int64)
			if !ok { r.tx.starter.t.Fatal("report count injection received wrong type") }
			*count = -1
			return nil
		}
		return pgx.ErrNoRows
	}
	return err
}

func TestConversionAdapterEmptyBatchRollback(t *testing.T) {
	for _, name := range []string{"retry", "exhausted", "cancelled-cleanup-failure"} {
		t.Run(name, func(t *testing.T) {
			f, app, _, g, _ := conversionAdapterFixture(t)
			s := &conversionEmptyStarter{conn: app, t: t, force: 1}
			ctx := f.ctx
			var want error
			var cleanup error
			if name == "exhausted" { s.force = 2; want = run.ErrPayloadConflict }
			if name == "cancelled-cleanup-failure" {
				ctx, s.cancel = context.WithCancel(f.ctx)
				t.Cleanup(s.cancel)
				cleanup = errors.New("synthetic rollback failure")
				s.rollbackFailure = cleanup
				want = context.Canceled
			}
			c, err := NewConversion(s, "writer-a"); if err != nil { t.Fatal(err) }
			rows, err := c.InsertOffsetMapSegments(ctx, g, conversionAdapterTarget(), conversionAdapterAsset(), conversionAdapterAsset(), []ConversionSegment{conversionAdapterSegment(0)})
			if want == nil { if err != nil || len(rows) != 1 || s.begins != 2 { t.Fatalf("rollback retry: %v", err) }
			} else {
				if !errors.Is(err, want) || (cleanup != nil && !errors.Is(err, cleanup)) { t.Fatalf("rollback lost cause: %v", err) }
				if conversionAdapterCounts(t, f)[1] != 0 { t.Fatal("failed batch retained rows") }
				if cleanup != nil && s.begins != 1 { t.Fatal("retried failed cleanup") }
			}
		})
	}
}

func TestConversionAdapterWrongVersionAndDanglingBlobs(t *testing.T) {
	f, _, c, g, seed := conversionAdapterFixture(t)
	otherKey := identityKey + ":rev:" + conversionOtherDigest
	if _, err := f.conn.Exec(f.ctx, `INSERT INTO silver.document_revision
		(document_identity_id, revision_key, source_observation_set_hash, text_hash, metadata_hash,
		 conversion_recipe_digest, normalization_recipe_digest, parser_recipe_digest, policy_recipe_digest,
		 normalized_number, document_type, document_version_key, intake_execution_id, intake_execution_key)
		SELECT document_identity_id, $1, source_observation_set_hash, text_hash, metadata_hash,
		 conversion_recipe_digest, normalization_recipe_digest, parser_recipe_digest, policy_recipe_digest,
		 normalized_number, document_type, 'version-b', intake_execution_id, intake_execution_key
		FROM silver.document_revision WHERE id = $2`, otherKey, seed.revision); err != nil { t.Fatal(err) }
	target := ConversionTarget{RevisionKey: otherKey, CandidateDigest: conversionOtherDigest}
	before := conversionAdapterCounts(t, f)
	for _, write := range []func() error{
		func() error { _, err := c.RegisterConversionArtifact(f.ctx, g, target, conversionAdapterArtifact()); return err },
		func() error { _, err := c.InsertOffsetMapSegments(f.ctx, g, target, conversionAdapterAsset(), conversionAdapterAsset(), []ConversionSegment{conversionAdapterSegment(0)}); return err },
		func() error { _, err := c.AppendEditLedgerEntry(f.ctx, g, target, conversionAdapterEdit()); return err },
		func() error { _, err := c.RecordConversionQualityReport(f.ctx, g, target, conversionAdapterReport()); return err },
		func() error { _, err := c.RecordRevisionComparison(f.ctx, g, target, ConversionComparisonInput{PriorReportID: 1, CandidateReportID: 2,
			PriorRevisionKey: revisionKey, PriorCandidateDigest: identityDigest, RecipeDigest: identityDigest, Result: conversionAdapterBlob(identityDigest)}); return err },
	} { if err := write(); !errors.Is(err, run.ErrStaleGrant) || before != conversionAdapterCounts(t, f) { t.Fatalf("wrong version wrote rows: %v", err) } }
	v := conversionAdapterArtifact()
	v.OriginalHash = model.Digest(strings.Repeat("d", 64))
	_, err := c.RegisterConversionArtifact(f.ctx, g, conversionAdapterTarget(), v)
	var rejection *RejectionError
	if !errors.As(err, &rejection) || before != conversionAdapterCounts(t, f) { t.Fatalf("dangling blob wrote rows: %v", err) }
	v = conversionAdapterArtifact(); v.Blob.ObjectRef = "unregistered"
	_, err = c.RegisterConversionArtifact(f.ctx, g, conversionAdapterTarget(), v)
	if !errors.As(err, &rejection) || before != conversionAdapterCounts(t, f) { t.Fatalf("forged blob locator wrote rows: %v", err) }
	for _, size := range []int64{15, 17} {
		v = conversionAdapterArtifact(); v.Blob.ByteSize = size; v.Members[0].ByteSize = size
		_, err = c.RegisterConversionArtifact(f.ctx, g, conversionAdapterTarget(), v)
		if !errors.As(err, &rejection) || before != conversionAdapterCounts(t, f) { t.Fatalf("catalog size mismatch %d: %v", size, err) }
	}
	bad := ConversionComparisonInput{PriorReportID: 9999, CandidateReportID: 9998, PriorRevisionKey: revisionKey,
		PriorCandidateDigest: conversionOtherDigest, RecipeDigest: identityDigest, Result: conversionAdapterBlob(identityDigest)}
	_, err = c.RecordRevisionComparison(f.ctx, g, conversionAdapterTarget(), bad)
	if !errors.As(err, &rejection) || before != conversionAdapterCounts(t, f) { t.Fatalf("dangling comparison reports wrote rows: %v", err) }
	for _, write := range []func() error{
		func() error { v := conversionAdapterEdit(); v.LedgerHash = model.Digest(strings.Repeat("d", 64)); _, err := c.AppendEditLedgerEntry(f.ctx, g, conversionAdapterTarget(), v); return err },
		func() error { v := conversionAdapterSegment(0); v.MapHash = model.Digest(strings.Repeat("d", 64)); _, err := c.InsertOffsetMapSegments(f.ctx, g, conversionAdapterTarget(), conversionAdapterAsset(), conversionAdapterAsset(), []ConversionSegment{v}); return err },
		func() error { v := conversionAdapterReport(); v.Report = conversionAdapterBlob(strings.Repeat("d", 64)); _, err := c.RecordConversionQualityReport(f.ctx, g, conversionAdapterTarget(), v); return err },
	} { if err := write(); !errors.As(err, &rejection) || before != conversionAdapterCounts(t, f) { t.Fatalf("dangling provenance blob wrote rows: %v", err) } }
}

// A complete child set commits with its report; a changed child conflicts
// without adding a second report or changing either retained child.
func TestConversionAdapterReportAtomicity(t *testing.T) {
	f, _, c, g, _ := conversionAdapterFixture(t)
	v := conversionAdapterReport()
	v.Records = append(v.Records, v.Records[0]); v.Records[1].Ordinal = 1; v.RecordCount = 2
	row, err := c.RecordConversionQualityReport(f.ctx, g, conversionAdapterTarget(), v)
	if err != nil { t.Fatal(err) }
	counts := conversionAdapterCounts(t, f)
	if counts[3] != 1 || counts[4] != 2 { t.Fatalf("report did not commit complete child set: %v", counts) }
	bad := v; bad.Records = append([]ConversionQualityRecord{}, v.Records...); bad.Records[1].Code = "CHANGED"
	_, err = c.RecordConversionQualityReport(f.ctx, g, conversionAdapterTarget(), bad)
	if !errors.Is(err, run.ErrPayloadConflict) || counts != conversionAdapterCounts(t, f) { t.Fatalf("child conflict was not atomic: %v", err) }
	findings, err := c.ReadConversionQualityFindings(f.ctx, g, conversionAdapterTarget(), row.ID, 0, 1)
	if err != nil || len(findings) != 1 { t.Fatalf("findings bound: %v", err) }
	remaining, err := c.ReadConversionQualityFindings(f.ctx, g, conversionAdapterTarget(), row.ID, findings[0].ID, 1)
	if err != nil || len(remaining) != 1 { t.Fatalf("findings cursor: %v", err) }
}

func TestConversionAdapterEmptyReportRollsBackChildren(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty-result", true: "wrong-returned-count"}[corrupt], func(t *testing.T) {
			f, app, _, g, _ := conversionAdapterFixture(t)
			s := &conversionEmptyStarter{conn: app, t: t, force: 1, mutation: "RecordConversionQualityReport", countTable: "quality_report", corruptCount: corrupt}
			c, err := NewConversion(s, "writer-a"); if err != nil { t.Fatal(err) }
			row, err := c.RecordConversionQualityReport(f.ctx, g, conversionAdapterTarget(), conversionAdapterReport())
			if err != nil || row.ID <= 0 || s.begins != 2 { t.Fatalf("empty report retry: %v", err) }
			counts := conversionAdapterCounts(t, f)
			if counts[3] != 1 || counts[4] != 1 { t.Fatalf("empty report leaked partial children: %v", counts) }
		})
	}
}


func TestConversionAdapterNestedJSONRoundTrip(t *testing.T) {
	f, _, c, g, _ := conversionAdapterFixture(t)
	v := conversionAdapterEdit()
	v.EncodingEvidence = []byte(`{"nested":{"a":1,"b":2},"list":[1]}`)
	v.SourceAnchors = []byte(`[{"page":1,"object":"body"}]`)
	first, err := c.AppendEditLedgerEntry(f.ctx, g, conversionAdapterTarget(), v); if err != nil { t.Fatal(err) }
	before := conversionAdapterCounts(t, f)
	for _, number := range []string{"1", "1.0", "1e0"} {
		replay := v; replay.EncodingEvidence = []byte(`{"list":[`+number+`],"nested":{"b":2.0,"a":`+number+`}}`)
		r, err := c.AppendEditLedgerEntry(f.ctx, g, conversionAdapterTarget(), replay)
		if err != nil || !reflect.DeepEqual(first, r) || before != conversionAdapterCounts(t, f) { t.Fatalf("numeric replay changed receipt: %v", err) }
	}
	for _, raw := range []string{`{"nested":{"a":1},"list":[1]}`, `{"nested":{"a":1,"b":3},"list":[1]}`} {
		changed := v; changed.EncodingEvidence = []byte(raw)
		_, err := c.AppendEditLedgerEntry(f.ctx, g, conversionAdapterTarget(), changed)
		if !errors.Is(err, run.ErrPayloadConflict) || before != conversionAdapterCounts(t, f) { t.Fatalf("nested change retained rows: %v", err) }
	}
	changed := v; changed.SourceAnchors = []byte(`[{"page":1}]`)
	_, err = c.AppendEditLedgerEntry(f.ctx, g, conversionAdapterTarget(), changed)
	if !errors.Is(err, run.ErrPayloadConflict) || before != conversionAdapterCounts(t, f) { t.Fatalf("nested anchor removal retained rows: %v", err) }
	rows, err := c.ReadEditLedgerByHashes(f.ctx, g, conversionAdapterTarget(), identityDigest, identityDigest, 0, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != first.ID || before[2] != 1 { t.Fatalf("ledger round trip: %v", err) }
}

func TestConversionAdapterCoordinateRoundTrip(t *testing.T) {
	f, _, c, g, _ := conversionAdapterFixture(t)
	spaces := []model.CoordinateSpace{model.OriginalBytes, model.DecodedMember, model.RawText, model.NormalizedText}
	want := []model.ProvenanceSpace{model.ProvenanceOriginalBytes, model.ProvenanceMemberBytes, model.ProvenanceRawText, model.ProvenanceNormalizedText}
	var items []ConversionSegment
	for i, space := range spaces {
		v := conversionAdapterSegment(int32(i)); v.Source.Space = space; v.Destination.Space = space
		v.Source.Range = &model.ByteRange{Start: 16, End: 16}
		v.Destination.Range = nil; v.Destination.ReasonCode = "BYTE_MAPPING_UNAVAILABLE"
		switch i {
		case 0: page := 1; v.Destination.Page = &page
		case 1: v.Destination.Member = "word/document.xml"
		default: v.Destination.Object = "body"
		}
		items = append(items, v)
	}
	receipts, err := c.InsertOffsetMapSegments(f.ctx, g, conversionAdapterTarget(), conversionAdapterAsset(), conversionAdapterAsset(), items)
	if err != nil || len(receipts) != 4 { t.Fatalf("coordinate write: %v", err) }
	before := conversionAdapterCounts(t, f)
	replay, err := c.InsertOffsetMapSegments(f.ctx, g, conversionAdapterTarget(), conversionAdapterAsset(), conversionAdapterAsset(), items)
	if err != nil || !reflect.DeepEqual(receipts, replay) || before != conversionAdapterCounts(t, f) { t.Fatalf("coordinate replay: %v", err) }
	rows, err := c.ReadOffsetMapSegments(f.ctx, g, revisionKey, "block-a", 0, 4)
	if err != nil || len(rows) != 4 || before[1] != 4 { t.Fatalf("coordinate read: %v", err) }
	for _, row := range rows {
		i := int(row.SegmentOrdinal)
		if i < 0 || i >= len(items) { t.Fatalf("unexpected segment ordinal: %d", i) }
		if row.ID != receipts[i].ID || row.SourceSpace != string(want[i]) || row.DestinationSpace != string(want[i]) || row.SourceStart == nil || *row.SourceStart != 16 || row.SourceEnd == nil || *row.SourceEnd != 16 || row.DestinationStart != nil || row.DestinationEnd != nil || row.DestinationReason == nil || *row.DestinationReason != "BYTE_MAPPING_UNAVAILABLE" {
			t.Fatalf("coordinate %d lost translation or invented range: %+v", i, row)
		}
		if i == 0 && (row.DestinationPage == nil || *row.DestinationPage != 1) { t.Fatal("page anchor changed") }
		if i == 1 && (row.DestinationMember == nil || *row.DestinationMember != items[i].Destination.Member) { t.Fatal("member anchor changed") }
		if i >= 2 && (row.DestinationObject == nil || *row.DestinationObject != items[i].Destination.Object) { t.Fatal("object anchor changed") }
	}
}

func TestConversionAdapterQualityReferenceRoundTrip(t *testing.T) {
	f, _, c, g, _ := conversionAdapterFixture(t)
	v := conversionAdapterReport()
	var err error
	v.Records[0].Evidence, err = json.Marshal([]model.AssetRef{conversionAdapterAsset()}); if err != nil { t.Fatal(err) }
	span := conversionAdapterSegment(0).Source
	v.Records[0].SourceSpans, err = json.Marshal([]model.Span{span}); if err != nil { t.Fatal(err) }
	first, err := c.RecordConversionQualityReport(f.ctx, g, conversionAdapterTarget(), v); if err != nil { t.Fatal(err) }
	before := conversionAdapterCounts(t, f)
	replay, err := c.RecordConversionQualityReport(f.ctx, g, conversionAdapterTarget(), v)
	if err != nil || !reflect.DeepEqual(first, replay) || before != conversionAdapterCounts(t, f) { t.Fatalf("quality reference replay: %v", err) }
	rows, err := c.ReadConversionQualityFindings(f.ctx, g, conversionAdapterTarget(), first.ID, 0, 1)
	if err != nil || len(rows) != 1 { t.Fatalf("quality reference read: %v", err) }
	var assets []model.AssetRef
	var spans []model.Span
	if err := json.Unmarshal(rows[0].Evidence, &assets); err != nil || !reflect.DeepEqual(assets, []model.AssetRef{conversionAdapterAsset()}) { t.Fatalf("evidence changed: %v", err) }
	if err := json.Unmarshal(rows[0].SourceSpans, &spans); err != nil || !reflect.DeepEqual(spans, []model.Span{span}) { t.Fatalf("spans changed: %v", err) }
	for _, size := range []int64{15, 17} {
		bad := conversionAdapterReport(); bad.Report = conversionAdapterBlob(conversionOtherDigest)
		bad.Records[0].Evidence, err = json.Marshal([]model.AssetRef{{Path: "synthetic", SHA256: identityDigest, ByteSize: size}}); if err != nil { t.Fatal(err) }
		_, err = c.RecordConversionQualityReport(f.ctx, g, conversionAdapterTarget(), bad)
		var rejection *RejectionError
		if !errors.As(err, &rejection) || before != conversionAdapterCounts(t, f) { t.Fatalf("evidence catalog size mismatch wrote rows: %v", err) }
	}
	bad := conversionAdapterReport(); bad.Report = conversionAdapterBlob(conversionOtherDigest)
	bad.Records[0].Evidence, err = json.Marshal([]model.AssetRef{{Path: "missing", SHA256: model.Digest(strings.Repeat("d", 64)), ByteSize: 16}}); if err != nil { t.Fatal(err) }
	_, err = c.RecordConversionQualityReport(f.ctx, g, conversionAdapterTarget(), bad)
	var rejection *RejectionError
	if !errors.As(err, &rejection) || before != conversionAdapterCounts(t, f) { t.Fatalf("missing evidence blob wrote rows: %v", err) }
}

func TestConversionAdapterPreRevisionComparison(t *testing.T) {
	f, _, c, g, _ := conversionAdapterFixture(t)
	priorTarget := ConversionTarget{RevisionKey: identityKey+":rev:"+conversionOtherDigest, CandidateDigest: conversionOtherDigest}
	target := ConversionTarget{RevisionKey: identityKey+":rev:"+conversionNewDigest, CandidateDigest: conversionNewDigest}
	prior, err := c.RecordConversionQualityReport(f.ctx, g, priorTarget, conversionAdapterReport()); if err != nil { t.Fatal(err) }
	candidate, err := c.RecordConversionQualityReport(f.ctx, g, target, conversionAdapterReport()); if err != nil { t.Fatal(err) }
	for _, entry := range []struct{ target ConversionTarget; id int64 }{{priorTarget, prior.ID}, {target, candidate.ID}} {
		rows, err := c.ReadConversionQualityReports(f.ctx, g, entry.target, 0, 1)
		if err != nil || len(rows) != 1 || rows[0].ID != entry.id || rows[0].DocumentRevisionID != nil { t.Fatalf("reserved report parent: %v", err) }
		findings, err := c.ReadConversionQualityFindings(f.ctx, g, entry.target, entry.id, 0, 1)
		if err != nil || len(findings) != 1 || findings[0].DocumentRevisionID != nil || findings[0].QualityReportID != entry.id { t.Fatalf("reserved finding parent: %v", err) }
	}
	v := ConversionComparisonInput{PriorReportID: prior.ID, CandidateReportID: candidate.ID, PriorRevisionKey: priorTarget.RevisionKey,
		PriorCandidateDigest: priorTarget.CandidateDigest, RecipeDigest: identityDigest, Result: conversionAdapterBlob(identityDigest)}
	first := conversionAdapterReplay(t, f, func() (ConversionReceipt, error) { return c.RecordRevisionComparison(f.ctx, g, target, v) }, func() error {
		changed := v; changed.Result = conversionAdapterBlob(conversionOtherDigest)
		_, err := c.RecordRevisionComparison(f.ctx, g, target, changed); return err
	})
	rows, err := c.ReadRevisionComparisons(f.ctx, g, priorTarget.CandidateDigest, target.CandidateDigest, 0, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != first.ID || rows[0].DocumentRevisionID != nil { t.Fatalf("reserved comparison parent: %v", err) }
	counts := conversionAdapterCounts(t, f)
	if counts[3] != 2 || counts[4] != 2 || counts[5] != 1 { t.Fatalf("pre-revision row counts: %v", counts) }
	var parents int64
	if err := f.conn.QueryRow(f.ctx, "SELECT count(*) FROM silver.document_revision WHERE revision_key = ANY($1::text[])", []string{priorTarget.RevisionKey, target.RevisionKey}).Scan(&parents); err != nil || parents != 0 { t.Fatalf("comparison fabricated parents: %v", err) }
}

// The winner holds an uncommitted unique key on the fixture connection. The
// losing public operation runs on the app connection. The database lock graph
// is the barrier: commit only after the mutation's snapshot waits on that key.
func conversionWaitForWinner(t *testing.T, ctx context.Context, holder pgx.Tx, app *pgx.Conn, started <-chan struct{}) {
	t.Helper()
	select { case <-started: case <-ctx.Done(): t.Fatal(ctx.Err()) }
	for {
		var blocked bool
		if err := holder.QueryRow(ctx, "SELECT pg_backend_pid() = ANY(pg_blocking_pids($1::integer))", int32(app.PgConn().PID())).Scan(&blocked); err != nil { t.Fatal(err) }
		if blocked { return }
		if ctx.Err() != nil { t.Fatal(ctx.Err()) }
	}
}

func conversionWinnerStarter(t *testing.T, app *pgx.Conn, mutation, table string) *conversionEmptyStarter {
	t.Helper()
	return &conversionEmptyStarter{conn: app, t: t, mutation: mutation, countTable: table, expectedRows: 1,
		mutationStarted: make(chan struct{}), rollbackReady: make(chan struct{}), classify: make(chan struct{})}
}

func conversionReleaseClassification(s *conversionEmptyStarter) {
	select { case <-s.classify: default: close(s.classify) }
}

func TestConversionAdapterInvisibleSegmentWinner(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "identical"; if changed { name = "changed" }
		t.Run(name, func(t *testing.T) {
			f, app, _, g, seed := conversionAdapterFixture(t)
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			t.Cleanup(cancel)
			holder := identityTx(t, f, false)
			transform := "nfc"; if changed { transform = "changed" }
			var winnerID int64
			if err := holder.QueryRow(ctx, `INSERT INTO silver.offset_map_segment
				(document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest,
				 map_hash, source_hash, destination_hash, source_block_id, destination_block_id, source_space, destination_space,
				 source_start, source_end, destination_start, destination_end, transform_order, segment_ordinal, transform)
				VALUES ($1, $2, 'version-a', $3, $4, $4, $4, $4, 'block-a', 'block-b', 'raw_text', 'normalized_text',
				 0, 1, 0, 1, 0, 0, $5) RETURNING id`, seed.revision, revisionKey, executionKey, identityDigest, transform).Scan(&winnerID); err != nil { t.Fatal(err) }
			s := conversionWinnerStarter(t, app, "InsertOffsetMapSegments", "offset_map_segment")
			c, err := NewConversion(s, "writer-a"); if err != nil { t.Fatal(err) }
			type outcome struct { rows []ConversionReceipt; err error }
			done := make(chan outcome, 1)
			go func() {
				rows, err := c.InsertOffsetMapSegments(ctx, g, conversionAdapterTarget(), conversionAdapterAsset(), conversionAdapterAsset(), []ConversionSegment{conversionAdapterSegment(0), conversionAdapterSegment(1)})
				done <- outcome{rows, err}
			}()
			finished := false
			t.Cleanup(func() {
				cancel()
				conversionReleaseClassification(s)
				cleanup, stop := context.WithTimeout(context.Background(), revisionRollbackTimeout); defer stop()
				_ = holder.Rollback(cleanup)
				if !finished { select { case <-done: case <-cleanup.Done(): t.Error("winner writer did not stop") } }
			})
			conversionWaitForWinner(t, ctx, holder, app, s.mutationStarted)
			if err := holder.Commit(ctx); err != nil { t.Fatal(err) }
			select { case <-s.rollbackReady: case <-ctx.Done(): t.Fatal(ctx.Err()) }
			var siblings int64
			if err := f.conn.QueryRow(ctx, "SELECT count(*) FROM silver.offset_map_segment WHERE segment_ordinal = 1").Scan(&siblings); err != nil || siblings != 0 { t.Fatalf("partial sibling survived rollback: %v", err) }
			if counts := conversionAdapterCounts(t, f); counts[1] != 1 { t.Fatalf("rollback lost winner or retained sibling: %v", counts) }
			conversionReleaseClassification(s)
			var result outcome
			select { case result = <-done: finished = true; case <-ctx.Done(): t.Fatal(ctx.Err()) }
			if s.begins != 2 || (changed && s.inserts != 1) || (!changed && s.inserts != 2) || !s.last.rolledBack { t.Fatalf("winner classification transaction sequence: %+v", s) }
			if changed {
				if !errors.Is(result.err, run.ErrPayloadConflict) || conversionAdapterCounts(t, f)[1] != 1 { t.Fatalf("changed invisible winner wrote sibling: %v", result.err) }
			} else {
				if result.err != nil || len(result.rows) != 2 || result.rows[0].ID != winnerID || conversionAdapterCounts(t, f)[1] != 2 { t.Fatalf("identical winner receipt: %v", result.err) }
				plain, err := NewConversion(app, "writer-a"); if err != nil { t.Fatal(err) }
				original, err := plain.InsertOffsetMapSegments(ctx, g, conversionAdapterTarget(), conversionAdapterAsset(), conversionAdapterAsset(), []ConversionSegment{conversionAdapterSegment(0)})
				if err != nil || len(original) != 1 || !reflect.DeepEqual(original[0], result.rows[0]) { t.Fatalf("winner receipt changed on retry: %v", err) }
			}
		})
	}
}

func TestConversionAdapterInvisibleComparisonWinner(t *testing.T) {
	for _, name := range []string{"identical", "changed", "stale-before-classification"} {
		t.Run(name, func(t *testing.T) {
			f, app, c, g, seed := conversionAdapterFixture(t)
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			t.Cleanup(cancel)
			prior, err := c.RecordConversionQualityReport(ctx, g, conversionAdapterTarget(), conversionAdapterReport()); if err != nil { t.Fatal(err) }
			target := conversionAdapterTarget(); target.CandidateDigest = conversionOtherDigest
			candidate, err := c.RecordConversionQualityReport(ctx, g, target, conversionAdapterReport()); if err != nil { t.Fatal(err) }
			v := ConversionComparisonInput{PriorReportID: prior.ID, CandidateReportID: candidate.ID, PriorRevisionKey: revisionKey,
				PriorCandidateDigest: identityDigest, RecipeDigest: identityDigest, Result: conversionAdapterBlob(identityDigest)}
			resultHash := identityDigest; if name == "changed" { resultHash = conversionOtherDigest }
			holder := identityTx(t, f, false)
			var winnerID int64
			if err := holder.QueryRow(ctx, `INSERT INTO silver.revision_comparison
				(document_revision_id, revision_key, document_version_key, processing_execution_key, candidate_digest,
				 prior_report_id, candidate_report_id, prior_revision_key, prior_candidate_digest, comparison_recipe_digest,
				 result_hash, result_byte_size, acceptance_watermark)
				VALUES ($1, $2, 'version-a', $3, $4, $5, $6, $2, $7, $7, $8, 16, 0) RETURNING id`,
				seed.revision, revisionKey, executionKey, conversionOtherDigest, prior.ID, candidate.ID, identityDigest, resultHash).Scan(&winnerID); err != nil { t.Fatal(err) }
			s := conversionWinnerStarter(t, app, "RecordRevisionComparison", "revision_comparison")
			c, err = NewConversion(s, "writer-a"); if err != nil { t.Fatal(err) }
			type outcome struct { row ConversionReceipt; err error }
			done := make(chan outcome, 1)
			go func() { row, err := c.RecordRevisionComparison(ctx, g, target, v); done <- outcome{row, err} }()
			finished := false
			t.Cleanup(func() {
				cancel()
				conversionReleaseClassification(s)
				cleanup, stop := context.WithTimeout(context.Background(), revisionRollbackTimeout); defer stop()
				_ = holder.Rollback(cleanup)
				if !finished { select { case <-done: case <-cleanup.Done(): t.Error("comparison writer did not stop") } }
			})
			conversionWaitForWinner(t, ctx, holder, app, s.mutationStarted)
			if err := holder.Commit(ctx); err != nil { t.Fatal(err) }
			select { case <-s.rollbackReady: case <-ctx.Done(): t.Fatal(ctx.Err()) }
			before := conversionAdapterCounts(t, f)
			if before[5] != 1 { t.Fatalf("comparison winner not committed: %v", before) }
			if name == "stale-before-classification" {
				setup := identityTx(t, f, false)
				seedConversionAcceptance(t, ctx, setup, seed, conversionNewDigest, 1)
				if err := setup.Commit(ctx); err != nil { t.Fatal(err) }
			}
			conversionReleaseClassification(s)
			var result outcome
			select { case result = <-done: finished = true; case <-ctx.Done(): t.Fatal(ctx.Err()) }
			if s.begins != 2 || s.inserts != 1 || !s.last.rolledBack || before != conversionAdapterCounts(t, f) { t.Fatalf("comparison retry changed rows: %v", result.err) }
			switch name {
			case "identical":
				if result.err != nil || result.row.ID != winnerID { t.Fatalf("identical comparison winner: %v", result.err) }
				plain, err := NewConversion(app, "writer-a"); if err != nil { t.Fatal(err) }
				original, err := plain.RecordRevisionComparison(ctx, g, target, v)
				if err != nil || !reflect.DeepEqual(original, result.row) { t.Fatalf("comparison winner receipt changed: %v", err) }
			case "changed":
				if !errors.Is(result.err, run.ErrPayloadConflict) { t.Fatalf("changed comparison winner: %v", result.err) }
			default:
				var rejection *RejectionError
				if !errors.As(result.err, &rejection) || rejection.Reason != "conversion_comparison_stale" { t.Fatalf("stale comparison replay accepted: %v", result.err) }
				plain, err := NewConversion(app, "writer-a"); if err != nil { t.Fatal(err) }
				rows, err := plain.ReadRevisionComparisons(ctx, g, identityDigest, conversionOtherDigest, 0, 1)
				if err != nil || len(rows) != 0 { t.Fatalf("stale winner remained eligible: %v", err) }
			}
			var watermark int64
			if err := f.conn.QueryRow(ctx, "SELECT acceptance_watermark FROM silver.revision_comparison WHERE id = $1", winnerID).Scan(&watermark); err != nil || watermark != 0 { t.Fatalf("classification rewrote baseline: %v", err) }
		})
	}
}
