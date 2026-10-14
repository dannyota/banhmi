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
	all, err := r.ReadProvisionRevisions(f.ctx, g, revisionKey)
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
		return emptyBulkRow{row: row, tx: tx, force: tx.starter.inject && tx.starter.inserts == 1}
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
	for _, name := range []string{"epoch", "owner", "fence", "execution", "input", "recipe", "budget", "expired", "cancelled", "writer", "disabled"} {
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
					_, err := r.ReadProvisionRevisions(f.ctx, g, revisionKey)
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
