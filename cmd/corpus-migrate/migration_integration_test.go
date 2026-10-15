package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pressly/goose/v3"

	"danny.vn/banhmi/deploy/corpusmigrations"
	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/quality/formats"
	"danny.vn/banhmi/internal/corpus/run"
	sourceapi "danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/corpus/store/document"
	"danny.vn/banhmi/internal/corpus/store/intake"
)

var fixtureSequence atomic.Uint64

func TestReadOnlyFixtureURL(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	t.Setenv("PGPASSFILE", filepath.Join(t.TempDir(), "absent"))
	u, err := url.Parse("postgres://banhmi_corpus_writer_app@localhost:5432/banhmi_corpus_fixture_1_1?application_name=corpus%2Bmigrate%20fixture&sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pgx.ParseConfig(readOnlyFixtureURL(*u))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.RuntimeParams["options"]; got != "-c default_transaction_read_only=on" {
		t.Fatalf("startup options = %q, want read-only option with a space", got)
	}
	if got := cfg.RuntimeParams["application_name"]; got != "corpus+migrate fixture" {
		t.Fatalf("application name = %q, want literal plus and space preserved", got)
	}
}

type chainFixture struct {
	chain        chainSpec
	db           *sql.DB
	admin        *sql.DB
	adminURL     string
	migrationURL string
	database     string
}

func testURL(t *testing.T, chain string) *url.URL {
	t.Helper()
	name := "BANHMI_CORPUS_" + strings.ToUpper(chain) + "_TEST_DATABASE_URL"
	value := os.Getenv(name)
	if value == "" {
		if testing.Short() {
			t.Skip(name + " is absent")
		}
		t.Fatal(name + " is required outside short mode")
	}
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal("invalid synthetic test URL")
	}
	// Fixtures must never create/drop databases on an arbitrary service.
	if (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") || u.Path != "/banhmi_corpus_"+chain+"_ci" ||
		u.User == nil || u.User.Username() != "banhmi_corpus_"+chain+"_migrator" {
		t.Fatal("chain tests require the isolated trust-authenticated CI service")
	}
	if _, set := u.User.Password(); set {
		t.Fatal("chain tests require password-free synthetic logins")
	}
	if (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Port() != "5432" || u.Fragment != "" ||
		len(u.Query()) != 1 || u.Query().Get("sslmode") != "disable" {
		t.Fatal("chain test URL must use the fixed disposable service without connection overrides")
	}
	return u
}

func openTestDB(t *testing.T, value string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", value)
	if err != nil {
		t.Fatal("open fixture database failed")
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatal("connect fixture database failed")
	}
	return db
}

func execSQL(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), statement); err != nil {
		t.Fatalf("fixture SQL: %v", err)
	}
}

func tool(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	// All command URLs are validated, synthetic and password-free. No credential
	// files or production environment are consulted by the service authentication.
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture %s: %v\n%s", name, err, output)
	}
	return output
}

func newFixture(t *testing.T, name string) *chainFixture {
	t.Helper()
	return newFixtureMode(t, name, false)
}

func newFixtureMode(t *testing.T, name string, restoring bool) *chainFixture {
	t.Helper()
	u := testURL(t, name)
	adminURL := *u
	adminURL.User = url.User("postgres")
	adminURL.Path = "/postgres"
	admin := openTestDB(t, adminURL.String())
	database := fmt.Sprintf("banhmi_corpus_fixture_%d_%d", time.Now().UnixNano(), fixtureSequence.Add(1))
	execSQL(t, admin, "CREATE DATABASE "+database)
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), "DROP DATABASE "+database+" WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable fixture: %v", err)
		}
	})
	u.Path = "/" + database
	adminURL.Path = u.Path
	localAdmin := openTestDB(t, adminURL.String())
	if name != "control" {
		execSQL(t, localAdmin, "CREATE EXTENSION vector WITH SCHEMA public VERSION '0.8.2'; CREATE EXTENSION btree_gist WITH SCHEMA public VERSION '1.7'")
	}
	args := []string{"--no-psqlrc", "--dbname", adminURL.String(), "--set", "ON_ERROR_STOP=1", "--set", "migration_login_role=banhmi_corpus_" + name + "_migrator", "--set", "runtime_login_role=banhmi_corpus_" + name + "_app"}
	if name == "writer" {
		args = append(args, "--set", "export_login_role=banhmi_corpus_writer_exporter")
	}
	if name == "release" {
		if restoring {
			args = append(args, "--set", "restore_login_role=banhmi_corpus_release_restore")
		} else {
			args = append(args, "--set", "loader_login_role=banhmi_corpus_release_load")
		}
	}
	args = append(args, "--file", filepath.Join("..", "..", "deploy", "corpusmigrations", "bootstrap", name+".sql"))
	tool(t, "psql", args...)
	c, err := loadChain(name)
	if err != nil {
		t.Fatal(err)
	}
	f := &chainFixture{chain: c, db: openTestDB(t, u.String()), admin: localAdmin, adminURL: adminURL.String(), migrationURL: u.String(), database: database}
	if restoring {
		t.Cleanup(func() {
			execSQL(t, localAdmin, "REVOKE banhmi_corpus_release_migrator FROM banhmi_corpus_release_restore; REVOKE CONNECT ON DATABASE "+database+" FROM banhmi_corpus_release_restore")
		})
	}
	return f
}

func applyFixture(t *testing.T, f *chainFixture, opt options) {
	t.Helper()
	if err := migrate(context.Background(), f.db, f.chain, opt, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func assertInventory(t *testing.T, f *chainFixture, version int64) {
	t.Helper()
	tx, err := f.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	got, err := preflight(context.Background(), tx, f.chain, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != version {
		t.Fatalf("version %d, want %d", got, version)
	}
}

// loadChain reads the embedded directory and its JSON inventory.
func finalVersion(c chainSpec) int64 {
	return c.versions[len(c.versions)-1]
}

func inventoryAt(c chainSpec, version int64) []inventoryEntry {
	var entries []inventoryEntry
	for _, entry := range c.inventory {
		if entry.CreateVersion <= version && (entry.RemoveVersion == 0 || entry.RemoveVersion > version) {
			entries = append(entries, entry)
		}
	}
	return entries
}

func appliedHistory(c chainSpec, through int64) string {
	values := []string{"(0,true)"}
	for _, version := range c.versions {
		if version <= through {
			values = append(values, fmt.Sprintf("(%d,true)", version))
		}
	}
	return strings.Join(values, ",")
}

func inventoriedTable(t *testing.T, f *chainFixture, toast bool) string {
	t.Helper()
	for _, entry := range inventoryAt(f.chain, finalVersion(f.chain)) {
		if entry.Kind != "relation" {
			continue
		}
		var eligible bool
		if err := f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='r'
              AND (NOT $3::boolean OR c.reltoastrelid <> 0))`, entry.Schema, entry.Identity, toast).Scan(&eligible); err != nil {
			t.Fatal(err)
		}
		if eligible {
			return `"` + strings.ReplaceAll(entry.Schema, `"`, `""`) + `"."` + strings.ReplaceAll(entry.Identity, `"`, `""`) + `"`
		}
	}
	t.Fatalf("inventory has no table with required TOAST storage=%t", toast)
	return ""
}

func TestWriterChainReplay(t *testing.T) {
	testReplay(t, "writer")
}

type prefixObjects map[string][]byte

// Open reads one retained synthetic object.
func (s prefixObjects) Open(_ context.Context, path string) (io.ReadCloser, error) {
	data, ok := s[path]
	if !ok {
		return nil, errors.New("synthetic prefix object missing")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// Put rejects unexpected object writes.
func (prefixObjects) Put(context.Context, blob.Metadata, io.Reader) (blob.Object, error) {
	return blob.Object{}, errors.New("unexpected prefix object write")
}

// Stat rejects unexpected object metadata reads.
func (prefixObjects) Stat(context.Context, string) (blob.Object, error) {
	return blob.Object{}, errors.New("unexpected prefix object stat")
}

// Delete rejects unexpected object removal.
func (prefixObjects) Delete(context.Context, string) error {
	return errors.New("unexpected prefix object deletion")
}

type prefixPermit struct {
	expected run.DecisionBinding
	state    model.AcceptanceState
}

type prefixGateObjects struct {
	blob.Store
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}

// Open pauses the first read while acceptance holds the identity lock.
func (s *prefixGateObjects) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	s.once.Do(func() {
		close(s.entered)
		select {
		case <-s.resume:
		case <-ctx.Done():
		}
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Store.Open(ctx, path)
}

func prefixComparisonCause(err error) bool {
	return prefixCauseContains(err, "comparison-dependent eligibility is unavailable")
}

func prefixCauseContains(err error, want string) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), want) {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range joined.Unwrap() {
			if prefixCauseContains(child, want) {
				return true
			}
		}
	}
	return prefixCauseContains(errors.Unwrap(err), want)
}

// Check validates only the exact injected synthetic authority binding.
func (p prefixPermit) Check(_ context.Context, _ pgx.Tx, r run.IntakeApplyRequest) error {
	if r.Decision == nil || !reflect.DeepEqual(*r.Decision, p.expected) || r.Acceptance != p.state {
		return errors.New("synthetic prefix authority binding mismatch")
	}
	return nil
}

// TestWriterM1Prefix proves scoped-gap acceptance through the real adapters on
// versions 1-5. Its authored one-byte oracle and authority are synthetic only.
func TestWriterM1Prefix(t *testing.T) {
	t.Run("two-originals", func(t *testing.T) { writerM1PrefixCase(t, model.AcceptedWithGaps, "two-originals") })
	for _, positive := range []string{"source-format-empty", "source-format-unknown", "capture-media-parameters", "json-media-mislabeled", "json-media-shared"} {
		t.Run(positive, func(t *testing.T) { writerM1PrefixCase(t, model.AcceptedWithGaps, positive) })
	}
	for _, partial := range []string{"partial-conversion", "partial-coverage"} {
		t.Run(partial, func(t *testing.T) { writerM1PrefixCase(t, model.Quarantined, partial) })
	}
	for _, negative := range []string{"detector-pdf", "detector-media-mismatch", "capture-media-invalid", "json-budget-mislabeled", "json-budget-shared"} {
		t.Run(negative, func(t *testing.T) { writerM1PrefixCase(t, model.AcceptedWithGaps, negative) })
	}
	for _, state := range []model.AcceptanceState{model.AcceptedWithGaps, model.Accepted, model.Candidate, model.Quarantined, model.Excluded} {
		t.Run(string(state), func(t *testing.T) { writerM1PrefixCase(t, state, "") })
		for _, negative := range []string{"missing-execution-edge", "wrong-execution-edge"} {
			t.Run(string(state)+"/"+negative, func(t *testing.T) { writerM1PrefixCase(t, state, negative) })
		}
	}
	for _, negative := range []string{"missing-authority", "revoked-authority", "expired-decision", "wrong-decision-execution", "stale-decision", "reclaimed-decision", "action-mismatch", "scope-substitution", "scope-version", "scope-ranges", "missing-revision", "foreign-revision", "wrong-origin", "missing-gate", "missing-map", "missing-role", "wrong-map", "wrong-recipe", "wrong-report", "quarantining-report", "needs-ocr-report", "closure-overflow", "missing-manifest-member", "missing-converter-report", "unused-member", "manifest-size", "unknown-output", "unknown-normalizer", "unknown-parser"} {
		t.Run("negative/"+negative, func(t *testing.T) { writerM1PrefixCase(t, model.AcceptedWithGaps, negative) })
	}
}

func writerM1PrefixCase(t *testing.T, state model.AcceptanceState, negative string) {
	partial := negative == "partial-conversion" || negative == "partial-coverage"
	positive := negative == "two-originals" || negative == "source-format-empty" || negative == "source-format-unknown" || negative == "capture-media-parameters" || negative == "partial-conversion" || strings.HasPrefix(negative, "json-media-")
	f := newFixture(t, "writer")
	applyFixture(t, f, options{hasTo: true, to: 5})
	assertInventory(t, f, 5)
	assertWriterTableBoundaries(t, f)
	for _, name := range []string{"silver.part_revision", "silver.evidence_target", "config.taxonomy_release", "gold.chunk_set", "ingest.corpus_build"} {
		var absent bool
		if err := f.db.QueryRow(`SELECT to_regclass($1) IS NULL`, name).Scan(&absent); err != nil || !absent {
			t.Fatalf("M1 prefix contains a future table %s: %v", name, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	u, err := url.Parse(f.migrationURL)
	must(err)
	u.User = url.User("banhmi_corpus_writer_app")
	app, err := pgx.Connect(ctx, u.String())
	must(err)
	defer func() { _ = app.Close(context.Background()) }()
	var runtimeUser string
	must(app.QueryRow(ctx, `SELECT current_user`).Scan(&runtimeUser))
	if runtimeUser != "banhmi_corpus_writer_app" {
		t.Fatal("prefix adapters did not connect as the runtime login")
	}
	_, err = app.Exec(ctx, `INSERT INTO ingest.country_admission(id,country_code,admission_epoch,admitted_writer,enabled) VALUES(1,'vn',1,'prefix-writer',true)`)
	must(err)
	objects := prefixObjects{}
	encode := func(value any) []byte {
		raw, err := json.Marshal(value)
		must(err)
		return raw
	}
	stash := func(path string, data []byte) model.AssetRef {
		objects[path] = data
		return model.AssetRef{Path: path, SHA256: formats.Hash(data), ByteSize: int64(len(data))}
	}
	original := stash("prefix/original", []byte("x"))
	transport := stash("prefix/transport", []byte(`{"synthetic":true}`))
	identityDigest := formats.Hash([]byte("synthetic prefix work"))
	budget := run.Budget{MaxItems: 10000, MaxInputBytes: 1 << 20, MaxPages: 10, MaxElapsed: time.Minute, MaxConcurrent: 1}
	ledger, err := intake.NewLedger(app, "prefix-writer", objects, nil)
	must(err)
	work := run.IntakeWork{Execution: run.ScopedExecution{Country: "vn", WorkflowID: "prefix/source", WorkflowKind: run.SourceSync, AttemptKey: "prefix-attempt", AdmissionEpoch: 1, Owner: "prefix-owner", Scope: run.ExecutionScope{Kind: run.ScopeIntake, Subject: run.IntakeSubject{Kind: run.SubjectSourceWindow, SourceWindow: "prefix-window"}}},
		Stage: run.Discover, Substep: "open", Shard: "prefix-window", InputDigest: identityDigest, RecipeDigest: identityDigest, BudgetDigest: identityDigest, Budget: budget}
	lease, err := ledger.ClaimLease(ctx, 1, work.Execution.Scope, work.Execution.Owner, time.Now().Add(time.Hour))
	must(err)
	work.Execution.AttemptFence = lease.AttemptFence
	_, err = ledger.RegisterExecution(ctx, work, "synthetic-prefix-source", nil)
	must(err)
	docWork := work
	docWork.Stage = run.Fetch
	docWork.Execution.WorkflowID, docWork.Execution.WorkflowKind = "prefix/document", run.DocumentIngest
	docWork.Execution.Scope.Subject = run.IntakeSubject{Kind: run.SubjectDocumentVersion, DocumentVersion: "prefix-version"}
	docLease, err := ledger.ClaimLease(ctx, 1, docWork.Execution.Scope, docWork.Execution.Owner, time.Now().Add(time.Hour))
	must(err)
	docWork.Execution.AttemptFence = docLease.AttemptFence
	origin, err := ledger.RegisterExecution(ctx, docWork, "synthetic-prefix-document", nil)
	must(err)
	step := func(w run.IntakeWork, substep, shard string) run.IntakeWork { w.Substep, w.Shard = substep, shard; return w }
	sources, err := intake.NewSources(app, "prefix-writer", objects, nil)
	must(err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = sources.OpenWindow(ctx, work, intake.FrozenSourceWindow{Request: sourceapi.DiscoverRequest{Country: "vn", SourceID: "prefix-source", WindowKey: "prefix-window", SliceKey: "synthetic", Cursor: "start", Mode: sourceapi.DiscoveryIncremental, Start: now.Add(-time.Hour), Cutoff: now, PolicyHash: identityDigest, BudgetHash: identityDigest, Budget: model.ResourceBudget(budget)}, Deadline: now.Add(time.Hour), PageBudget: 1, RequestBudget: 10, AttemptLimit: 2, RetryInterval: time.Hour})
	must(err)
	_, err = sources.RegisterHost(ctx, step(work, "host", "example.com"), intake.SourceHostPacing{HostKey: "example.com", NextAllowedAt: now.Add(-time.Hour), MaxInFlight: 1})
	must(err)
	pageID := fmt.Sprintf("page-%032x", 1)
	listingID := fmt.Sprintf("request-%032x", 1)
	plan := sourceapi.RequestPlan{Country: "vn", SourceID: "prefix-source", RequestKey: listingID, WindowKey: "prefix-window", Purpose: model.PurposeListing, URL: "https://example.com/prefix", Method: "GET", Cursor: "start", PolicyHash: identityDigest, BudgetHash: identityDigest, Budget: model.ResourceBudget(budget)}
	_, err = sources.RegisterPage(ctx, step(work, "page", pageID), intake.SourcePagePlan{PageKey: pageID, Ordinal: 0, Plan: plan, RetryStartedAt: now.Add(-time.Minute)})
	must(err)
	pageClaim, err := sources.ClaimPage(ctx, step(work, "claim", pageID), pageID)
	must(err)
	capture := model.CaptureRef{Country: "vn", SourceID: "prefix-source", CaptureKey: fmt.Sprintf("capture-%032x", 1), Asset: original, MediaType: "text/html", RequestHash: identityDigest, ResponseHash: identityDigest, TransportReceipt: transport}
	if negative == "capture-media-parameters" {
		capture.MediaType = "Text/HTML; charset=utf-8"
	} else if negative == "capture-media-invalid" {
		capture.MediaType = "text/html; charset"
	}
	_, err = sources.RecordPageCapture(ctx, step(work, "capture", pageID), intake.SourceCaptureReceipt{Key: pageID, ClaimResult: pageClaim.ResultID, AttemptKey: pageClaim.AttemptKey, Attempt: 1, Capture: capture})
	must(err)
	page := sourceapi.DecodedDiscoveryPage{PageKey: pageID, Capture: capture, Cursor: model.CursorPosition{WindowKey: "prefix-window", SliceKey: "synthetic", InitialCursor: "start", CommittedCursor: "start", NextCursor: "done", Complete: true},
		Observations: []sourceapi.DecodedObservation{{SourceID: "prefix-source", ExternalID: "prefix-document", VersionKey: "prefix-version", Capture: capture, Limits: model.RuleLimits{MaxItems: 10000, MaxBytes: 1 << 20}}}, Limits: model.RuleLimits{MaxItems: 10000, MaxBytes: 1 << 20}}
	_, err = sources.RecordPageDecode(ctx, step(work, "decode", pageID), page)
	must(err)
	pageReceipt, err := sources.ApplyDiscoveryPage(ctx, step(work, "accept", pageID), intake.DiscoveryAcceptance{Page: page})
	must(err)
	observations, err := sources.AcceptedObservations(ctx, work, pageReceipt.ResultID)
	must(err)
	if len(observations) != 1 {
		t.Fatal("prefix source did not retain one observation")
	}
	observation := observations[0]
	artifact := model.ArtifactInventoryEntry{ArtifactKey: "prefix-artifact", Role: "main", URL: "https://example.com/original", Required: true, Format: "html"}
	switch negative {
	case "source-format-empty":
		artifact.Format = ""
	case "source-format-unknown":
		artifact.Format = "unknown"
	case "detector-pdf":
		artifact.Format = "pdf"
	}
	artifactID := fmt.Sprintf("request-%032x", 2)
	artifactPlan := plan
	artifactPlan.RequestKey, artifactPlan.Purpose, artifactPlan.URL, artifactPlan.Cursor, artifactPlan.ParentCapture = artifactID, model.PurposeArtifact, artifact.URL, "", capture.CaptureKey
	_, err = sources.RegisterRequests(ctx, step(docWork, "inventory", artifactID), sourceapi.DecodedDetailResult{Observation: page.Observations[0], Artifacts: []model.ArtifactInventoryEntry{artifact}, Limits: page.Limits}, []intake.SourceRequestPlan{{Plan: artifactPlan, ParentObservation: observation.ObservationKey, Role: model.ArtifactRoleMain, Required: true, ArtifactKey: artifact.ArtifactKey, HostKey: "example.com", RetryStartedAt: now.Add(-time.Minute)}})
	must(err)
	artifactClaim, err := sources.ClaimRequest(ctx, docWork)
	must(err)
	capture.CaptureKey = fmt.Sprintf("capture-%032x", 2)
	_, err = sources.RecordTransport(ctx, step(docWork, "transport", artifactID), intake.SourceTransportReceipt{RequestKey: artifactID, AttemptKey: artifactClaim.AttemptKey, Attempt: artifactClaim.NextAttempt, Capture: &capture, Next: now, RetryAfter: now})
	must(err)
	artifact.Capture = &capture
	_, err = sources.ApplyCapture(ctx, step(docWork, "bronze", artifactID), intake.CaptureAcceptance{RequestKey: artifactID, Capture: capture, Artifact: &artifact, CaptureURL: artifact.URL})
	must(err)
	selectedArtifacts := []formats.CandidateArtifact{{ArtifactKey: artifact.ArtifactKey, Role: "main", Required: true, Capture: capture, Format: artifact.Format}}
	var secondCapture model.CaptureRef
	if negative == "two-originals" || partial {
		secondOriginal := stash("prefix/original-b", []byte("<p>x</p>"))
		secondArtifact := model.ArtifactInventoryEntry{ArtifactKey: "prefix-artifact-b", Role: "main", URL: "https://example.com/second", Ordinal: 1, Required: true, Format: "html"}
		secondPlan := artifactPlan
		secondPlan.RequestKey, secondPlan.URL = fmt.Sprintf("request-%032x", 3), secondArtifact.URL
		_, err = sources.RegisterRequests(ctx, step(docWork, "inventory-b", secondPlan.RequestKey), sourceapi.DecodedDetailResult{Observation: page.Observations[0], Artifacts: []model.ArtifactInventoryEntry{secondArtifact}, Limits: page.Limits}, []intake.SourceRequestPlan{{Plan: secondPlan, ParentObservation: observation.ObservationKey, Role: model.ArtifactRoleMain, Required: true, ArtifactKey: secondArtifact.ArtifactKey, HostKey: "example.com", RetryStartedAt: now.Add(-time.Minute)}})
		must(err)
		secondClaim, err := sources.ClaimRequest(ctx, docWork)
		must(err)
		secondCapture = capture
		secondCapture.CaptureKey, secondCapture.Asset = fmt.Sprintf("capture-%032x", 3), secondOriginal
		_, err = sources.RecordTransport(ctx, step(docWork, "transport-b", secondPlan.RequestKey), intake.SourceTransportReceipt{RequestKey: secondPlan.RequestKey, AttemptKey: secondClaim.AttemptKey, Attempt: secondClaim.NextAttempt, Capture: &secondCapture, Next: now, RetryAfter: now})
		must(err)
		secondArtifact.Capture = &secondCapture
		_, err = sources.ApplyCapture(ctx, step(docWork, "bronze-b", secondPlan.RequestKey), intake.CaptureAcceptance{RequestKey: secondPlan.RequestKey, Capture: secondCapture, Artifact: &secondArtifact, CaptureURL: secondArtifact.URL})
		must(err)
		selectedArtifacts = append(selectedArtifacts, formats.CandidateArtifact{ArtifactKey: secondArtifact.ArtifactKey, ObservationOrdinal: 1, Role: "main", Required: true, Capture: secondCapture, Format: "html"})
	}
	catalog, err := intake.NewConversionCatalog(app, "prefix-writer", objects, formats.DefaultLimits())
	must(err)
	docWork.Stage = run.Normalize
	retained := map[model.Digest]model.AssetRef{}
	metadata := func(ref model.AssetRef) blob.Metadata {
		var m blob.Metadata
		must(app.QueryRow(ctx, `SELECT sha256,byte_size,media_type,object_ref,capture_source,capture_ref,captured_at FROM bronze.blob WHERE sha256=$1`, string(ref.SHA256)).Scan(&m.SHA256, &m.ByteSize, &m.MediaType, &m.ObjectRef, &m.CaptureSource, &m.CaptureRef, &m.CapturedAt))
		return m
	}
	register := func(path string, data []byte, dependencies ...model.AssetRef) model.AssetRef {
		ref := stash(path, data)
		media := "application/json"
		if !json.Valid(data) {
			media = "text/plain"
		}
		if strings.HasSuffix(negative, "mislabeled") {
			media = "application/octet-stream"
		}
		m := blob.Metadata{SHA256: string(ref.SHA256), ByteSize: ref.ByteSize, MediaType: media, ObjectRef: path, CaptureSource: "conversion", CaptureRef: origin.ExecutionKey, CapturedAt: now}
		if strings.HasPrefix(negative, "json-") && strings.HasSuffix(negative, "shared") && json.Valid(data) {
			// Register the same bytes first in a non-JSON role. The seed
			// manifest is not a dependency of the selected candidate closure.
			seedData := encode([]string{"catalog-winner", m.SHA256})
			seed := stash("winners/"+m.SHA256, seedData)
			seedMetadata := blob.Metadata{SHA256: string(seed.SHA256), ByteSize: seed.ByteSize, MediaType: "application/json", ObjectRef: seed.Path, CaptureSource: "conversion", CaptureRef: origin.ExecutionKey, CapturedAt: now}
			winner := m
			winner.MediaType = "application/octet-stream"
			_, err := catalog.RegisterConversionOutputs(ctx, step(docWork, "conversion-catalog", string(seed.SHA256)), intake.ConversionRegistration{Manifest: seedMetadata, Members: []blob.Metadata{winner}, MemberRefs: []model.AssetRef{ref}})
			must(err)
		}
		receipt, err := catalog.RegisterConversionOutputs(ctx, step(docWork, "conversion-catalog", string(ref.SHA256)), intake.ConversionRegistration{Manifest: m, MemberRefs: dependencies})
		must(err)
		for _, output := range receipt.Result.Outputs {
			retained[output.SHA256] = output
		}
		return ref
	}
	asset := func(data []byte) model.AssetRef { return register("objects/"+string(formats.Hash(data)), data) }
	recipe := register("recipes/converter", []byte("synthetic prefix converter"))
	normalizerValue := formats.BaselineNormalizer()
	parserValue := formats.Parser{SchemaVersion: "parser-v1", Parser: "none"}
	if negative == "unknown-normalizer" {
		normalizerValue.SchemaVersion = "normalizer-v2"
	} else if negative == "unknown-parser" {
		parserValue.SchemaVersion = "parser-v2"
	}
	normalizer := register("recipes/normalizer", encode(normalizerValue))
	parser := register("recipes/parser", encode(parserValue))
	profile := []model.MetricRequirement{{Kind: model.MetricFidelity, Mandatory: true, Threshold: "min:1"}, {Kind: model.MetricGeometry, Mandatory: true, Threshold: "min:1"}}
	policy := formats.PolicyBundle{SchemaVersion: "policy-bundle-v1", Country: "vn", ThresholdVersion: "synthetic-prefix-v1", ArtifactProfile: profile, RollupProfile: profile, AcceptanceLimits: formats.DefaultLimits(), GapReasonCodes: []string{"SYNTHETIC_METADATA_GAP"}}
	if negative == "closure-overflow" {
		policy.AcceptanceLimits.MaxBlobs = 6
	}
	if strings.HasPrefix(negative, "json-budget-") {
		policy.AcceptanceLimits.MaxJSONBytes = 6000
	}
	policyBytes, policyDigest, err := formats.PolicyBundleCodec(formats.DefaultLimits()).Encode(policy)
	must(err)
	_, err = app.Exec(ctx, `INSERT INTO config.policy_bundle(digest,manifest) VALUES($1,$2)`, string(policyDigest), policyBytes)
	must(err)
	input := formats.CandidateInput{SchemaVersion: "candidate-input-v1", Country: "vn", DocumentVersionKey: "prefix-version", ProcessingExecutionKey: origin.ExecutionKey, InputDigest: docWork.InputDigest, RecipeDigest: docWork.RecipeDigest, BudgetDigest: docWork.BudgetDigest,
		ObservationKey: observation.ObservationKey, Artifacts: selectedArtifacts, Language: "vi", HTMLSelector: "body", DetectorDigest: identityDigest,
		ConversionRecipe: recipe, NormalizerManifest: normalizer, ParserManifest: parser, GatePolicyDigest: policyDigest, ThresholdVersion: policy.ThresholdVersion, Mode: "first_pass"}
	inputBytes, candidateDigest, err := formats.CandidateInputCodec(formats.DefaultLimits()).Encode(input)
	must(err)
	inputDependencies := []model.AssetRef{original, transport, recipe, normalizer, parser}
	if negative == "two-originals" || partial {
		inputDependencies = append(inputDependencies, secondCapture.Asset)
	}
	register("candidates/"+string(candidateDigest), inputBytes, inputDependencies...)
	request := conversion.ConversionManifest{RequestKey: fmt.Sprintf("convert-%032x", 1), Request: api.ConvertRequest{Artifact: capture, Recipe: recipe, Budget: model.ResourceBudget(budget)}, ObservationKey: observation.ObservationKey, ArtifactRole: "main", Language: "vi", HTMLSelector: "body", DetectorHash: identityDigest, CandidateDigest: candidateDigest, OutputBytes: 1 << 20}
	conversionRequest := register("requests/"+string(formats.Hash(encode(request))), encode(request), original, transport, recipe)
	text := asset([]byte("x"))
	blockDigest := formats.Hash(encode([]string{"block-v1", "paragraph", "x", "x"}))
	blockID := fmt.Sprintf("block-%06d-%s", 0, blockDigest)
	originalSpan := model.Span{ArtifactHash: original.SHA256, BlockID: blockID, Space: model.OriginalBytes, Range: &model.ByteRange{Start: 0, End: 1}}
	rawSpan := model.Span{ArtifactHash: text.SHA256, Space: model.RawText, Range: &model.ByteRange{Start: 0, End: 1}, Object: "text"}
	normalSpan := model.Span{ArtifactHash: text.SHA256, Space: model.NormalizedText, Range: &model.ByteRange{Start: 0, End: 1}}
	block := model.Block{ID: blockID, Kind: model.BlockParagraph, RawText: "x", NormalizedText: text, Language: "vi", Authority: model.AuthorityUnknown, SourceSpans: []model.Span{originalSpan}}
	rawBlocks := asset(encode([]struct {
		ID      string
		Kind    model.BlockKind
		Text    string
		Sources []model.Span
	}{{block.ID, block.Kind, block.RawText, block.SourceSpans}}))
	normalBlocks := asset(encode([]model.Block{block}))
	maps := []model.OffsetMapSegment{{Sources: []model.Span{originalSpan}, Destinations: []model.Span{rawSpan}, Transform: conversion.TransformSourceDecode, Ordinal: 0}, {Sources: []model.Span{rawSpan}, Destinations: []model.Span{normalSpan}, Transform: conversion.TransformIdentity, Ordinal: 1}}
	mapAsset := asset(encode(maps))
	var unchangedLedger []model.EditLedgerEntry
	ledgerAsset := asset(encode(unchangedLedger))
	detectedFormat := "html"
	if negative == "detector-pdf" {
		detectedFormat = "pdf"
	} else if negative == "detector-media-mismatch" {
		detectedFormat = "docx"
	}
	detection := asset(encode(struct {
		Version  string
		Format   string
		Original model.Digest
		Recipe   model.Digest
	}{"detect-v1", detectedFormat, original.SHA256, identityDigest}))
	converted := model.ConvertedDocument{SchemaVersion: "blocks-v1", Original: capture, ObservationKey: observation.ObservationKey, ArtifactRole: "main", RecipeHash: recipe.SHA256, DetectorHash: detection.SHA256, CandidateDigest: candidateDigest, RawBlocks: rawBlocks, NormalizedBlocks: normalBlocks, RawText: text, NormalizedText: text, OffsetMap: mapAsset, EditLedger: ledgerAsset, Blocks: []model.Block{block}}
	resultPath := "results/" + string(conversionRequest.SHA256) + "/"
	documentAsset := register(resultPath+"document.json", encode(converted), rawBlocks, normalBlocks, text, mapAsset, ledgerAsset, detection)
	baselineMetrics := []model.QualityMetric{}
	baselineFindings := []model.Finding{}
	for k, kind := range []model.MetricKind{model.MetricGeometry, model.MetricFidelity} {
		baselineMetrics = append(baselineMetrics, model.QualityMetric{Kind: kind, State: model.MetricUnknown, Threshold: "qualification-required", Mandatory: true, ReasonCode: "BASELINE_UNQUALIFIED"})
		baselineFindings = append(baselineFindings, model.Finding{ReasonCode: []string{"GEOMETRY_UNKNOWN", "FIDELITY_UNQUALIFIED"}[k], Severity: model.SeverityBlocking, Scope: model.GapArtifact, ScopeKey: string(original.SHA256)})
	}
	observationReport := register(resultPath+"quality.json", encode(struct {
		SchemaVersion   string
		ArtifactHash    model.Digest
		CandidateDigest model.Digest
		RequestDigest   model.Digest
		RecipeHash      model.Digest
		DetectorHash    model.Digest
		DocumentHash    model.Digest
		Outcome         model.QualityOutcome
		Metrics         []model.QualityMetric
		Findings        []model.Finding
	}{"conversion-quality-v1", original.SHA256, candidateDigest, conversionRequest.SHA256, recipe.SHA256, detection.SHA256, documentAsset.SHA256, model.QualityQuarantined, baselineMetrics, baselineFindings}))
	members := []model.AssetRef{rawBlocks, normalBlocks, text, mapAsset, ledgerAsset, detection, documentAsset, observationReport}
	if negative == "missing-manifest-member" || negative == "missing-converter-report" {
		removed := rawBlocks
		if negative == "missing-converter-report" {
			removed = observationReport
			delete(retained, observationReport.SHA256)
		}
		filtered := []model.AssetRef{}
		for _, ref := range members {
			if ref.SHA256 != removed.SHA256 {
				filtered = append(filtered, ref)
			}
		}
		members = filtered
	} else if negative == "unused-member" {
		members = append(members, asset([]byte("unused synthetic member")))
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Path < members[j].Path })
	manifestMembers := append([]model.AssetRef(nil), members...)
	if negative == "manifest-size" {
		members[0].ByteSize++
	}
	sandboxManifest := register("manifests/"+string(formats.Hash(encode(members))), encode(members), manifestMembers...)
	var secondDocument model.ConvertedDocument
	var secondRequest, secondDocumentAsset, secondManifest, secondDetection model.AssetRef
	var secondMaps []model.OffsetMapSegment
	if negative == "two-originals" {
		requestB := request
		requestB.Request.Artifact = secondCapture
		requestB.RequestKey = fmt.Sprintf("convert-%032x", 2)
		secondRequest = register("requests/"+string(formats.Hash(encode(requestB))), encode(requestB), secondCapture.Asset, transport, recipe)
		sourceB := originalSpan
		sourceB.ArtifactHash, sourceB.Range = secondCapture.Asset.SHA256, &model.ByteRange{Start: 3, End: 4}
		blockB := block
		blockB.SourceSpans = []model.Span{sourceB}
		rawB := asset(encode([]struct {
			ID      string
			Kind    model.BlockKind
			Text    string
			Sources []model.Span
		}{{blockB.ID, blockB.Kind, blockB.RawText, blockB.SourceSpans}}))
		normalB := asset(encode([]model.Block{blockB}))
		secondMaps = []model.OffsetMapSegment{{Sources: []model.Span{sourceB}, Destinations: []model.Span{rawSpan}, Transform: conversion.TransformSourceDecode, Ordinal: 0}, {Sources: []model.Span{rawSpan}, Destinations: []model.Span{normalSpan}, Transform: conversion.TransformIdentity, Ordinal: 1}}
		mapB := asset(encode(secondMaps))
		secondDetection = asset(encode(struct {
			Version  string
			Format   string
			Original model.Digest
			Recipe   model.Digest
		}{"detect-v1", "html", secondCapture.Asset.SHA256, identityDigest}))
		secondDocument = converted
		secondDocument.Original, secondDocument.DetectorHash = secondCapture, secondDetection.SHA256
		secondDocument.RawBlocks, secondDocument.NormalizedBlocks = rawB, normalB
		secondDocument.OffsetMap, secondDocument.Blocks = mapB, []model.Block{blockB}
		pathB := "results/" + string(secondRequest.SHA256) + "/"
		secondDocumentAsset = register(pathB+"document.json", encode(secondDocument), rawB, normalB, text, mapB, ledgerAsset, secondDetection)
		findingsB := append([]model.Finding(nil), baselineFindings...)
		for i := range findingsB {
			findingsB[i].ScopeKey = string(secondCapture.Asset.SHA256)
		}
		reportB := register(pathB+"quality.json", encode(struct {
			SchemaVersion   string
			ArtifactHash    model.Digest
			CandidateDigest model.Digest
			RequestDigest   model.Digest
			RecipeHash      model.Digest
			DetectorHash    model.Digest
			DocumentHash    model.Digest
			Outcome         model.QualityOutcome
			Metrics         []model.QualityMetric
			Findings        []model.Finding
		}{"conversion-quality-v1", secondCapture.Asset.SHA256, candidateDigest, secondRequest.SHA256, recipe.SHA256, secondDetection.SHA256, secondDocumentAsset.SHA256, model.QualityQuarantined, baselineMetrics, findingsB}))
		membersB := []model.AssetRef{rawB, normalB, text, mapB, ledgerAsset, secondDetection, secondDocumentAsset, reportB}
		sort.Slice(membersB, func(i, j int) bool { return membersB[i].Path < membersB[j].Path })
		secondManifest = register("manifests/"+string(formats.Hash(encode(membersB))), encode(membersB), membersB...)
	}
	output := formats.CandidateOutput{SchemaVersion: "candidate-output-v1", CandidateDigest: candidateDigest, NormalizerManifestDigest: normalizer.SHA256, GatePolicyDigest: policyDigest,
		Artifacts: []formats.OutputArtifact{{Ordinal: 0, OriginalHash: original.SHA256, DetectorDigest: detection.SHA256, RawBlocks: rawBlocks, NormalizedBlocks: normalBlocks, RawText: text, NormalizedText: text, OffsetMap: mapAsset, EditLedger: ledgerAsset}}}
	outputDependencies := []model.AssetRef{rawBlocks, normalBlocks, text, mapAsset, ledgerAsset, detection}
	if negative == "two-originals" {
		output.Artifacts = append(output.Artifacts, formats.OutputArtifact{Ordinal: 1, OriginalHash: secondCapture.Asset.SHA256, DetectorDigest: secondDetection.SHA256, RawBlocks: secondDocument.RawBlocks, NormalizedBlocks: secondDocument.NormalizedBlocks, RawText: text, NormalizedText: text, OffsetMap: secondDocument.OffsetMap, EditLedger: ledgerAsset})
		outputDependencies = append(outputDependencies, secondDocument.RawBlocks, secondDocument.NormalizedBlocks, secondDocument.OffsetMap, secondDetection)
	}
	output.TextHash = formats.TextHash(output.Artifacts)
	var outputBytes []byte
	var outputDigest model.Digest
	if negative == "unknown-output" {
		output.SchemaVersion = "candidate-output-v2"
		outputBytes, outputDigest = encode(output), formats.Hash(encode(output))
	} else {
		outputBytes, outputDigest, err = formats.CandidateOutputCodec(formats.DefaultLimits()).Encode(output)
		must(err)
	}
	var outputManifest model.AssetRef
	if !partial {
		outputManifest = register("candidates/"+string(outputDigest), outputBytes, outputDependencies...)
	}
	grant := document.Grant{Execution: docWork.Execution, ExecutionID: origin.ID, InputDigest: docWork.InputDigest, RecipeDigest: docWork.RecipeDigest, BudgetDigest: docWork.BudgetDigest}
	revisions, err := document.NewRevisions(app, "prefix-writer", document.ProvisionLimits{MaxItems: 1000, MaxPayloadBytes: 1 << 20, MaxCitationBytes: 1000, MaxAnchorBytes: 1000, MaxAnchors: 100})
	must(err)
	identity := "vn:doc:00000000-0000-0000-0000-000000000001"
	minted, err := revisions.MintDocumentIdentity(ctx, grant, identity, document.ObservedAssertion{ObservationKey: observation.ObservationKey, SourceID: "prefix-source", ExternalID: "prefix-document", TitleHash: identityDigest})
	must(err)
	revision := identity + ":rev:" + string(formats.Hash([]byte("synthetic prefix revision")))
	if negative != "missing-revision" {
		_, err = revisions.InsertDocumentRevision(ctx, grant, document.RevisionInput{DocumentIdentityID: minted.ID, RevisionKey: revision, SourceObservationSetHash: formats.Hash(encode([]string{observation.ObservationKey})), TextHash: output.TextHash, MetadataHash: model.Digest(observation.MetadataDigest), ConversionRecipeDigest: recipe.SHA256, NormalizationRecipeDigest: normalizer.SHA256, ParserRecipeDigest: parser.SHA256, PolicyRecipeDigest: policyDigest})
		must(err)
	}
	conversionStore, err := document.NewConversion(app, "prefix-writer")
	must(err)
	target := document.ConversionTarget{RevisionKey: revision, CandidateDigest: candidateDigest}
	roles := []struct {
		role model.ConversionArtifactRole
		ref  model.AssetRef
	}{{model.ConversionRawBlocks, rawBlocks}, {model.ConversionNormalizedBlocks, normalBlocks}, {model.ConversionRawText, text}, {model.ConversionNormalizedText, text}, {model.ConversionManifest, sandboxManifest}}
	var roleMembers []document.ConversionArtifactMember
	for _, role := range roles {
		roleMembers = append(roleMembers, document.ConversionArtifactMember{Role: role.role, Hash: role.ref.SHA256, ByteSize: role.ref.ByteSize})
	}
	sort.Slice(roleMembers, func(i, j int) bool { return roleMembers[i].Role < roleMembers[j].Role })
	for _, role := range roles {
		if negative != "missing-revision" {
			_, err := conversionStore.RegisterConversionArtifact(ctx, grant, target, document.ConversionArtifactInput{Role: role.role, Blob: metadata(role.ref), SchemaVersion: "blocks-v1", OriginalHash: original.SHA256, ConversionRecipeDigest: recipe.SHA256, NormalizationRecipeDigest: normalizer.SHA256, DetectorDigest: detection.SHA256, Manifest: metadata(sandboxManifest), Members: roleMembers})
			must(err)
		}
	}
	var segments []document.ConversionSegment
	for _, segment := range maps {
		source, err := formats.ProjectReportSpan(segment.Sources[0], retained[segment.Sources[0].ArtifactHash])
		must(err)
		destination, err := formats.ProjectReportSpan(segment.Destinations[0], retained[segment.Destinations[0].ArtifactHash])
		must(err)
		segments = append(segments, document.ConversionSegment{MapHash: mapAsset.SHA256, Source: source, Destination: destination, TransformOrder: int32(segment.Ordinal), Transform: segment.Transform})
	}
	if negative != "missing-revision" {
		_, err = conversionStore.InsertOffsetMapSegments(ctx, grant, target, retained[text.SHA256], retained[text.SHA256], segments)
		must(err)
	}
	if negative == "two-originals" {
		rolesB := []struct {
			role model.ConversionArtifactRole
			ref  model.AssetRef
		}{{model.ConversionRawBlocks, secondDocument.RawBlocks}, {model.ConversionNormalizedBlocks, secondDocument.NormalizedBlocks}, {model.ConversionRawText, text}, {model.ConversionNormalizedText, text}, {model.ConversionManifest, secondManifest}}
		var membersB []document.ConversionArtifactMember
		for _, role := range rolesB {
			membersB = append(membersB, document.ConversionArtifactMember{Role: role.role, Ordinal: 1, Hash: role.ref.SHA256, ByteSize: role.ref.ByteSize})
		}
		sort.Slice(membersB, func(i, j int) bool { return membersB[i].Role < membersB[j].Role })
		for _, role := range rolesB {
			_, err = conversionStore.RegisterConversionArtifact(ctx, grant, target, document.ConversionArtifactInput{Role: role.role, Ordinal: 1, Blob: metadata(role.ref), SchemaVersion: "blocks-v1", OriginalHash: secondCapture.Asset.SHA256, ConversionRecipeDigest: recipe.SHA256, NormalizationRecipeDigest: normalizer.SHA256, DetectorDigest: secondDetection.SHA256, Manifest: metadata(secondManifest), Members: membersB})
			must(err)
		}
		for i, segment := range secondMaps {
			source, err := formats.ProjectReportSpan(segment.Sources[0], retained[segment.Sources[0].ArtifactHash])
			must(err)
			destination, err := formats.ProjectReportSpan(segment.Destinations[0], retained[segment.Destinations[0].ArtifactHash])
			must(err)
			_, err = conversionStore.InsertOffsetMapSegments(ctx, grant, target, retained[segment.Sources[0].ArtifactHash], retained[segment.Destinations[0].ArtifactHash], []document.ConversionSegment{{MapHash: secondDocument.OffsetMap.SHA256, Source: source, Destination: destination, TransformOrder: int32(2 + i), Transform: segment.Transform}})
			must(err)
		}
	}
	metrics := []model.QualityMetric{}
	for _, requirement := range profile {
		// The oracle is exactly the retained one-byte text and full range.
		metrics = append(metrics, model.QualityMetric{Kind: requirement.Kind, State: model.MetricMeasured, Numerator: int64(len(objects[text.Path])), Denominator: 1, Mandatory: true, Passed: len(objects[text.Path]) == 1 && objects[text.Path][0] == 'x', Threshold: requirement.Threshold, Evidence: []model.AssetRef{original}, Exclusions: []model.Span{}})
	}
	reportOutcome, decisionRef := model.QualityAccepted, ""
	gateFindings := []model.Finding{}
	if state == model.AcceptedWithGaps {
		reportOutcome, decisionRef = model.QualityAcceptedWithGaps, "decision-a"
	}
	if !state.BuildEligible() || negative == "quarantining-report" || negative == "needs-ocr-report" {
		reportOutcome, decisionRef = model.QualityQuarantined, ""
		if negative == "needs-ocr-report" {
			reportOutcome = model.QualityNeedsOCR
		}
		for k := range metrics {
			metrics[k].State, metrics[k].Numerator, metrics[k].Denominator, metrics[k].Passed = model.MetricUnknown, 0, 0, false
			metrics[k].ReasonCode, metrics[k].Evidence = "BASELINE_UNQUALIFIED", []model.AssetRef{}
		}
		for _, finding := range baselineFindings {
			finding.Spans, finding.Evidence = []model.Span{}, []model.AssetRef{}
			gateFindings = append(gateFindings, finding)
		}
	}
	gate := formats.GateReport{SchemaVersion: "gate-report-v1", Artifact: model.QualityReport{ArtifactHash: original.SHA256, CandidateDigest: candidateDigest, Report: formats.ReportSentinel(), PolicyHash: policyDigest, ThresholdVersion: policy.ThresholdVersion, Outcome: reportOutcome, Metrics: metrics, Findings: gateFindings, DecisionRef: decisionRef}, RequestManifest: &conversionRequest, Document: &documentAsset, OutputManifest: &sandboxManifest}
	gateCodec, err := formats.GateReportCodec(policy, formats.DefaultLimits())
	must(err)
	gateBytes, gateDigest, err := gateCodec.Encode(gate)
	must(err)
	gateAsset := register("reports/"+string(gateDigest), gateBytes, conversionRequest, documentAsset, sandboxManifest, original)
	gaps := []formats.Gap{{Ordinal: 0, Scope: model.GapArtifact, ScopeKey: artifact.ArtifactKey, ReasonCode: "SYNTHETIC_METADATA_GAP", Spans: []model.Span{}}}
	retainedSpan := normalSpan
	retainedSpan.BlockID = "document"
	retainedRanges := []formats.Retained{{Spans: []model.Span{retainedSpan}}}
	if state == model.Accepted {
		gaps = []formats.Gap{}
	} else if state == model.Excluded {
		gaps[0].Spans, retainedRanges[0].Spans = []model.Span{retainedSpan}, []model.Span{}
	}
	rollup := formats.GateRollup{SchemaVersion: "gate-rollup-v1", CandidateDigest: candidateDigest, OutputManifest: &outputManifest, PolicyHash: policyDigest, ThresholdVersion: policy.ThresholdVersion, Outcome: reportOutcome,
		ArtifactReports: []formats.ArtifactReport{{OriginalHash: original.SHA256, Report: gateAsset, Outcome: reportOutcome}}, MissingOrdinals: []int{}, Metrics: metrics, Findings: []model.Finding{}, Gaps: gaps, Retained: retainedRanges, DecisionRef: decisionRef}
	var secondGate formats.GateReport
	var secondGateAsset model.AssetRef
	if partial {
		secondGate = gate
		secondGate.RequestManifest, secondGate.Document, secondGate.OutputManifest = nil, nil, nil
		secondGate.Artifact.ArtifactHash = secondCapture.Asset.SHA256
		secondGate.Artifact.Findings = []model.Finding{{ReasonCode: "CONVERSION_MISSING", Severity: model.SeverityBlocking, Scope: model.GapArtifact, ScopeKey: "prefix-artifact-b", Spans: []model.Span{}, Evidence: []model.AssetRef{}}}
		body, digest, err := gateCodec.Encode(secondGate)
		must(err)
		secondGateAsset = register("reports/"+string(digest), body)
		rollup.OutputManifest, rollup.MissingOrdinals = nil, []int{1}
		rollup.ArtifactReports = append(rollup.ArtifactReports, formats.ArtifactReport{Ordinal: 1, OriginalHash: secondCapture.Asset.SHA256, Report: secondGateAsset, Outcome: reportOutcome})
		rollup.Retained = append(rollup.Retained, formats.Retained{Ordinal: 1, Spans: []model.Span{}})
		rollup.Gaps = append(rollup.Gaps, formats.Gap{Ordinal: 1, Scope: model.GapArtifact, ScopeKey: "prefix-artifact-b", ReasonCode: "SYNTHETIC_METADATA_GAP", Spans: []model.Span{}})
		if negative == "partial-coverage" {
			rollup.Retained[0].Spans = []model.Span{}
		}
	}
	if negative == "two-originals" {
		secondGate = gate
		secondGate.RequestManifest, secondGate.Document, secondGate.OutputManifest = &secondRequest, &secondDocumentAsset, &secondManifest
		secondGate.Artifact.ArtifactHash, secondGate.Artifact.Outcome, secondGate.Artifact.DecisionRef = secondCapture.Asset.SHA256, model.QualityAccepted, ""
		secondGate.Artifact.Metrics = append([]model.QualityMetric(nil), metrics...)
		for i := range secondGate.Artifact.Metrics {
			secondGate.Artifact.Metrics[i].Evidence = []model.AssetRef{secondCapture.Asset}
		}
		body, digest, err := gateCodec.Encode(secondGate)
		must(err)
		secondGateAsset = register("reports/"+string(digest), body, secondRequest, secondDocumentAsset, secondManifest, secondCapture.Asset)
		rollup.ArtifactReports = append(rollup.ArtifactReports, formats.ArtifactReport{Ordinal: 1, OriginalHash: secondCapture.Asset.SHA256, Report: secondGateAsset, Outcome: model.QualityAccepted})
		rollup.Retained = append(rollup.Retained, formats.Retained{Ordinal: 1, Spans: []model.Span{retainedSpan}})
	}
	rollupCodec, err := formats.GateRollupCodec(policy, formats.DefaultLimits())
	must(err)
	rollupBytes, rollupDigest, err := rollupCodec.Encode(rollup)
	must(err)
	rollupDependencies := []model.AssetRef{outputManifest, gateAsset, original}
	if partial {
		rollupDependencies = []model.AssetRef{gateAsset, original}
		outputDigest = rollupDigest
	}
	if negative == "two-originals" || partial {
		rollupDependencies = append(rollupDependencies, secondGateAsset)
	}
	rollupAsset := register("reports/"+string(rollupDigest), rollupBytes, rollupDependencies...)
	needsDecision := state == model.AcceptedWithGaps || state == model.Excluded
	var scopeDigest model.Digest
	if needsDecision {
		scope := formats.GapScope{SchemaVersion: "gap-scope-v1", CandidateDigest: candidateDigest, OriginalHash: original.SHA256, OutputDigest: outputDigest, Gaps: gaps, Retained: rollup.Retained}
		if negative == "scope-ranges" {
			scope.Retained = []formats.Retained{{Ordinal: 0, Spans: []model.Span{}}}
		}
		var scopeBytes []byte
		if negative == "scope-version" {
			scope.SchemaVersion = "gap-scope-v2"
			scopeBytes, scopeDigest = encode(scope), formats.Hash(encode(scope))
		} else {
			scopeBytes, scopeDigest, err = formats.GapScopeCodec(formats.DefaultLimits()).Encode(scope)
			must(err)
		}
		register("decisions/"+string(scopeDigest), scopeBytes)
	}
	persist := func(ref model.AssetRef, parent formats.QualityParent, selectedMetrics []model.QualityMetric, findings []model.Finding, selectedGaps []formats.Gap) {
		var refs []model.AssetRef
		for _, ref := range retained {
			refs = append(refs, ref)
		}
		rows, err := formats.ProjectQualityRows(selectedMetrics, findings, selectedGaps, refs, formats.DefaultLimits())
		must(err)
		var records []document.ConversionQualityRecord
		for _, row := range rows {
			records = append(records, document.ConversionQualityRecord{Ordinal: row.Ordinal, Kind: row.RecordKind, Code: row.Code, Severity: row.Severity, Scope: row.Scope, ScopeKey: row.ScopeKey, Reason: row.Reason, MetricKind: row.MetricKind, MetricState: row.MetricState, Numerator: row.Numerator, Denominator: row.Denominator, Threshold: row.Threshold, Mandatory: row.Mandatory, Passed: row.Passed, Evidence: encode(row.Evidence), SourceSpans: encode(row.SourceSpans)})
		}
		_, err = conversionStore.RecordConversionQualityReport(ctx, grant, target, document.ConversionQualityInput{Report: metadata(ref), ArtifactHash: parent.ArtifactHash, PolicyDigest: parent.PolicyHash, ThresholdVersion: parent.ThresholdVersion, Outcome: parent.Outcome, RecordCount: parent.RecordCount, Records: records})
		must(err)
	}
	gateParent, err := formats.ProjectArtifactParent(gate, retained[gateAsset.SHA256], policy, formats.DefaultLimits())
	must(err)
	if negative != "missing-gate" {
		persist(gateAsset, gateParent, metrics, gate.Artifact.Findings, []formats.Gap{})
	}
	if negative == "two-originals" || partial {
		parent, err := formats.ProjectArtifactParent(secondGate, retained[secondGateAsset.SHA256], policy, formats.DefaultLimits())
		must(err)
		persist(secondGateAsset, parent, secondGate.Artifact.Metrics, secondGate.Artifact.Findings, []formats.Gap{})
	}
	rollupParent, err := formats.ProjectRollupParent(rollup, retained[rollupAsset.SHA256], policy, formats.DefaultLimits())
	must(err)
	persist(rollupAsset, rollupParent, metrics, rollup.Findings, rollup.Gaps)
	decision := run.DecisionBinding{DecisionKey: "decision-a", ActorKey: "reviewer-a", Action: model.DecisionAcceptGaps, ExpiresAt: now.Add(time.Hour), AttemptFence: docWork.Execution.AttemptFence, AdmissionEpoch: 1, DecisionRevision: 1, Scope: docWork.Execution.Scope, RevisionKey: revision, CandidateDigest: candidateDigest, ReportHash: rollupDigest, OriginalHash: original.SHA256, OutputDigest: outputDigest, ScopeManifest: scopeDigest}
	if state == model.Excluded {
		decision.Action = model.DecisionExclude
	}
	if negative == "scope-substitution" {
		decision.ScopeManifest = parser.SHA256
	}
	if needsDecision {
		decisionExecution := origin.ID
		if negative == "wrong-decision-execution" {
			must(app.QueryRow(ctx, `SELECT id FROM ingest.execution WHERE resource_kind='source_window'`).Scan(&decisionExecution))
		}
		_, err = app.Exec(ctx, `INSERT INTO ingest.quarantine_decision(decision_key,document_version_key,execution_id,quality_report_hash,revision_key,candidate_digest,original_hash,output_hash,action,actor,workflow_fence_token,expected_decision_epoch,decision_revision,scope_kind,resource_kind,scope_key,scope_manifest_digest,evidence,expires_at) VALUES($1,'prefix-version',$2,$3,$4,$5,$6,$7,$8,$9,$10,1,1,'intake','document_version','prefix-version',$11,'{"fixture":true}',$12)`, decision.DecisionKey, decisionExecution, string(rollupDigest), revision, string(candidateDigest), string(original.SHA256), string(outputDigest), string(decision.Action), decision.ActorKey, decision.AttemptFence, string(decision.ScopeManifest), decision.ExpiresAt)
		must(err)
	}
	applyWork := step(docWork, "evidence-acceptance", revision)
	apply := run.IntakeApplyRequest{Work: applyWork, OutputDigest: outputDigest, Outcome: run.OutcomeSucceeded, Counts: run.Counts{Expected: 1, Processed: 1}, Acceptance: state, RevisionKey: revision, CandidateDigest: candidateDigest, QualityReportHash: rollupDigest, OriginalHash: original.SHA256, Complete: true}
	var permits intake.PermitCheck
	if needsDecision {
		apply.Work.DecisionRevision, apply.Decision, apply.GapAcceptanceRef = 1, &decision, decision.DecisionKey
		apply.Counts.TerminalGap, apply.Counts.AcceptedGaps = 1, 1
		permits = prefixPermit{expected: decision, state: state}
	} else {
		apply.Counts.Succeeded = 1
	}
	if state == model.Candidate {
		apply.Work.Substep, apply.Complete = "evidence-candidate", false
	} else if state == model.Quarantined {
		apply.Work.Substep, apply.Complete, apply.Outcome = "evidence-quarantine", false, run.OutcomeRejected
		apply.Counts.Succeeded, apply.Counts.Rejected = 0, 1
	} else if state == model.Excluded {
		apply.Outcome = run.OutcomeTerminalGap
	}
	apply.IdempotencyKey = run.IntakeIdempotencyKey(apply.Work)
	for _, ref := range retained {
		apply.Outputs = append(apply.Outputs, ref)
	}
	sort.Slice(apply.Outputs, func(i, j int) bool { return apply.Outputs[i].SHA256 < apply.Outputs[j].SHA256 })
	snapshot := func() map[string]string {
		rows := map[string]string{}
		for _, table := range []string{"ingest.execution_result", "ingest.revision_acceptance", "ingest.revision_acceptance_state", "ingest.revision_receipt", "ingest.evidence_gap", "bronze.blob_retention_reference", "ingest.delivery_record", "ingest.acceptance_lock"} {
			var raw json.RawMessage
			must(app.QueryRow(ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY r.id),'[]'::jsonb) FROM "+table+" r").Scan(&raw))
			rows[table] = string(raw)
		}
		return rows
	}
	if negative != "" && !positive {
		switch negative {
		case "reclaimed-decision":
			_, err = app.Exec(ctx, `UPDATE ingest.execution_lease SET cancelled_at=clock_timestamp() WHERE resource_kind='document_version'`)
			must(err)
			reclaimed, err := ledger.ReclaimLease(ctx, 1, apply.Work.Execution.Scope, "owner-b", time.Now().Add(time.Hour))
			must(err)
			apply.Work.Execution.Owner, apply.Work.Execution.AttemptFence = reclaimed.Owner, reclaimed.AttemptFence
			_, err = ledger.RegisterExecution(ctx, apply.Work, "prefix-reclaimed", nil)
			must(err)
		case "missing-authority":
			permits = nil
		case "revoked-authority":
			permits = prefixPermit{expected: decision, state: model.Quarantined}
		case "expired-decision", "stale-decision", "action-mismatch":
			changed := *apply.Decision
			if negative == "expired-decision" {
				changed.ExpiresAt = now.Add(-time.Second)
			} else if negative == "stale-decision" {
				changed.DecisionRevision, apply.Work.DecisionRevision = 2, 2
			} else {
				changed.Action = model.DecisionExclude
			}
			apply.Decision = &changed
		case "missing-execution-edge", "wrong-execution-edge":
			_, err = f.db.ExecContext(ctx, `DELETE FROM bronze.blob_retention_reference WHERE owner_kind='execution' AND blob_id=(SELECT id FROM bronze.blob WHERE sha256=$1)`, string(candidateDigest))
			must(err)
			if negative == "wrong-execution-edge" {
				_, err = app.Exec(ctx, `INSERT INTO bronze.blob_retention_reference(blob_id,owner_kind,owner_key) SELECT b.id,'execution',e.execution_key FROM bronze.blob b CROSS JOIN ingest.execution e WHERE b.sha256=$1 AND e.resource_kind='source_window'`, string(candidateDigest))
				must(err)
			}
		case "foreign-revision", "wrong-origin":
			execSQL(t, f.db, `ALTER TABLE silver.document_revision DISABLE TRIGGER document_revision_immutable`)
			if negative == "foreign-revision" {
				execSQL(t, f.db, `UPDATE silver.document_revision SET document_version_key='foreign-version'`)
			} else {
				execSQL(t, f.db, `UPDATE silver.document_revision SET intake_execution_key=(SELECT execution_key FROM ingest.execution WHERE resource_kind='source_window')`)
			}
			execSQL(t, f.db, `ALTER TABLE silver.document_revision ENABLE TRIGGER document_revision_immutable`)
		case "missing-role", "wrong-recipe":
			execSQL(t, f.db, `ALTER TABLE silver.conversion_artifact DISABLE TRIGGER conversion_artifact_immutable`)
			if negative == "missing-role" {
				execSQL(t, f.db, `DELETE FROM silver.conversion_artifact WHERE role='raw_blocks'`)
			} else {
				_, err = f.db.ExecContext(ctx, `UPDATE silver.conversion_artifact SET conversion_recipe_digest=$1`, string(identityDigest))
				must(err)
			}
			execSQL(t, f.db, `ALTER TABLE silver.conversion_artifact ENABLE TRIGGER conversion_artifact_immutable`)
		case "missing-map", "wrong-map":
			execSQL(t, f.db, `ALTER TABLE silver.offset_map_segment DISABLE TRIGGER offset_map_segment_immutable`)
			if negative == "missing-map" {
				execSQL(t, f.db, `DELETE FROM silver.offset_map_segment WHERE transform_order=0`)
			} else {
				execSQL(t, f.db, `UPDATE silver.offset_map_segment SET transform='forged' WHERE transform_order=0`)
			}
			execSQL(t, f.db, `ALTER TABLE silver.offset_map_segment ENABLE TRIGGER offset_map_segment_immutable`)
		case "wrong-report":
			execSQL(t, f.db, `ALTER TABLE silver.quality_finding DISABLE TRIGGER quality_finding_immutable`)
			execSQL(t, f.db, `UPDATE silver.quality_finding SET passed=false WHERE record_kind='metric'`)
			execSQL(t, f.db, `ALTER TABLE silver.quality_finding ENABLE TRIGGER quality_finding_immutable`)
		}
		apply.IdempotencyKey = run.IntakeIdempotencyKey(apply.Work)
		acceptance, err := intake.NewAcceptance(app, "prefix-writer", objects, permits)
		must(err)
		before := snapshot()
		_, rejected := acceptance.Apply(ctx, apply)
		if rejected == nil {
			t.Fatal("negative prefix fixture accepted invalid evidence or authority")
		}
		if negative == "partial-coverage" && !prefixCauseContains(rejected, "acceptance partition:") {
			t.Fatal("partial coverage did not fail partition validation", rejected)
		}
		if strings.HasPrefix(negative, "detector-") || negative == "capture-media-invalid" {
			if !prefixCauseContains(rejected, "acceptance detector") {
				t.Fatal("detector negative failed outside its contract check", rejected)
			}
		}
		if negative == "quarantining-report" || negative == "needs-ocr-report" {
			if !prefixCauseContains(rejected, "report cannot support requested disposition") {
				t.Fatal("ineligible report failed outside the disposition check", rejected)
			}
		}
		if strings.HasPrefix(negative, "json-budget-") && !prefixCauseContains(rejected, "acceptance work exceeds pinned bounds") {
			t.Fatal("mislabeled JSON did not fail cumulative accounting", rejected)
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("negative prefix fixture changed a commit or synchronization row")
		}
		return
	}
	acceptance, err := intake.NewAcceptance(app, "prefix-writer", objects, permits)
	must(err)
	var candidateReceipt run.IntakeReceipt
	candidate := apply
	if state.BuildEligible() || state == model.Excluded {
		candidate.Acceptance, candidate.Complete, candidate.Outcome = model.Candidate, false, run.OutcomeSucceeded
		candidate.Work.Substep, candidate.Work.DecisionRevision = "evidence-candidate", 0
		candidate.Counts, candidate.Decision, candidate.GapAcceptanceRef = run.Counts{Expected: 1, Processed: 1, Succeeded: 1}, nil, ""
		candidate.Outputs = []model.AssetRef{}
		for _, ref := range apply.Outputs {
			if ref.SHA256 != scopeDigest {
				candidate.Outputs = append(candidate.Outputs, ref)
			}
		}
		candidate.IdempotencyKey = run.IntakeIdempotencyKey(candidate.Work)
		candidateReceipt, err = acceptance.Apply(ctx, candidate)
		must(err)
	}
	if state == model.AcceptedWithGaps && negative == "" {
		quarantineGate := gate
		quarantineGate.Artifact.Outcome, quarantineGate.Artifact.DecisionRef = model.QualityQuarantined, ""
		quarantineGate.Artifact.Metrics = append([]model.QualityMetric(nil), metrics...)
		for i := range quarantineGate.Artifact.Metrics {
			m := &quarantineGate.Artifact.Metrics[i]
			m.State, m.Numerator, m.Denominator, m.Passed = model.MetricUnknown, 0, 0, false
			m.ReasonCode, m.Evidence = "BASELINE_UNQUALIFIED", []model.AssetRef{}
		}
		body, digest, err := gateCodec.Encode(quarantineGate)
		must(err)
		quarantineGateAsset := register("reports/"+string(digest), body, conversionRequest, documentAsset, sandboxManifest, original)
		parent, err := formats.ProjectArtifactParent(quarantineGate, retained[quarantineGateAsset.SHA256], policy, formats.DefaultLimits())
		must(err)
		persist(quarantineGateAsset, parent, quarantineGate.Artifact.Metrics, quarantineGate.Artifact.Findings, []formats.Gap{})
		quarantineRollup := rollup
		quarantineRollup.Outcome, quarantineRollup.DecisionRef = model.QualityQuarantined, ""
		quarantineRollup.Metrics = quarantineGate.Artifact.Metrics
		quarantineRollup.ArtifactReports = []formats.ArtifactReport{{Ordinal: 0, OriginalHash: original.SHA256, Report: quarantineGateAsset, Outcome: model.QualityQuarantined}}
		body, digest, err = rollupCodec.Encode(quarantineRollup)
		must(err)
		quarantineAsset := register("reports/"+string(digest), body, outputManifest, quarantineGateAsset, original)
		parent, err = formats.ProjectRollupParent(quarantineRollup, retained[quarantineAsset.SHA256], policy, formats.DefaultLimits())
		must(err)
		persist(quarantineAsset, parent, quarantineRollup.Metrics, quarantineRollup.Findings, gaps)
		quarantine := candidate
		quarantine.Work.Substep, quarantine.Acceptance, quarantine.Outcome = "evidence-quarantine", model.Quarantined, run.OutcomeRejected
		quarantine.Counts, quarantine.QualityReportHash = run.Counts{Expected: 1, Processed: 1, Rejected: 1}, quarantineAsset.SHA256
		quarantine.Outputs = []model.AssetRef{}
		for _, ref := range candidate.Outputs {
			if ref.SHA256 != gateAsset.SHA256 && ref.SHA256 != rollupAsset.SHA256 {
				quarantine.Outputs = append(quarantine.Outputs, ref)
			}
		}
		quarantine.Outputs = append(quarantine.Outputs, retained[quarantineGateAsset.SHA256], retained[quarantineAsset.SHA256])
		sort.Slice(quarantine.Outputs, func(i, j int) bool { return quarantine.Outputs[i].SHA256 < quarantine.Outputs[j].SHA256 })
		quarantine.IdempotencyKey = run.IntakeIdempotencyKey(quarantine.Work)
		quarantineReceipt, err := acceptance.Apply(ctx, quarantine)
		must(err)
		if quarantineReceipt.ResultID == candidateReceipt.ResultID {
			t.Fatal("candidate and quarantine reused an event key")
		}
	}
	var first run.IntakeReceipt
	if state == model.Accepted {
		// A separate version lease must still serialize at this identity. The
		// waiter starts before the first eligible event commits and must reread
		// that history after its identity-lock wait, despite first_pass mode and
		// an omitted comparison. Later provenance checks cannot satisfy this
		// assertion because the test requires the comparison-specific cause.
		nextWork := docWork
		nextWork.Execution.WorkflowID, nextWork.Execution.AttemptKey = "prefix/next-document", "next-attempt"
		nextWork.Execution.Scope.Subject.DocumentVersion = "prefix-next-version"
		nextLease, err := ledger.ClaimLease(ctx, 1, nextWork.Execution.Scope, nextWork.Execution.Owner, now.Add(time.Hour))
		must(err)
		nextWork.Execution.AttemptFence = nextLease.AttemptFence
		nextOrigin, err := ledger.RegisterExecution(ctx, nextWork, "synthetic next prefix", nil)
		must(err)
		docWork, origin = nextWork, nextOrigin
		nextInput := input
		nextInput.DocumentVersionKey, nextInput.ProcessingExecutionKey = "prefix-next-version", nextOrigin.ExecutionKey
		nextInputBytes, nextDigest, err := formats.CandidateInputCodec(formats.DefaultLimits()).Encode(nextInput)
		must(err)
		register("candidates/"+string(nextDigest), nextInputBytes, original, transport, recipe, normalizer, parser)
		nextOutput := output
		nextOutput.CandidateDigest = nextDigest
		nextOutputBytes, nextOutputDigest, err := formats.CandidateOutputCodec(formats.DefaultLimits()).Encode(nextOutput)
		must(err)
		nextOutputAsset := register("candidates/"+string(nextOutputDigest), nextOutputBytes, rawBlocks, normalBlocks, text, mapAsset, ledgerAsset, detection)
		nextGate := gate
		nextGate.Artifact.CandidateDigest = nextDigest
		nextGateBytes, nextGateDigest, err := gateCodec.Encode(nextGate)
		must(err)
		nextGateAsset := register("reports/"+string(nextGateDigest), nextGateBytes, conversionRequest, documentAsset, sandboxManifest, original)
		nextRollup := rollup
		nextRollup.CandidateDigest, nextRollup.OutputManifest = nextDigest, &nextOutputAsset
		nextRollup.ArtifactReports = append([]formats.ArtifactReport(nil), rollup.ArtifactReports...)
		nextRollup.ArtifactReports[0].Report = nextGateAsset
		nextRollupBytes, nextRollupDigest, err := rollupCodec.Encode(nextRollup)
		must(err)
		register("reports/"+string(nextRollupDigest), nextRollupBytes, nextOutputAsset, nextGateAsset, original)
		nextRequest := apply
		nextRequest.Work = step(nextWork, "evidence-acceptance", identity+":rev:"+string(formats.Hash([]byte("synthetic next revision"))))
		nextRequest.RevisionKey, nextRequest.CandidateDigest = nextRequest.Work.Shard, nextDigest
		nextRequest.QualityReportHash, nextRequest.OutputDigest = nextRollupDigest, nextOutputDigest
		nextRequest.IdempotencyKey = run.IntakeIdempotencyKey(nextRequest.Work)
		nextRequest.Outputs = []model.AssetRef{}
		for _, ref := range retained {
			nextRequest.Outputs = append(nextRequest.Outputs, ref)
		}
		sort.Slice(nextRequest.Outputs, func(i, j int) bool { return nextRequest.Outputs[i].SHA256 < nextRequest.Outputs[j].SHA256 })
		nextConn, err := pgx.Connect(ctx, u.String())
		must(err)
		defer func() { _ = nextConn.Close(context.Background()) }()
		observer, err := pgx.Connect(ctx, u.String())
		must(err)
		defer func() { _ = observer.Close(context.Background()) }()
		nextAcceptance, err := intake.NewAcceptance(nextConn, "prefix-writer", objects, nil)
		must(err)
		gateObjects := &prefixGateObjects{Store: objects, entered: make(chan struct{}), resume: make(chan struct{})}
		var release sync.Once
		resume := func() { release.Do(func() { close(gateObjects.resume) }) }
		defer resume()
		acceptance, err = intake.NewAcceptance(app, "prefix-writer", gateObjects, nil)
		must(err)
		type prefixResult struct {
			receipt run.IntakeReceipt
			err     error
		}
		committing := make(chan prefixResult, 1)
		waiting := make(chan error, 1)
		go func() { receipt, err := acceptance.Apply(ctx, apply); committing <- prefixResult{receipt, err} }()
		select {
		case <-gateObjects.entered:
		case <-ctx.Done():
			t.Fatal("first version did not reach its identity-locked barrier")
		}
		go func() { _, err := nextAcceptance.Apply(ctx, nextRequest); waiting <- err }()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for blocked := false; !blocked; {
			must(observer.QueryRow(ctx, `SELECT $2::integer = ANY(pg_blocking_pids($1::integer))`, int64(nextConn.PgConn().PID()), int64(app.PgConn().PID())).Scan(&blocked))
			if !blocked {
				select {
				case <-ticker.C:
				case <-ctx.Done():
					t.Fatal("different-version acceptance did not serialize on identity")
				}
			}
		}
		resume()
		select {
		case result := <-committing:
			first = result.receipt
			must(result.err)
		case <-ctx.Done():
			t.Fatal("first version did not commit")
		}
		select {
		case err := <-waiting:
			if !prefixComparisonCause(err) {
				t.Fatal("identity waiter did not reread comparison-dependent acceptance history")
			}
		case <-ctx.Done():
			t.Fatal("identity waiter did not finish")
		}
		var untouched bool
		must(app.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM ingest.revision_acceptance WHERE processing_attempt_key='next-attempt') AND NOT EXISTS(SELECT 1 FROM ingest.acceptance_lock WHERE processing_attempt_key='next-attempt')`).Scan(&untouched))
		if !untouched {
			t.Fatal("rejected next version left acceptance or synchronization rows")
		}
	} else {
		first, err = acceptance.Apply(ctx, apply)
		must(err)
	}
	committed := snapshot()
	again, err := acceptance.Apply(ctx, apply)
	must(err)
	if !reflect.DeepEqual(first, again) || !reflect.DeepEqual(committed, snapshot()) {
		t.Fatal("prefix response-loss replay changed the original receipt")
	}
	if candidateReceipt.ResultID != "" {
		historical, err := acceptance.Apply(ctx, candidate)
		must(err)
		if !reflect.DeepEqual(candidateReceipt, historical) {
			t.Fatal("historical candidate replay changed after terminal acceptance")
		}
		changed := candidate
		changed.Work.Substep = "evidence-quarantine"
		changed.Acceptance, changed.Outcome, changed.Counts = model.Quarantined, run.OutcomeRejected, run.Counts{Expected: 1, Processed: 1, Rejected: 1}
		changed.IdempotencyKey = run.IntakeIdempotencyKey(changed.Work)
		if _, err := acceptance.Apply(ctx, changed); err == nil {
			t.Fatal("terminal acceptance permitted a new event")
		}
	}
	var complete bool
	must(app.QueryRow(ctx, `SELECT (SELECT count(*) FROM ingest.revision_acceptance WHERE execution_result_id=r.id)=1 AND (SELECT count(*) FROM ingest.revision_receipt WHERE execution_result_id=r.id)=1 AND (SELECT count(*) FROM ingest.evidence_gap WHERE acceptance_id=a.id)=$3 AND s.state=$2 AND s.watermark=r.id AND (SELECT count(*) FROM ingest.delivery_record WHERE receipt_key=r.result_key)=1 FROM ingest.execution_result r JOIN ingest.revision_acceptance a ON a.execution_result_id=r.id JOIN ingest.revision_acceptance_state s ON s.acceptance_id=a.id WHERE r.result_key=$1`, first.ResultID, string(state), len(rollup.Gaps)).Scan(&complete))
	if !complete {
		t.Fatal("prefix acceptance lost part of its atomic commit set")
	}
	if negative == "two-originals" {
		var distinctMaps int
		var orders []int32
		must(app.QueryRow(ctx, `SELECT count(DISTINCT map_hash), array_agg(transform_order ORDER BY transform_order) FROM silver.offset_map_segment WHERE candidate_digest=$1 AND transform='identity-v1'`, string(candidateDigest)).Scan(&distinctMaps, &orders))
		if distinctMaps != 2 || !reflect.DeepEqual(orders, []int32{1, 3}) {
			t.Fatal("two originals with the same paragraph lost candidate-wide map ordering")
		}
	}
}
func TestReleaseChainReplay(t *testing.T) {
	testReplay(t, "release")
}
func TestControlChainReplay(t *testing.T) {
	testReplay(t, "control")
}

func TestRecreatedPlpgsqlReplay(t *testing.T) {
	for _, name := range []string{"writer", "release", "control"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, name)
			execSQL(t, f.admin, "DROP EXTENSION plpgsql; CREATE EXTENSION plpgsql WITH SCHEMA pg_catalog")
			var handlers int
			var normalOIDs bool
			if err := f.admin.QueryRow(`SELECT count(*), COALESCE(bool_and(p.oid >= 16384),false)
                FROM pg_proc p JOIN pg_depend d ON d.classid='pg_proc'::regclass AND d.objid=p.oid
                JOIN pg_extension e ON d.refclassid='pg_extension'::regclass AND d.refobjid=e.oid
                JOIN pg_namespace n ON n.oid=p.pronamespace
                WHERE e.extname='plpgsql' AND d.deptype='e' AND n.nspname='pg_catalog'
                  AND p.proname IN ('plpgsql_call_handler','plpgsql_inline_handler','plpgsql_validator')`).Scan(&handlers, &normalOIDs); err != nil {
				t.Fatal(err)
			}
			if handlers != 3 || !normalOIDs {
				t.Fatal("recreated plpgsql must have three normal-OID catalog handlers")
			}
			assertInventory(t, f, 0)
			assertReadOnlyStatus(t, f, false)
			for _, version := range f.chain.versions {
				applyFixture(t, f, options{hasTo: true, to: version})
				assertInventory(t, f, version)
				before := catalogSnapshot(t, f)
				applyFixture(t, f, options{hasTo: true, to: version})
				if got := catalogSnapshot(t, f); got != before {
					t.Fatal("recreated plpgsql prefix no-op changed catalog or history")
				}
			}
			before := catalogSnapshot(t, f)
			applyFixture(t, f, options{})
			if got := catalogSnapshot(t, f); got != before {
				t.Fatal("recreated plpgsql no-op changed catalog or history")
			}
			assertReadOnlyStatus(t, f, true)
		})
	}
}

func readOnlyFixtureURL(u url.URL) string {
	query := u.Query()
	query.Set("options", "-c default_transaction_read_only=on")
	// PostgreSQL URIs require percent-encoded spaces; '+' is literal in libpq.
	u.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	return u.String()
}

func assertReadOnlyStatus(t *testing.T, f *chainFixture, applied bool) {
	t.Helper()
	if applied {
		execSQL(t, f.db, "GRANT SELECT ON public."+f.chain.table+" TO banhmi_corpus_"+f.chain.name+"_app")
	}
	u, err := url.Parse(f.migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.User("banhmi_corpus_" + f.chain.name + "_app")
	readonly := openTestDB(t, readOnlyFixtureURL(*u))
	before := catalogSnapshot(t, f)
	var out bytes.Buffer
	if err := migrate(context.Background(), readonly, f.chain, options{status: true}, &out); err != nil {
		t.Fatal(err)
	}
	state := "pending"
	if applied {
		state = "applied"
	}
	for _, file := range f.chain.files {
		if !strings.Contains(out.String(), file+" "+state) {
			t.Fatal("read-only status omitted file or state")
		}
	}
	if got := catalogSnapshot(t, f); got != before {
		t.Fatal("read-only status changed catalog or history")
	}
}

func testReplay(t *testing.T, name string) {
	f := newFixture(t, name)
	assertInventory(t, f, 0)
	for _, version := range f.chain.versions {
		applyFixture(t, f, options{hasTo: true, to: version})
		assertInventory(t, f, version)
		before := catalogSnapshot(t, f)
		applyFixture(t, f, options{hasTo: true, to: version})
		if got := catalogSnapshot(t, f); got != before {
			t.Fatal("second prefix replay changed catalog or history")
		}
	}
	t.Setenv(migrationDatabaseURLEnv, f.migrationURL)
	if err := run(context.Background(), []string{"-chain", name, "-to", fmt.Sprint(finalVersion(f.chain))}, io.Discard); err != nil {
		t.Fatal(err)
	}
	before := catalogSnapshot(t, f)
	applyFixture(t, f, options{})
	if got := catalogSnapshot(t, f); got != before {
		t.Fatal("default no-op replay changed catalog or history")
	}
	for _, args := range [][]string{
		{"-chain", name, "-to", "0"}, {"-chain", name, "-to", "-1"},
		{"-chain", name, "-to", fmt.Sprint(finalVersion(f.chain) + 1)}, {"-chain", name, "-status", "-to", fmt.Sprint(f.chain.versions[0])},
		{"-chain", name, "extra"}, {"-chain", name, "-unknown"}, {}, {"-chain", "legacy"},
	} {
		t.Setenv(migrationDatabaseURLEnv, f.migrationURL)
		if err := run(context.Background(), args, io.Discard); err == nil {
			t.Fatalf("accepted invalid flags %v", args)
		}
		if got := catalogSnapshot(t, f); got != before {
			t.Fatal("invalid flags changed database")
		}
	}
	// An explicit future-version fixture proves -to cannot move backwards even
	// when a target is a real file in the currently embedded prefix.
	opt := options{hasTo: true, to: f.chain.versions[0]}
	if len(f.chain.versions) > 1 {
		if err := migrate(context.Background(), f.db, f.chain, opt, io.Discard); err == nil {
			t.Fatal("accepted target below current version")
		}
	}
	gooseMu.Lock()
	if err := goose.SetDialect("postgres"); err != nil {
		gooseMu.Unlock()
		t.Fatal(err)
	}
	sub, err := fs.Sub(corpusmigrations.FS, name)
	if err != nil {
		gooseMu.Unlock()
		t.Fatal(err)
	}
	goose.SetBaseFS(sub)
	goose.SetTableName("public." + f.chain.table)
	err = goose.DownContext(context.Background(), f.db, ".")
	gooseMu.Unlock()
	if err == nil {
		t.Fatal("Down succeeded")
	}
	if got := catalogSnapshot(t, f); got != before {
		t.Fatal("failed Down changed catalog or history")
	}
	assertInventory(t, f, finalVersion(f.chain))
	var maximum int64
	if err := f.db.QueryRow("SELECT max(version_id) FROM public." + f.chain.table + " WHERE is_applied").Scan(&maximum); err != nil {
		t.Fatal(err)
	}
	if maximum != finalVersion(f.chain) {
		t.Fatalf("version table maximum %d, want %d", maximum, finalVersion(f.chain))
	}
	t.Run("restored-portable-inventory", func(t *testing.T) {
		restored := newFixtureMode(t, name, name == "release")
		dump := postgresTool(t, nil, "pg_dump", "--dbname", f.adminURL, "--format=custom")
		list := postgresTool(t, dump, "pg_restore", "--list")
		var keep []string
		for _, line := range strings.Split(string(list), "\n") {
			if !strings.Contains(line, " EXTENSION ") && !strings.Contains(line, " SCHEMA - public ") {
				keep = append(keep, line)
			}
		}
		// The service contains the matching PostgreSQL 17 client. The runner's
		// pg_dump may be older and cannot dump a PostgreSQL 17 server.
		toc := "/tmp/" + restored.database + ".list"
		postgresTool(t, []byte(strings.Join(keep, "\n")), "sh", "-c", `cat > "$1"`, "sh", toc)
		t.Cleanup(func() { postgresTool(t, nil, "rm", "--", toc) })
		restoreURL := restored.adminURL
		if name == "release" {
			u, err := url.Parse(restoreURL)
			if err != nil {
				t.Fatal(err)
			}
			u.User = url.User("banhmi_corpus_release_restore")
			restoreURL = u.String()
		}
		postgresTool(t, dump, "pg_restore", "--dbname", restoreURL, "--no-owner", "--no-acl", "--no-comments", "--exit-on-error",
			"--role=banhmi_corpus_"+name+"_migrator", "--use-list", toc)
		assertInventory(t, restored, finalVersion(restored.chain))
		var originalOID, restoredOID int64
		query := "SELECT oid::bigint FROM pg_namespace WHERE nspname=$1"
		if err := f.db.QueryRow(query, f.chain.schemas[0]).Scan(&originalOID); err != nil {
			t.Fatal(err)
		}
		if err := restored.db.QueryRow(query, f.chain.schemas[0]).Scan(&restoredOID); err != nil {
			t.Fatal(err)
		}
		if originalOID == restoredOID {
			t.Fatal("restored fixture did not change OIDs")
		}
		applyFixture(t, restored, options{})
	})
	t.Run("existing-empty-history-replay", func(t *testing.T) {
		empty := newFixture(t, name)
		makeVersionTable(t, empty, "(0,true)")
		execSQL(t, empty.db, "DELETE FROM public."+empty.chain.table)
		assertInventory(t, empty, 0)
		applyFixture(t, empty, options{})
		assertInventory(t, empty, finalVersion(empty.chain))
	})
	if name != "control" {
		for _, mutation := range []string{"DROP EXTENSION vector CASCADE", "DROP EXTENSION btree_gist CASCADE",
			"UPDATE pg_extension SET extversion='0.0' WHERE extname='vector'", "UPDATE pg_extension SET extversion='0.0' WHERE extname='btree_gist'"} {
			t.Run(mutation, func(t *testing.T) {
				broken := newFixture(t, name)
				execSQL(t, broken.admin, mutation)
				before := catalogSnapshot(t, broken)
				// Exercise the extension checks in 00001 itself, not only the
				// catalog's rejection of objects from a wrong-version extension.
				up, err := corpusmigrationsSQL(name)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := broken.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				_, applyErr := tx.Exec(up)
				_ = tx.Rollback()
				if applyErr == nil {
					t.Fatal("00001 accepted missing or wrong-version extension")
				}
				if got := catalogSnapshot(t, broken); got != before {
					t.Fatal("failed base extension check created objects")
				}
				if err := migrate(context.Background(), broken.db, broken.chain, options{}, io.Discard); err == nil {
					t.Fatal("migration accepted invalid extension provisioning")
				}
				var schemas int
				if err := broken.admin.QueryRow(`SELECT count(*) FROM pg_namespace WHERE nspname IN ('ingest','bronze','silver','gold','config','release')`).Scan(&schemas); err != nil {
					t.Fatal(err)
				}
				if schemas != 0 {
					t.Fatal("failed migration left chain schemas")
				}
			})
		}
	}
}

func catalogSnapshot(t *testing.T, f *chainFixture) string {
	t.Helper()
	var schemas string
	if err := f.admin.QueryRow(`SELECT COALESCE(json_agg(row(nspname,nspowner) ORDER BY nspname)::text,'[]') FROM pg_namespace
        WHERE oid >= 16384 OR nspname='public'`).Scan(&schemas); err != nil {
		t.Fatal(err)
	}
	rows, err := f.admin.Query(catalogObjects, f.chain.table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out bytes.Buffer
	out.WriteString(schemas)
	for rows.Next() {
		var schema, kind, identity, owner, parent, cs, cr string
		var extension, versionObject, internal bool
		if err := rows.Scan(&schema, &kind, &identity, &owner, &extension, &versionObject, &parent, &internal, &cs, &cr); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%s/%s/%s/%s/%t/%t/%s/%t/%s/%s\n", schema, kind, identity, owner, extension, versionObject, parent, internal, cs, cr)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := f.admin.QueryRow("SELECT to_regclass($1) IS NOT NULL", "public."+f.chain.table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		var history string
		if err := f.admin.QueryRow("SELECT COALESCE(json_agg(v ORDER BY id)::text,'[]') FROM public." + f.chain.table + " v").Scan(&history); err != nil {
			t.Fatal(err)
		}
		out.WriteString(history)
	}
	return out.String()
}

func TestWriterSchemaBoundaries(t *testing.T) {
	f := newFixture(t, "writer")
	for _, version := range f.chain.versions {
		applyFixture(t, f, options{hasTo: true, to: version})
		assertInventory(t, f, version)
		var expected int
		for _, entry := range inventoryAt(f.chain, version) {
			if entry.Kind == "relation" {
				expected++
			}
		}
		var relations int
		if err := f.db.QueryRow(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
            WHERE n.nspname IN ('ingest','bronze','silver','gold','config')`).Scan(&relations); err != nil {
			t.Fatal(err)
		}
		if relations != expected {
			t.Fatalf("version %d contains %d relations, want inventory count %d", version, relations, expected)
		}
		assertWriterTableBoundaries(t, f)
	}
	for _, schema := range f.chain.schemas {
		tx, err := f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("CREATE TABLE " + schema + ".immutable_fixture (id bigint PRIMARY KEY); CREATE TRIGGER immutable_fixture BEFORE UPDATE OR DELETE ON " + schema + ".immutable_fixture FOR EACH ROW EXECUTE FUNCTION " + schema + ".reject_immutable_change(); INSERT INTO " + schema + ".immutable_fixture VALUES (1)"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		_, mutationErr := tx.Exec("UPDATE " + schema + ".immutable_fixture SET id=2")
		assertImmutableError(t, mutationErr)
		_ = tx.Rollback()
		tx, err = f.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec("CREATE TABLE " + schema + ".immutable_fixture (id bigint PRIMARY KEY); CREATE TRIGGER immutable_fixture BEFORE UPDATE OR DELETE ON " + schema + ".immutable_fixture FOR EACH ROW EXECUTE FUNCTION " + schema + ".reject_immutable_change(); INSERT INTO " + schema + ".immutable_fixture VALUES (1)"); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		_, mutationErr = tx.Exec("DELETE FROM " + schema + ".immutable_fixture")
		assertImmutableError(t, mutationErr)
		_ = tx.Rollback()
	}
	assertInventory(t, f, finalVersion(f.chain))
}

func assertWriterTableBoundaries(t *testing.T, f *chainFixture) {
	t.Helper()
	rows, err := f.db.Query(`WITH tables AS (
        SELECT c.oid, n.nspname, c.relname FROM pg_class c
        JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname IN ('ingest','bronze','silver','gold','config') AND c.relkind IN ('r','p')
    ), required_paths(table_name, columns, predicate) AS (VALUES
        ('execution_lease', ARRAY['expires_at','id'], NULL::text),
        ('execution_lease', ARRAY['next_eligible_at','id'], NULL::text),
        ('execution_result', ARRAY['execution_id','stage','substep','id'], NULL::text),
        ('revision_acceptance', ARRAY['state','watermark','id'], NULL::text),
        ('revision_acceptance', ARRAY['revision_key','processing_attempt_key','id'], NULL::text),
        ('revision_acceptance_state', ARRAY['state','watermark','id'], NULL::text),
        ('revision_acceptance_state', ARRAY['revision_key','processing_attempt_key'], NULL::text),
        ('delivery_record', ARRAY['due_at','id'], '(acknowledged_at IS NULL)'),
        ('batch_request', ARRAY['state','next_eligible_at','id'], NULL::text),
        ('batch_execution', ARRAY['state','next_poll_at','id'], NULL::text),
        ('batch_item', ARRAY['batch_execution_id','item_key'], NULL::text),
        ('batch_waiter', ARRAY['batch_request_id','workflow_id'], NULL::text),
        ('batch_waiter', ARRAY['due_at','id'], '(delivered_at IS NULL)'),
        ('quarantine_decision', ARRAY['document_version_key','id'], NULL::text),
        ('evidence_gap', ARRAY['scope_kind','scope_key','id'], NULL::text)
    )
    SELECT t.nspname || '.' || t.relname || ': missing single-column primary key'
    FROM tables t WHERE NOT EXISTS (
        SELECT 1 FROM pg_constraint k WHERE k.conrelid=t.oid AND k.contype='p' AND cardinality(k.conkey)=1)
    UNION ALL
    SELECT t.nspname || '.' || t.relname || '.' || k.conname || ': cross-schema foreign key'
    FROM tables t JOIN pg_constraint k ON k.conrelid=t.oid AND k.contype='f'
    JOIN pg_class parent ON parent.oid=k.confrelid JOIN pg_namespace pn ON pn.oid=parent.relnamespace
    WHERE pn.nspname <> t.nspname
    UNION ALL
    SELECT t.nspname || '.' || t.relname || '.' || a.attname || ': country discriminator'
    FROM tables t JOIN pg_attribute a ON a.attrelid=t.oid
    WHERE a.attnum > 0 AND NOT a.attisdropped AND (a.attname='country' OR a.attname LIKE 'country\_%' ESCAPE '\')
      AND NOT (t.nspname='ingest' AND t.relname='country_admission' AND a.attname='country_code')
    UNION ALL
    SELECT t.nspname || '.' || t.relname || '.' || k.conname || ': missing parent-leading index'
    FROM tables t JOIN pg_constraint k ON k.conrelid=t.oid AND k.contype='f'
    WHERE NOT EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid=t.oid
        AND i.indisvalid AND i.indisready AND i.indpred IS NULL AND i.indnkeyatts >= cardinality(k.conkey)
        AND NOT EXISTS (SELECT 1 FROM unnest(k.conkey) WITH ORDINALITY AS fkcol(attnum, ordinal)
            WHERE i.indkey[(fkcol.ordinal-1)::integer] <> fkcol.attnum))
    UNION ALL
    SELECT t.nspname || '.' || t.relname || ': missing required index on (' || array_to_string(r.columns, ',') || ')'
    FROM tables t JOIN required_paths r ON t.nspname='ingest' AND t.relname=r.table_name
    WHERE NOT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class idx ON idx.oid=i.indexrelid
        JOIN pg_am am ON am.oid=idx.relam
        WHERE i.indrelid=t.oid AND i.indisvalid AND i.indisready AND am.amname='btree'
          AND pg_get_expr(i.indpred, i.indrelid) IS NOT DISTINCT FROM r.predicate
          AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY AS keycol(attnum, ordinal)
              JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=keycol.attnum
              WHERE keycol.ordinal <= cardinality(r.columns) AND keycol.ordinal <= i.indnkeyatts
              ORDER BY keycol.ordinal) = r.columns)
    UNION ALL
    SELECT n.nspname || '.' || c.relname || ': invalid or unready index'
    FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname IN ('ingest','bronze','silver','gold','config') AND (NOT i.indisvalid OR NOT i.indisready)`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var violation string
		if err := rows.Scan(&violation); err != nil {
			t.Fatal(err)
		}
		t.Error(violation)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func assertImmutableError(t *testing.T, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "55000" {
		t.Fatalf("mutation must fail with PostgreSQL state 55000: %v", err)
	}
}

// The container identifier is supplied only by the disposable generation job.
func postgresTool(t *testing.T, input []byte, name string, args ...string) []byte {
	t.Helper()
	container := os.Getenv("BANHMI_CORPUS_TEST_POSTGRES_CONTAINER")
	if container == "" {
		t.Fatal("disposable PostgreSQL container is required for restore fixtures")
	}
	command := append([]string{"exec", "-i", "--user", "postgres", container, name}, args...)
	cmd := exec.Command("docker", command...)
	cmd.Stdin = bytes.NewReader(input)
	var output, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("fixture PostgreSQL %s: %v\n%s", name, err, stderr.String())
	}
	return output.Bytes()
}
