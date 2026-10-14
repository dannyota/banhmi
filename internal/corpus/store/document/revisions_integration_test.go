package document

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/run"
)

func adapterLimits() ProvisionLimits {
	return ProvisionLimits{MaxItems: 10, MaxPayloadBytes: 16384, MaxCitationBytes: 128, MaxAnchorBytes: 256, MaxAnchors: 4}
}

func adapterAssertion() ObservedAssertion {
	return ObservedAssertion{ObservationKey: observationKey, SourceID: "source-a", ExternalID: "external-a",
		NormalizedNumber: "50/2024/TT-NHNN", DocumentType: "thong-tu", TitleHash: identityDigest}
}

func adapterGrant(id int64) Grant {
	return Grant{Execution: run.ScopedExecution{Country: "vn", WorkflowID: "document-ingest/vn/source-a/version-a",
		WorkflowKind: run.DocumentIngest, AttemptKey: "attempt-a", AttemptFence: 1, AdmissionEpoch: 1, Owner: "owner-a",
		Scope: run.ExecutionScope{Kind: run.ScopeIntake, Subject: run.IntakeSubject{Kind: run.SubjectDocumentVersion, DocumentVersion: "version-a"}}},
		ExecutionID: id, InputDigest: identityDigest, RecipeDigest: identityDigest, BudgetDigest: identityDigest}
}

func adapterFixture(t *testing.T) (identityDatabase, *pgx.Conn, *Revisions, Grant) {
	t.Helper()
	f := identityFixture(t)
	seed := identityTx(t, f, false)
	id := seedIdentityExecution(t, f.ctx, seed)
	if err := seed.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	u := *f.u
	u.User = url.User("banhmi_corpus_writer_app")
	app, err := pgx.Connect(f.ctx, u.String())
	if err != nil {
		t.Fatal("connect disposable adapter app failed")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Close(ctx)
	})
	r, err := NewRevisions(app, "writer-a", adapterLimits())
	if err != nil {
		t.Fatal(err)
	}
	return f, app, r, adapterGrant(id)
}

func adapterRevision(id int64) RevisionInput {
	return RevisionInput{DocumentIdentityID: id, RevisionKey: revisionKey, SourceObservationSetHash: identityDigest,
		TextHash: identityDigest, MetadataHash: identityDigest, ConversionRecipeDigest: identityDigest,
		NormalizationRecipeDigest: identityDigest, ParserRecipeDigest: identityDigest, PolicyRecipeDigest: identityDigest,
		NormalizedNumber: "50/2024/TT-NHNN", DocumentType: "thong-tu"}
}

func adapterProvision(local string, ordinal int64) ProvisionInput {
	return ProvisionInput{ProvisionKey: revisionKey + ":provision:" + local, LocalKey: local, Ordinal: ordinal,
		NativeCitation: []byte("Điều 1"), SourceAnchors: json.RawMessage(`[{"page":1,"object":"body"}]`), TextHash: identityDigest}
}

func adapterSeedRevision(t *testing.T, f identityDatabase, r *Revisions, g Grant) RevisionInput {
	t.Helper()
	identity, err := r.MintDocumentIdentity(f.ctx, g, identityKey, adapterAssertion())
	if err != nil {
		t.Fatal(err)
	}
	v := adapterRevision(identity.ID)
	if _, err := r.InsertDocumentRevision(f.ctx, g, v); err != nil {
		t.Fatal(err)
	}
	return v
}

func adapterCounts(t *testing.T, f identityDatabase) [5]int64 {
	t.Helper()
	var counts [5]int64
	for i, table := range []string{"document_identity", "identity_assertion", "legacy_identity_mapping", "document_revision", "provision_revision"} {
		if err := f.conn.QueryRow(f.ctx, "SELECT count(*) FROM silver."+table).Scan(&counts[i]); err != nil {
			t.Fatal(err)
		}
	}
	return counts
}

func TestRevisionsAdapterIdentity(t *testing.T) {
	f, _, r, g := adapterFixture(t)
	first, err := r.MintDocumentIdentity(f.ctx, g, identityKey, adapterAssertion())
	if err != nil {
		t.Fatal(err)
	}
	counts := adapterCounts(t, f)
	replay, err := r.MintDocumentIdentity(f.ctx, g, otherIdentityKey, adapterAssertion())
	if err != nil || replay != first || adapterCounts(t, f) != counts {
		t.Fatalf("mint replay changed receipt or rows: %v", err)
	}
	wrong := strings.Replace(identityKey, "vn:", "my:", 1)
	_, err = r.MintDocumentIdentity(f.ctx, g, wrong, adapterAssertion())
	var rejection *RejectionError
	if !errors.As(err, &rejection) || rejection.Reason != "wrong_country" || adapterCounts(t, f) != counts {
		t.Fatalf("wrong-country mint did not reject without writes: %v", err)
	}
	otherObservation := adapterAssertion()
	otherObservation.NormalizedNumber = "51/2024/TT-NHNN"
	other, err := r.MintDocumentIdentity(f.ctx, g, otherIdentityKey, otherObservation)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []model.IdentityAssertionKind{model.IdentityAssertionMerge, model.IdentityAssertionSplit, model.IdentityAssertionContinuity} {
		a := IdentityAssertion{ObservedAssertion: adapterAssertion(), DocumentIdentityID: first.ID,
			Kind: kind, ReviewState: model.IdentityReviewConfirmed, EvidenceHash: identityDigest}
		if kind != model.IdentityAssertionContinuity {
			a.RelatedIdentityID = &other.ID
		}
		receipt, err := r.AppendIdentityAssertion(f.ctx, g, a)
		if err != nil || receipt.ID <= 0 || receipt.IntakeExecutionKey != executionKey {
			t.Fatalf("append %s: %v", kind, err)
		}
		before := adapterCounts(t, f)
		reused, err := r.AppendIdentityAssertion(f.ctx, g, a)
		if err != nil || !reflect.DeepEqual(receipt, reused) || adapterCounts(t, f) != before {
			t.Fatalf("assertion %s replay changed receipt or rows: %v", kind, err)
		}
		// Evidence and review state participate in the natural key. A new
		// assessment appends instead of rewriting the confirmed assertion.
		a.ReviewState = model.IdentityReviewUnresolved
		changed, err := r.AppendIdentityAssertion(f.ctx, g, a)
		if err != nil || changed.ID == receipt.ID {
			t.Fatalf("new assessment did not append: %v", err)
		}
	}
	for _, key := range []string{identityKey, otherIdentityKey} {
		row, err := r.ReadDocumentIdentity(f.ctx, g, key)
		if err != nil || row.IdentityKey != key {
			t.Fatalf("merge or split lost old key: %v", err)
		}
	}
	assertions, err := r.ReadIdentityByAssertion(f.ctx, g, adapterAssertion())
	if err != nil || len(assertions) != 7 {
		t.Fatalf("retained assertion history: count=%d err=%v", len(assertions), err)
	}
}

// Register another synthetic execution after committing seeds. The digest is
// the pinned legacy export input; it never comes from a production export.
func adapterImportGrant(t *testing.T, f identityDatabase, original Grant, digit string) Grant {
	t.Helper()
	g := original
	g.InputDigest = model.Digest(strings.Repeat(digit, 64))
	g.Execution.WorkflowID = "document-ingest/vn/source-a/import-" + digit
	g.Execution.AttemptKey = "import-" + digit
	err := f.conn.QueryRow(f.ctx, `INSERT INTO ingest.execution
		(execution_key, workflow_id, workflow_kind, processing_attempt_key, attempt_fence, admission_epoch,
		 scope_kind, resource_kind, resource_key, input_digest, recipe_digest, budget_digest, budget, run_receipts)
		VALUES ($1, $2, 'DocumentIngest', $3, 1, 1, 'intake', 'document_version', 'version-a', $4, $5, $5, '{}', '[]') RETURNING id`,
		"execution-"+strings.Repeat(digit, 32), g.Execution.WorkflowID, g.Execution.AttemptKey, string(g.InputDigest), identityDigest).Scan(&g.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRevisionsAdapterLegacyMappings(t *testing.T) {
	f, _, r, g := adapterFixture(t)
	first, err := r.MintDocumentIdentity(f.ctx, g, identityKey, adapterAssertion())
	if err != nil {
		t.Fatal(err)
	}
	obs := adapterAssertion()
	obs.NormalizedNumber = "other-number"
	other, err := r.MintDocumentIdentity(f.ctx, g, otherIdentityKey, obs)
	if err != nil {
		t.Fatal(err)
	}
	second := adapterImportGrant(t, f, g, "b")
	third := adapterImportGrant(t, f, g, "c")
	mapping := LegacyMapping{Country: "vn", ExportManifestDigest: g.InputDigest, EntityKind: model.IdentityEntityDocument,
		LegacyID: "agreement", State: model.LegacyIdentityResolved, DocumentIdentityID: &first.ID}
	receipt, err := r.InsertLegacyIdentityMapping(f.ctx, g, mapping)
	if err != nil {
		t.Fatal(err)
	}
	before := adapterCounts(t, f)
	replay, err := r.InsertLegacyIdentityMapping(f.ctx, g, mapping)
	if err != nil || !reflect.DeepEqual(receipt, replay) || adapterCounts(t, f) != before {
		t.Fatalf("legacy mapping replay: %v", err)
	}
	changed := mapping
	changed.DocumentIdentityID = &other.ID
	if _, err := r.InsertLegacyIdentityMapping(f.ctx, g, changed); !errors.Is(err, run.ErrPayloadConflict) || adapterCounts(t, f) != before {
		t.Fatalf("changed mapping did not conflict: %v", err)
	}
	mapping.ExportManifestDigest = second.InputDigest
	if _, err := r.InsertLegacyIdentityMapping(f.ctx, second, mapping); err != nil {
		t.Fatal(err)
	}
	resolved, err := r.ResolveLegacyIdentity(f.ctx, g, model.IdentityEntityDocument, "agreement")
	if err != nil || resolved.ID != first.ID {
		t.Fatalf("agreeing retained mappings did not resolve: %v", err)
	}
	mapping.ExportManifestDigest, mapping.DocumentIdentityID = third.InputDigest, &other.ID
	if _, err := r.InsertLegacyIdentityMapping(f.ctx, third, mapping); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ResolveLegacyIdentity(f.ctx, g, model.IdentityEntityDocument, "agreement"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("disagreeing retained mappings selected identity: %v", err)
	}
	report := model.Digest(identityDigest)
	for _, state := range []model.LegacyIdentityMappingState{model.LegacyIdentityAmbiguous, model.LegacyIdentityUnavailable} {
		m := LegacyMapping{Country: "vn", ExportManifestDigest: g.InputDigest, EntityKind: model.IdentityEntityDocument, LegacyID: string(state), State: state}
		if state == model.LegacyIdentityAmbiguous {
			m.CandidateReportHash = &report
		}
		if _, err := r.InsertLegacyIdentityMapping(f.ctx, g, m); err != nil {
			t.Fatal(err)
		}
		// One agreeing resolved mapping cannot hide an uncertain mapping.
		m.ExportManifestDigest, m.State, m.DocumentIdentityID, m.CandidateReportHash = second.InputDigest, model.LegacyIdentityResolved, &first.ID, nil
		if _, err := r.InsertLegacyIdentityMapping(f.ctx, second, m); err != nil {
			t.Fatal(err)
		}
		if _, err := r.ResolveLegacyIdentity(f.ctx, g, model.IdentityEntityDocument, string(state)); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("uncertain mapping selected identity: %v", err)
		}
		rows, err := r.ReadLegacyIdentityMappings(f.ctx, g, model.IdentityEntityDocument, string(state))
		if err != nil || len(rows) != 2 || rows[0].DocumentIdentityID != nil || rows[0].State != string(state) {
			t.Fatalf("uncertain mapping was hidden: %v", err)
		}
		if state == model.LegacyIdentityAmbiguous && (rows[0].CandidateReportHash == nil || *rows[0].CandidateReportHash != identityDigest) {
			t.Fatal("ambiguity lost its hash-pinned candidate report")
		}
	}
	for _, mutate := range []func(*LegacyMapping){
		func(m *LegacyMapping) { m.Country = "my" },
		func(m *LegacyMapping) { m.ExportManifestDigest = "not-a-digest" },
		func(m *LegacyMapping) { m.ExportManifestDigest = second.InputDigest },
		func(m *LegacyMapping) { m.State = model.LegacyIdentityAmbiguous },
		func(m *LegacyMapping) { m.State = model.LegacyIdentityUnavailable },
		func(m *LegacyMapping) { m.DocumentIdentityID = nil },
		func(m *LegacyMapping) { m.CandidateReportHash = &report },
	} {
		bad := changed
		mutate(&bad)
		before := adapterCounts(t, f)
		if _, err := r.InsertLegacyIdentityMapping(f.ctx, g, bad); err == nil || adapterCounts(t, f) != before {
			t.Fatalf("invalid mapping wrote rows: %v", err)
		}
	}
}

func TestRevisionsAdapterDocumentReplay(t *testing.T) {
	f, _, r, g := adapterFixture(t)
	v := adapterSeedRevision(t, f, r, g)
	stored, err := r.ReadDocumentRevision(f.ctx, g, revisionKey)
	if err != nil {
		t.Fatal(err)
	}
	before := adapterCounts(t, f)
	replay, err := r.InsertDocumentRevision(f.ctx, g, v)
	if err != nil || replay != stored || adapterCounts(t, f) != before {
		t.Fatalf("revision replay changed original receipt: %v", err)
	}
	v.MetadataHash = model.Digest(strings.Repeat("b", 64))
	if _, err := r.InsertDocumentRevision(f.ctx, g, v); !errors.Is(err, run.ErrPayloadConflict) || adapterCounts(t, f) != before {
		t.Fatalf("changed metadata reused a revision: %v", err)
	}
	// A valid second lease cannot reuse the old revision under another version.
	if _, err := f.conn.Exec(f.ctx, `INSERT INTO ingest.execution_lease
		(resource_kind, resource_key, owner, attempt_fence, admission_epoch, expires_at)
		VALUES ('document_version', 'version-b', 'owner-a', 1, 1, clock_timestamp() + interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	other := g
	other.Execution.WorkflowID = "document-ingest/vn/source-a/version-b"
	other.Execution.Scope.Subject.DocumentVersion = "version-b"
	if err := f.conn.QueryRow(f.ctx, `INSERT INTO ingest.execution
		(execution_key, workflow_id, workflow_kind, processing_attempt_key, attempt_fence, admission_epoch,
		 scope_kind, resource_kind, resource_key, input_digest, recipe_digest, budget_digest, budget, run_receipts)
		VALUES ('execution-22222222222222222222222222222222', $1, 'DocumentIngest', 'attempt-a', 1, 1,
		'intake', 'document_version', 'version-b', $2, $2, $2, '{}', '[]') RETURNING id`,
		other.Execution.WorkflowID, identityDigest).Scan(&other.ExecutionID); err != nil {
		t.Fatal(err)
	}
	v.MetadataHash = identityDigest
	if _, err := r.InsertDocumentRevision(f.ctx, other, v); !errors.Is(err, run.ErrPayloadConflict) || adapterCounts(t, f) != before {
		t.Fatalf("cross-version replay reused a revision: %v", err)
	}
	if _, err := r.InsertProvisionRevisions(f.ctx, other, revisionKey, []ProvisionInput{adapterProvision("article-1", 0)}); !errors.Is(err, run.ErrStaleGrant) || adapterCounts(t, f) != before {
		t.Fatalf("cross-version provision insert: %v", err)
	}
}

func TestRevisionsAdapterProvisionReplayAndContinuity(t *testing.T) {
	f, _, r, g := adapterFixture(t)
	v := adapterSeedRevision(t, f, r, g)
	items := []ProvisionInput{adapterProvision("article-1", 0), adapterProvision("article-2", 1)}
	items[0].NativeCitation = []byte{0, 255, 10, 42}
	rows, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, items)
	if err != nil || len(rows) != 2 || !reflect.DeepEqual(rows[0].NativeCitation, items[0].NativeCitation) {
		t.Fatalf("bulk insert changed citation bytes: %v", err)
	}
	before := adapterCounts(t, f)
	items[0].SourceAnchors = json.RawMessage(`[{"object":"body", "page":1.0}]`)
	replay, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, items)
	if err != nil || !reflect.DeepEqual(replay, rows) || adapterCounts(t, f) != before {
		t.Fatalf("provision replay changed rows: %v", err)
	}
	changed := append([]ProvisionInput(nil), items...)
	changed[0].TextHash = model.Digest(strings.Repeat("b", 64))
	changed = append(changed, adapterProvision("article-3", 2))
	if _, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, changed); !errors.Is(err, run.ErrPayloadConflict) || adapterCounts(t, f) != before {
		t.Fatalf("changed batch partially inserted rows: %v", err)
	}
	stable := "00000000-0000-0000-0000-000000000003"
	a := IdentityAssertion{ObservedAssertion: adapterAssertion(), DocumentIdentityID: v.DocumentIdentityID,
		Kind: model.IdentityAssertionContinuity, ReviewState: model.IdentityReviewUnreviewed, EvidenceHash: identityDigest}
	unreviewed, err := r.AppendIdentityAssertion(f.ctx, g, a)
	if err != nil {
		t.Fatal(err)
	}
	third := adapterProvision("article-3", 2)
	third.StableProvisionUUID, third.ContinuityAssertionID = &stable, &unreviewed.ID
	before = adapterCounts(t, f)
	if _, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{third}); err == nil || adapterCounts(t, f) != before {
		t.Fatal("unreviewed continuity inserted a stable provision")
	}
	a.ReviewState = model.IdentityReviewConfirmed
	confirmed, err := r.AppendIdentityAssertion(f.ctx, g, a)
	if err != nil {
		t.Fatal(err)
	}
	third.ContinuityAssertionID = &confirmed.ID
	assertions, err := r.ReadIdentityByAssertion(f.ctx, g, adapterAssertion())
	if err != nil || len(assertions) == 0 {
		t.Fatalf("read observed assertion for continuity negative: %v", err)
	}
	otherObservation := adapterAssertion()
	otherObservation.NormalizedNumber = "other-identity"
	other, err := r.MintDocumentIdentity(f.ctx, g, otherIdentityKey, otherObservation)
	if err != nil {
		t.Fatal(err)
	}
	foreign := a
	foreign.DocumentIdentityID = other.ID
	foreignReceipt, err := r.AppendIdentityAssertion(f.ctx, g, foreign)
	if err != nil {
		t.Fatal(err)
	}
	for _, assertionID := range []int64{assertions[0].AssertionID, foreignReceipt.ID, confirmed.ID + 1000} {
		bad := third
		bad.ContinuityAssertionID = &assertionID
		before := adapterCounts(t, f)
		if _, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{bad}); err == nil || adapterCounts(t, f) != before {
			t.Fatalf("invalid continuity wrote provisions: %v", err)
		}
	}
	if rows, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{third}); err != nil || len(rows) != 1 || !rows[0].StableProvisionUuid.Valid {
		t.Fatalf("confirmed continuity failed: %v", err)
	}
	all, err := r.ReadProvisionRevisions(f.ctx, g, revisionKey, -1, MaxProvisionPageItems)
	if err != nil || len(all) != 3 || all[2].Ordinal != 2 {
		t.Fatalf("provision read lost order or rows: %v", err)
	}
}

// This port proves input rejections happen before any transaction or query.
type forbiddenTransactionStarter struct{ calls int }

func (s *forbiddenTransactionStarter) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	s.calls++
	return nil, errors.New("validation reached database")
}

func TestRevisionsAdapterProvisionValidation(t *testing.T) {
	f, _, _, g := adapterFixture(t)
	stable := "00000000-0000-0000-0000-000000000003"
	positive, zero := int64(1), int64(0)
	cases := []struct {
		name  string
		edit  func(*ProvisionInput, *ProvisionLimits)
		extra bool
	}{
		{name: "parent_key", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.ProvisionKey = otherIdentityKey + ":rev:" + identityDigest + ":provision:article-1" }},
		{name: "local_key", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.LocalKey = "Article 1" }},
		{name: "negative_ordinal", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.Ordinal = -1 }},
		{name: "overflow_ordinal", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.Ordinal = 2147483648 }},
		{name: "duplicate_key", extra: true},
		{name: "duplicate_ordinal", extra: true, edit: func(v *ProvisionInput, _ *ProvisionLimits) {
			v.LocalKey = "article-2"
			v.ProvisionKey = revisionKey + ":provision:article-2"
		}},
		{name: "hash_length", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.TextHash = "a" }},
		{name: "hash_grammar", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.TextHash = model.Digest(strings.Repeat("A", 64)) }},
		{name: "citation_bound", edit: func(v *ProvisionInput, l *ProvisionLimits) { v.NativeCitation = make([]byte, l.MaxCitationBytes+1) }},
		{name: "anchor_byte_bound", edit: func(v *ProvisionInput, l *ProvisionLimits) { v.SourceAnchors = json.RawMessage(strings.Repeat(" ", l.MaxAnchorBytes+1)) }},
		{name: "anchor_json", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage(`[{`) }},
		{name: "anchor_utf8", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage{'[', '"', 255, '"', ']'} }},
		{name: "anchor_type", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage(`{}`) }},
		{name: "anchor_null", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage(`null`) }},
		{name: "anchor_item_type", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage(`[1]`) }},
		{name: "anchor_item_bound", edit: func(v *ProvisionInput, l *ProvisionLimits) {
			v.SourceAnchors = json.RawMessage(`[{},{},{},{},{}]`)
			l.MaxAnchors = 4
		}},
		{name: "anchor_duplicate_nul", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage(`[{"a":"\u0000","a":"x"}]`) }},
		{name: "anchor_duplicate_numeric_range", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage(`[{"a":1e999999,"a":1}]`) }},
		{name: "anchor_nul", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage(`[{"object":"\u0000"}]`) }},
		{name: "anchor_numeric_range", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage(`[{"page":1e999999}]`) }},
		{name: "anchor_unpaired_surrogate", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.SourceAnchors = json.RawMessage(`[{"object":"\ud800"}]`) }},
		{name: "stable_without_assertion", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.StableProvisionUUID = &stable }},
		{name: "assertion_without_stable", edit: func(v *ProvisionInput, _ *ProvisionLimits) { v.ContinuityAssertionID = &positive }},
		{name: "stable_uuid_grammar", edit: func(v *ProvisionInput, _ *ProvisionLimits) {
			bad := "invalid"
			v.StableProvisionUUID = &bad
			v.ContinuityAssertionID = &positive
		}},
		{name: "continuity_id_range", edit: func(v *ProvisionInput, _ *ProvisionLimits) {
			v.StableProvisionUUID = &stable
			v.ContinuityAssertionID = &zero
		}},
		{name: "payload_bound", edit: func(_ *ProvisionInput, l *ProvisionLimits) { l.MaxPayloadBytes = 100 }},
		{name: "encoded_payload_bound", edit: func(_ *ProvisionInput, l *ProvisionLimits) { l.MaxPayloadBytes = 250 }},
		{name: "item_bound", extra: true, edit: func(v *ProvisionInput, l *ProvisionLimits) {
			l.MaxItems = 1
			v.LocalKey = "article-2"
			v.ProvisionKey = revisionKey + ":provision:article-2"
			v.Ordinal = 1
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			starter := &forbiddenTransactionStarter{}
			limits := adapterLimits()
			item := adapterProvision("article-1", 0)
			if tc.edit != nil {
				tc.edit(&item, &limits)
			}
			r, err := NewRevisions(starter, "writer-a", limits)
			if err != nil {
				t.Fatal(err)
			}
			items := []ProvisionInput{item}
			if tc.extra {
				items = append(items, adapterProvision("article-1", 0))
			}
			if _, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, items); err == nil || starter.calls != 0 {
				t.Fatalf("invalid input reached query: calls=%d err=%v", starter.calls, err)
			}
		})
	}
}

// Lose the first bulk result after PostgreSQL has inserted real rows. This
// fault models an empty result with partial writes without altering the query.
type emptyBulkStarter struct {
	conn                  *pgx.Conn
	last                  *emptyBulkTx
	begins                int
	inserts               int
	inject                bool
	forceCount            int
	rollbackFailure       error
	expectedAfterRollback int64
	t                     *testing.T
}

func (s *emptyBulkStarter) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if s.last != nil && !s.last.rolledBack {
		s.t.Fatal("fresh transaction began before bulk rollback")
	}
	tx, err := s.conn.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	s.begins++
	if s.begins == 2 {
		var count int64
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM silver.provision_revision").Scan(&count); err != nil || count != s.expectedAfterRollback {
			s.t.Fatalf("first bulk transaction leaked rows: count=%d err=%v", count, err)
		}
	}
	s.last = &emptyBulkTx{Tx: tx, starter: s}
	return s.last, nil
}

type emptyBulkTx struct {
	pgx.Tx
	starter    *emptyBulkStarter
	empty      bool
	rolledBack bool
}

func (tx *emptyBulkTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if tx.empty {
		tx.starter.t.Fatal("statement followed empty bulk result before rollback")
	}
	row := tx.Tx.QueryRow(ctx, query, args...)
	if strings.HasPrefix(query, "-- name: InsertProvisionRevisions") {
		tx.starter.inserts++
		return emptyBulkRow{row: row, tx: tx, force: (tx.starter.inject && tx.starter.inserts == 1) || tx.starter.inserts <= tx.starter.forceCount}
	}
	return row
}

func (tx *emptyBulkTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if tx.empty {
		tx.starter.t.Error("read followed empty bulk result before rollback")
		return nil, errors.New("read before required rollback")
	}
	return tx.Tx.Query(ctx, query, args...)
}

func (tx *emptyBulkTx) Rollback(ctx context.Context) error {
	if tx.starter.rollbackFailure != nil {
		failure := tx.starter.rollbackFailure
		tx.starter.rollbackFailure = nil
		return failure
	}
	err := tx.Tx.Rollback(ctx)
	if err == nil || errors.Is(err, pgx.ErrTxClosed) {
		tx.rolledBack = true
	}
	return err
}

type emptyBulkRow struct {
	row   pgx.Row
	tx    *emptyBulkTx
	force bool
}

func (row emptyBulkRow) Scan(dest ...any) error {
	if err := row.row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			row.tx.empty = true
		}
		return err
	}
	if row.force {
		row.tx.empty = true
		return pgx.ErrNoRows
	}
	return nil
}

func TestRevisionsAdapterBulkEmptyRollsBack(t *testing.T) {
	f, app, r, g := adapterFixture(t)
	adapterSeedRevision(t, f, r, g)
	starter := &emptyBulkStarter{conn: app, t: t, inject: true}
	r, err := NewRevisions(starter, "writer-a", adapterLimits())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{adapterProvision("article-1", 0), adapterProvision("article-2", 1)})
	if err != nil || len(rows) != 2 || starter.begins != 2 || starter.inserts != 2 {
		t.Fatalf("bulk retry failed: begins=%d inserts=%d err=%v", starter.begins, starter.inserts, err)
	}
	if got := adapterCounts(t, f)[4]; got != 2 {
		t.Fatalf("rollback or retry duplicated provisions: %d", got)
	}
}

func TestRevisionsAdapterBulkInvisibleWinner(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed_%t", changed), func(t *testing.T) {
			f, app, r, g := adapterFixture(t)
			v := adapterSeedRevision(t, f, r, g)
			parent, err := r.ReadDocumentRevision(f.ctx, g, v.RevisionKey)
			if err != nil {
				t.Fatal(err)
			}
			holder := identityTx(t, f, false)
			first := adapterProvision("article-1", 0)
			winnerHash := first.TextHash
			if changed {
				winnerHash = model.Digest(strings.Repeat("b", 64))
			}
			// A direct synthetic insert bypasses the adapter's lease lock. Its
			// uncommitted unique key blocks ON CONFLICT in the bulk statement.
			identityExec(t, f.ctx, holder, `INSERT INTO silver.provision_revision
				(document_revision_id, provision_key, local_key, ordinal, native_citation, source_anchors, text_hash)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, parent.ID, first.ProvisionKey, first.LocalKey, first.Ordinal,
				first.NativeCitation, first.SourceAnchors, string(winnerHash))
			starter := &emptyBulkStarter{conn: app, t: t, expectedAfterRollback: 1}
			r, err = NewRevisions(starter, "writer-a", adapterLimits())
			if err != nil {
				t.Fatal(err)
			}
			workCtx, cancel := context.WithCancel(f.ctx)
			type outcome struct {
				count int
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				rows, err := r.InsertProvisionRevisions(workCtx, g, revisionKey, []ProvisionInput{first, adapterProvision("article-2", 1)})
				done <- outcome{count: len(rows), err: err}
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				ctx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				_ = holder.Rollback(ctx)
				select {
				case <-done:
				case <-ctx.Done():
					t.Error("bulk writer did not stop during cleanup")
				}
			})
			for {
				var blocked bool
				if err := holder.QueryRow(f.ctx, `SELECT $1::integer = ANY(pg_blocking_pids($2::integer))`,
					int32(f.conn.PgConn().PID()), int32(app.PgConn().PID())).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case result := <-done:
					t.Fatalf("bulk did not wait for invisible winner: %v", result.err)
				case <-f.ctx.Done():
					t.Fatal(f.ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err := holder.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if changed {
					if !errors.Is(result.err, run.ErrPayloadConflict) || adapterCounts(t, f)[4] != 1 {
						t.Fatalf("changed invisible winner leaked partial batch: %v", result.err)
					}
				} else if result.err != nil || result.count != 2 || adapterCounts(t, f)[4] != 2 {
					t.Fatalf("identical invisible winner did not retry after rollback: %v", result.err)
				}
				if starter.begins != 2 || !starter.last.rolledBack {
					t.Fatalf("bulk did not finish the rollback/retry sequence: begins=%d", starter.begins)
				}
			case <-f.ctx.Done():
				t.Fatal(f.ctx.Err())
			}
		})
	}
}

func TestRevisionsAdapterStaleGrants(t *testing.T) {
	for _, name := range []string{"epoch", "owner", "fence", "execution", "workflow", "attempt", "input", "recipe", "budget", "expired", "cancelled", "writer", "disabled"} {
		t.Run(name, func(t *testing.T) {
			f, _, r, g := adapterFixture(t)
			v := adapterSeedRevision(t, f, r, g)
			item := adapterProvision("article-1", 0)
			if _, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{item}); err != nil {
				t.Fatal(err)
			}
			a := IdentityAssertion{ObservedAssertion: adapterAssertion(), DocumentIdentityID: v.DocumentIdentityID,
				Kind: model.IdentityAssertionContinuity, ReviewState: model.IdentityReviewConfirmed, EvidenceHash: identityDigest}
			if _, err := r.AppendIdentityAssertion(f.ctx, g, a); err != nil {
				t.Fatal(err)
			}
			id := v.DocumentIdentityID
			mapping := LegacyMapping{Country: "vn", ExportManifestDigest: g.InputDigest, EntityKind: model.IdentityEntityDocument,
				LegacyID: "stale", State: model.LegacyIdentityResolved, DocumentIdentityID: &id}
			if _, err := r.InsertLegacyIdentityMapping(f.ctx, g, mapping); err != nil {
				t.Fatal(err)
			}
			before := adapterCounts(t, f)
			newObservation := adapterAssertion()
			newObservation.NormalizedNumber = "fresh-identity"
			newAssertion := a
			newAssertion.ReviewState = model.IdentityReviewUnresolved
			newMapping := mapping
			newMapping.LegacyID = "fresh-mapping"
			newRevision := v
			newRevision.RevisionKey = identityKey + ":rev:" + strings.Repeat("b", 64)
			switch name {
			case "epoch":
				g.Execution.AdmissionEpoch++
			case "owner":
				g.Execution.Owner = "owner-b"
			case "fence":
				g.Execution.AttemptFence++
			case "workflow":
				g.Execution.WorkflowID = "document-ingest/vn/source-a/forged"
			case "attempt":
				g.Execution.AttemptKey = "attempt-forged"
			case "execution":
				g.ExecutionID += 1000
			case "input":
				g.InputDigest = model.Digest(strings.Repeat("b", 64))
				mapping.ExportManifestDigest, newMapping.ExportManifestDigest = g.InputDigest, g.InputDigest
			case "recipe":
				g.RecipeDigest = model.Digest(strings.Repeat("b", 64))
			case "budget":
				g.BudgetDigest = model.Digest(strings.Repeat("b", 64))
			case "writer":
				r.writer = "writer-b"
			case "expired", "cancelled", "disabled":
				query := map[string]string{"expired": "UPDATE ingest.execution_lease SET expires_at = clock_timestamp() - interval '1 second'",
					"cancelled": "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()", "disabled": "UPDATE ingest.country_admission SET enabled = false"}[name]
				if _, err := f.conn.Exec(f.ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			operations := []func() error{
				func() error {
					_, err := r.MintDocumentIdentity(f.ctx, g, otherIdentityKey, newObservation)
					return err
				},
				func() error {
					_, err := r.AppendIdentityAssertion(f.ctx, g, newAssertion)
					return err
				},
				func() error {
					_, err := r.InsertLegacyIdentityMapping(f.ctx, g, newMapping)
					return err
				},
				func() error {
					_, err := r.InsertDocumentRevision(f.ctx, g, newRevision)
					return err
				},
				func() error {
					_, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{adapterProvision("article-2", 1)})
					return err
				},
				func() error {
					_, err := r.MintDocumentIdentity(f.ctx, g, otherIdentityKey, adapterAssertion())
					return err
				},
				func() error {
					_, err := r.AppendIdentityAssertion(f.ctx, g, a)
					return err
				},
				func() error {
					_, err := r.InsertLegacyIdentityMapping(f.ctx, g, mapping)
					return err
				},
				func() error {
					_, err := r.InsertDocumentRevision(f.ctx, g, v)
					return err
				},
				func() error {
					_, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{item})
					return err
				},
				func() error {
					_, err := r.ReadDocumentIdentity(f.ctx, g, identityKey)
					return err
				},
				func() error {
					_, err := r.ReadDocumentRevision(f.ctx, g, revisionKey)
					return err
				},
				func() error {
					_, err := r.ReadIdentityByAssertion(f.ctx, g, adapterAssertion())
					return err
				},
				func() error {
					_, err := r.ResolveLegacyIdentity(f.ctx, g, model.IdentityEntityDocument, "stale")
					return err
				},
				func() error {
					_, err := r.ReadLegacyIdentityMappings(f.ctx, g, model.IdentityEntityDocument, "stale")
					return err
				},
				func() error {
					_, err := r.ReadProvisionRevisions(f.ctx, g, revisionKey, -1, MaxProvisionPageItems)
					return err
				},
			}
			for i, operation := range operations {
				if err := operation(); !errors.Is(err, run.ErrStaleGrant) || adapterCounts(t, f) != before {
					t.Fatalf("stale operation %d wrote or returned wrong classification: %v", i, err)
				}
			}
		})
	}
}

func TestRevisionsAdapterRuntimeImmutable(t *testing.T) {
	f, app, r, g := adapterFixture(t)
	v := adapterSeedRevision(t, f, r, g)
	id := v.DocumentIdentityID
	if _, err := r.InsertLegacyIdentityMapping(f.ctx, g, LegacyMapping{Country: "vn", ExportManifestDigest: g.InputDigest,
		EntityKind: model.IdentityEntityDocument, LegacyID: "runtime", State: model.LegacyIdentityResolved, DocumentIdentityID: &id}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{adapterProvision("article-1", 0)}); err != nil {
		t.Fatal(err)
	}
	before := adapterCounts(t, f)
	tx, err := app.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	})
	for _, table := range []string{"document_identity", "identity_assertion", "legacy_identity_mapping", "document_revision", "provision_revision"} {
		identityState(t, f.ctx, tx, "42501", fmt.Sprintf("UPDATE silver.%s SET id = id", table))
		identityState(t, f.ctx, tx, "42501", "DELETE FROM silver."+table)
	}
	if adapterCounts(t, f) != before {
		t.Fatal("runtime changed immutable evidence")
	}
}

func TestRevisionsAdapterBulkExhaustionAndRollbackFailure(t *testing.T) {
	for _, failRollback := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback-failure-%t", failRollback), func(t *testing.T) {
			f, app, r, g := adapterFixture(t)
			adapterSeedRevision(t, f, r, g)
			before := adapterCounts(t, f)
			failure := errors.New("injected rollback failure")
			starter := &emptyBulkStarter{conn: app, t: t, forceCount: 2}
			if failRollback {
				starter.rollbackFailure = failure
			}
			r, err := NewRevisions(starter, "writer-a", adapterLimits())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			rows, err := r.InsertProvisionRevisions(ctx, g, revisionKey, []ProvisionInput{adapterProvision("article-1", 0)})
			if len(rows) != 0 || adapterCounts(t, f) != before || errors.Is(err, errBulkEmpty) || !starter.last.rolledBack {
				t.Fatalf("empty result leaked evidence or retry sentinel: %v", err)
			}
			if failRollback {
				var cause databaseCause
				if !errors.Is(err, failure) || !errors.As(err, &cause) || errors.Is(err, run.ErrPayloadConflict) || starter.begins != 1 || starter.inserts != 1 {
					t.Fatalf("failed rollback retried or lost error: begins=%d inserts=%d err=%v", starter.begins, starter.inserts, err)
				}
			} else if !errors.Is(err, run.ErrPayloadConflict) || starter.begins != 3 || starter.inserts != 2 {
				t.Fatalf("exhaustion did not end in read-only classification: begins=%d inserts=%d err=%v", starter.begins, starter.inserts, err)
			}
		})
	}
}

func TestRevisionsAdapterObservationOwnership(t *testing.T) {
	for _, name := range []string{"missing", "foreign", "source", "external", "version"} {
		t.Run(name, func(t *testing.T) {
			f, _, r, g := adapterFixture(t)
			v := adapterSeedRevision(t, f, r, g)
			a := IdentityAssertion{ObservedAssertion: adapterAssertion(), DocumentIdentityID: v.DocumentIdentityID,
				Kind: model.IdentityAssertionContinuity, ReviewState: model.IdentityReviewConfirmed, EvidenceHash: identityDigest}
			if _, err := r.AppendIdentityAssertion(f.ctx, g, a); err != nil {
				t.Fatal(err)
			}
			other := adapterSecondGrant(t, f, g)
			bad := adapterAssertion()
			switch name {
			case "missing":
				bad.ObservationKey = "observation-33333333333333333333333333333333"
			case "foreign":
				bad.ObservationKey = "observation-22222222222222222222222222222222"
				foreign, err := r.MintDocumentIdentity(f.ctx, other, otherIdentityKey, bad)
				if err != nil {
					t.Fatal(err)
				}
				a.ObservedAssertion, a.DocumentIdentityID = bad, foreign.ID
				if _, err := r.AppendIdentityAssertion(f.ctx, other, a); err != nil {
					t.Fatal(err)
				}
			case "source":
				bad.SourceID = "source-b"
			case "external":
				bad.ExternalID = "external-b"
			case "version":
				g = other
			}
			before := adapterCounts(t, f)
			ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			if _, err := r.MintDocumentIdentity(ctx, g, otherIdentityKey, bad); !errors.Is(err, run.ErrStaleGrant) || adapterCounts(t, f) != before {
				t.Fatalf("mint accepted unowned observation: %v", err)
			}
			a.ObservedAssertion = bad
			if _, err := r.AppendIdentityAssertion(ctx, g, a); !errors.Is(err, run.ErrStaleGrant) || adapterCounts(t, f) != before {
				t.Fatalf("assertion replay accepted unowned observation: %v", err)
			}
		})
	}
}

// A second valid execution and observation use an independent document lease.
func adapterSecondGrant(t *testing.T, f identityDatabase, g Grant) Grant {
	t.Helper()
	if _, err := f.conn.Exec(f.ctx, `INSERT INTO ingest.execution_lease
		(resource_kind, resource_key, owner, attempt_fence, admission_epoch, expires_at)
		VALUES ('document_version', 'version-b', 'owner-b', 1, 1, clock_timestamp() + interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	g.Execution.WorkflowID = "document-ingest/vn/source-a/version-b"
	g.Execution.Scope.Subject.DocumentVersion = "version-b"
	g.Execution.Owner, g.Execution.AttemptKey = "owner-b", "attempt-b"
	if err := f.conn.QueryRow(f.ctx, `INSERT INTO ingest.execution
		(execution_key, workflow_id, workflow_kind, processing_attempt_key, attempt_fence, admission_epoch,
		 scope_kind, resource_kind, resource_key, input_digest, recipe_digest, budget_digest, budget, run_receipts)
		VALUES ('execution-22222222222222222222222222222222', $1, 'DocumentIngest', 'attempt-b', 1, 1,
		'intake', 'document_version', 'version-b', $2, $2, $2, '{}', '[]') RETURNING id`,
		g.Execution.WorkflowID, identityDigest).Scan(&g.ExecutionID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conn.Exec(f.ctx, `INSERT INTO bronze.observation_revision
		(observation_key, source_record_id, observation_digest, metadata_digest, source_tree_digest,
		 relations_digest, source_window_key, document_version_key, execution_key, result_key, capture_blob_id, observed_at)
		SELECT 'observation-22222222222222222222222222222222', source_record_id, $1, metadata_digest,
		 source_tree_digest, relations_digest, source_window_key, 'version-b',
		 'execution-22222222222222222222222222222222', 'result-22222222222222222222222222222222', capture_blob_id, clock_timestamp()
		FROM bronze.observation_revision WHERE observation_key = $2`, strings.Repeat("b", 64), observationKey); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRevisionsAdapterProvisionPaginationAndReceipts(t *testing.T) {
	f, app, r, g := adapterFixture(t)
	adapterSeedRevision(t, f, r, g)
	for batch := 0; batch < 3; batch++ {
		items := make([]ProvisionInput, 10)
		for i := range items {
			ordinal := int64(batch*10 + i)
			items[i] = adapterProvision(fmt.Sprintf("article-%d", ordinal), ordinal)
		}
		if _, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, items); err != nil {
			t.Fatal(err)
		}
	}
	// Observe query results at the transaction boundary, so replay cannot
	// silently regress to loading the complete retained parent.
	starter := &receiptBoundStarter{conn: app, t: t}
	r, err := NewRevisions(starter, "writer-a", adapterLimits())
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{adapterProvision("article-0", 0)}); err != nil || len(rows) != 1 || starter.receiptReads != 1 {
		t.Fatalf("bounded replay failed: reads=%d err=%v", starter.receiptReads, err)
	}
	if _, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{adapterProvision("article-30", 30)}); err != nil || starter.receiptReads != 3 {
		t.Fatalf("insert did not use two bounded reads: reads=%d err=%v", starter.receiptReads, err)
	}
	before := adapterCounts(t, f)
	if _, err := r.InsertProvisionRevisions(f.ctx, g, revisionKey, []ProvisionInput{adapterProvision("new-key", 15)}); !errors.Is(err, run.ErrPayloadConflict) || adapterCounts(t, f) != before {
		t.Fatalf("bounded receipt lookup missed an ordinal collision: %v", err)
	}
	cursor := int32(-1)
	seen := 0
	for {
		page, err := r.ReadProvisionRevisions(f.ctx, g, revisionKey, cursor, 7)
		if err != nil || len(page) > 7 {
			t.Fatalf("page exceeded limit: %v", err)
		}
		if len(page) == 0 {
			break
		}
		for _, row := range page {
			if row.Ordinal != int32(seen) || row.Ordinal <= cursor {
				t.Fatal("page skipped or repeated an ordinal")
			}
			seen++
			cursor = row.Ordinal
		}
	}
	if seen != 31 {
		t.Fatalf("pagination lost rows: %d", seen)
	}
}

type receiptBoundStarter struct {
	conn         *pgx.Conn
	t            *testing.T
	receiptReads int
}

func (s *receiptBoundStarter) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := s.conn.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &receiptBoundTx{Tx: tx, starter: s}, nil
}

type receiptBoundTx struct {
	pgx.Tx
	starter *receiptBoundStarter
}

func (tx *receiptBoundTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if strings.HasPrefix(query, "-- name: ReadProvisionRevisions :") {
		tx.starter.t.Fatal("adapter read unbounded provision query")
	}
	rows, err := tx.Tx.Query(ctx, query, args...)
	if err == nil && strings.HasPrefix(query, "-- name: ReadProvisionReceipts :") {
		tx.starter.receiptReads++
		return &receiptBoundRows{Rows: rows, t: tx.starter.t, bound: 3 * len(args[1].([]string))}, nil
	}
	return rows, err
}

type receiptBoundRows struct {
	pgx.Rows
	t            *testing.T
	bound, count int
}

func (rows *receiptBoundRows) Next() bool {
	next := rows.Rows.Next()
	if next {
		rows.count++
		if rows.count > rows.bound {
			rows.t.Error("receipt query exceeded three times incoming batch size")
		}
	}
	return next
}

// Pause the losing adapter immediately before its mint statement, after both
// pre-reads missed. A valid execution on another connection commits the winner.
type mintCollisionStarter struct {
	conn           *pgx.Conn
	beforeMint     func(context.Context)
	afterCollision func(context.Context)
	begins         int
	collision      bool
}

func (s *mintCollisionStarter) BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	tx, err := s.conn.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	s.begins++
	return &mintCollisionTx{Tx: tx, starter: s}, nil
}

type mintCollisionTx struct {
	pgx.Tx
	starter *mintCollisionStarter
}

func (tx *mintCollisionTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.HasPrefix(query, "-- name: MintDocumentIdentity :") {
		tx.starter.beforeMint(ctx)
		return mintCollisionRow{Row: tx.Tx.QueryRow(ctx, query, args...), starter: tx.starter, ctx: ctx}
	}
	return tx.Tx.QueryRow(ctx, query, args...)
}

type mintCollisionRow struct {
	pgx.Row
	starter *mintCollisionStarter
	ctx     context.Context
}

func (row mintCollisionRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	var state interface{ SQLState() string }
	if errors.As(err, &state) && state.SQLState() == "23505" {
		row.starter.collision = true
		if row.starter.afterCollision != nil {
			row.starter.afterCollision(row.ctx)
		}
	}
	return err
}

func TestRevisionsAdapterMintCollisionLoser(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired-%t", expired), func(t *testing.T) {
			f, app, _, g := adapterFixture(t)
			other := adapterSecondGrant(t, f, g)
			u := *f.u
			u.User = url.User("banhmi_corpus_writer_app")
			ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
			defer cancel()
			winnerConn, err := pgx.Connect(ctx, u.String())
			if err != nil {
				t.Fatal("connect collision winner failed")
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = winnerConn.Close(cleanup)
			}()
			winner, err := NewRevisions(winnerConn, "writer-a", adapterLimits())
			if err != nil {
				t.Fatal(err)
			}
			if expired {
				if _, err := f.conn.Exec(ctx, `UPDATE ingest.execution_lease
					SET expires_at = clock_timestamp() + interval '2 seconds' WHERE resource_key = 'version-a'`); err != nil {
					t.Fatal(err)
				}
			}
			before := adapterCounts(t, f)
			starter := &mintCollisionStarter{conn: app}
			starter.beforeMint = func(ctx context.Context) {
				var count int
				if err := f.conn.QueryRow(ctx, "SELECT count(*) FROM silver.document_identity WHERE identity_key = $1", identityKey).Scan(&count); err != nil || count != 0 {
					t.Errorf("winner key was not unused after loser pre-read: %v", err)
					return
				}
				a := adapterAssertion()
				a.ObservationKey = "observation-22222222222222222222222222222222"
				if _, err := winner.MintDocumentIdentity(ctx, other, identityKey, a); err != nil {
					t.Errorf("collision winner failed: %v", err)
					return
				}
			}
			if expired {
				// Delay delivery of the real 23505 until the grant expires. The
				// mint statement itself must run while live to reach uniqueness.
				starter.afterCollision = func(ctx context.Context) {
					adapterWait(t, ctx, func() bool {
						var elapsed bool
						if err := f.conn.QueryRow(ctx, `SELECT clock_timestamp() >= expires_at FROM ingest.execution_lease
							WHERE resource_key = 'version-a'`).Scan(&elapsed); err != nil {
							t.Errorf("read collision expiry: %v", err)
							return true
						}
						return elapsed
					})
				}
			}
			r, err := NewRevisions(starter, "writer-a", adapterLimits())
			if err != nil {
				t.Fatal(err)
			}
			_, err = r.MintDocumentIdentity(ctx, g, identityKey, adapterAssertion())
			want := run.ErrPayloadConflict
			if expired {
				want = run.ErrStaleGrant
			}
			if !errors.Is(err, want) || !starter.collision || starter.begins != 1 {
				t.Fatalf("collision loser misclassified: collision=%t begins=%d err=%v", starter.collision, starter.begins, err)
			}
			expected := before
			expected[0]++
			expected[1]++
			if adapterCounts(t, f) != expected {
				t.Fatal("collision loser changed retained evidence")
			}
		})
	}
}

// Wait on observed database state, with an outer deadline for every lock test.
func adapterWait(t *testing.T, ctx context.Context, ready func() bool) {
	t.Helper()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestRevisionsAdapterLeaseLockWait(t *testing.T) {
	for _, mode := range []string{"expired", "cancelled", "context-cancelled", "advisory-expired", "advisory-context-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f, app, r, g := adapterFixture(t)
			before := adapterCounts(t, f)
			ctx, cancel := context.WithTimeout(f.ctx, 8*time.Second)
			defer cancel()
			if strings.HasSuffix(mode, "expired") {
				if _, err := f.conn.Exec(ctx, `UPDATE ingest.execution_lease
					SET expires_at = clock_timestamp() + interval '2 seconds'`); err != nil {
					t.Fatal(err)
				}
			}
			holder, err := f.conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "advisory-") {
				if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(jsonb_build_array(
					$1::text, 'source-a'::text, 'external-a'::text, '50/2024/TT-NHNN'::text,
					'thong-tu'::text, $2::text)::text, 0))`, observationKey, identityDigest); err != nil {
					t.Fatal(err)
				}
			} else if _, err := holder.Exec(ctx, "SELECT id FROM ingest.execution_lease WHERE resource_key = 'version-a' FOR UPDATE"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := r.MintDocumentIdentity(ctx, g, identityKey, adapterAssertion())
				done <- err
			}()
			t.Cleanup(func() {
				cancel()
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = holder.Rollback(cleanup)
				select {
				case <-done:
				case <-cleanup.Done():
					t.Error("lock waiter did not stop")
				}
			})
			adapterWait(t, ctx, func() bool {
				var blocked bool
				if err := holder.QueryRow(ctx, `SELECT $1::integer = ANY(pg_blocking_pids($2::integer))`,
					int32(f.conn.PgConn().PID()), int32(app.PgConn().PID())).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				return blocked
			})
			if strings.HasSuffix(mode, "expired") {
				adapterWait(t, ctx, func() bool {
					var elapsed bool
					if err := holder.QueryRow(ctx, "SELECT clock_timestamp() >= expires_at FROM ingest.execution_lease WHERE resource_key = 'version-a'").Scan(&elapsed); err != nil {
						t.Fatal(err)
					}
					return elapsed
				})
			} else if mode == "cancelled" {
				if _, err := holder.Exec(ctx, "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp() WHERE resource_key = 'version-a'"); err != nil {
					t.Fatal(err)
				}
			} else {
				cancel()
			}
			if !strings.HasSuffix(mode, "context-cancelled") {
				if err := holder.Commit(ctx); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-done:
				// Leave a completion receipt for cleanup after consuming the result.
				close(done)
				want := run.ErrStaleGrant
				if strings.HasSuffix(mode, "context-cancelled") {
					want = context.Canceled
				}
				if !errors.Is(err, want) || adapterCounts(t, f) != before {
					t.Fatalf("lock wait changed evidence or lost classification: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("lock waiter exceeded bounded completion time")
			}
		})
	}
}
