package document

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"danny.vn/banhmi/internal/corpus/model"
)

//go:embed queries/revisions.sql
var revisionQueries string

const identityDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const identityKey = "vn:doc:00000000-0000-0000-0000-000000000001"
const otherIdentityKey = "vn:doc:00000000-0000-0000-0000-000000000002"
const observationKey = "observation-00000000000000000000000000000001"
const executionKey = "execution-00000000000000000000000000000001"
const revisionKey = identityKey + ":rev:" + identityDigest

func identityTestURL(t *testing.T) *url.URL {
	t.Helper()
	if testing.Short() {
		t.Skip("identity schema tests require hosted PostgreSQL")
	}
	value := os.Getenv("BANHMI_CORPUS_WRITER_TEST_DATABASE_URL")
	if value == "" {
		t.Skip("BANHMI_CORPUS_WRITER_TEST_DATABASE_URL is absent")
	}
	u, err := url.Parse(value)
	if err != nil || u == nil || u.User == nil {
		t.Fatal("invalid synthetic writer test URL")
	}
	_, password := u.User.Password()
	if (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		(u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") ||
		u.Port() != "5432" || u.Path != "/banhmi_corpus_writer_ci" ||
		u.User.Username() != "banhmi_corpus_writer_migrator" || password ||
		u.Fragment != "" || len(u.Query()) != 1 || u.Query().Get("sslmode") != "disable" {
		t.Fatal("schema tests require the isolated password-free CI writer service")
	}
	return u
}

type identityDatabase struct {
	ctx  context.Context
	conn *pgx.Conn
	u    *url.URL
}

func identityFixture(t *testing.T) identityDatabase {
	t.Helper()
	u := identityTestURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	adminURL := *u
	adminURL.User, adminURL.Path = url.User("postgres"), "/postgres"
	admin, err := pgx.Connect(ctx, adminURL.String())
	if err != nil {
		t.Fatal("connect disposable identity administrator failed")
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	name := fmt.Sprintf("banhmi_identity_%d_%d", os.Getpid(), time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier+" TEMPLATE banhmi_corpus_writer_ci OWNER banhmi_corpus_writer_migrator"); err != nil {
		t.Fatalf("copy replayed identity database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)"); err != nil {
			t.Errorf("drop disposable identity database: %v", err)
		}
	})
	u.Path = "/" + name
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal("connect disposable identity migrator failed")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return identityDatabase{ctx: ctx, conn: conn, u: u}
}

func identityTx(t *testing.T, f identityDatabase, app bool) pgx.Tx {
	t.Helper()
	conn := f.conn
	if app {
		u := *f.u
		u.User = url.User("banhmi_corpus_writer_app")
		var err error
		conn, err = pgx.Connect(f.ctx, u.String())
		if err != nil {
			t.Fatal("connect disposable identity app failed")
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
	}
	tx, err := conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func identityExec(t *testing.T, tx pgx.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), query, args...); err != nil {
		t.Fatalf("identity fixture SQL: %v", err)
	}
}

func identityState(t *testing.T, tx pgx.Tx, state, query string, args ...any) {
	t.Helper()
	identityExec(t, tx, "SAVEPOINT negative_case")
	_, err := tx.Exec(context.Background(), query, args...)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != state {
		t.Fatalf("operation succeeded or returned %v, want PostgreSQL state %s", err, state)
	}
	identityExec(t, tx, "ROLLBACK TO SAVEPOINT negative_case")
	identityExec(t, tx, "RELEASE SAVEPOINT negative_case")
}

func identityQuery(t *testing.T, name string) string {
	t.Helper()
	_, tail, ok := strings.Cut(revisionQueries, "-- name: "+name+" :")
	if !ok {
		t.Fatal("missing revision query " + name)
	}
	_, tail, ok = strings.Cut(tail, "\n")
	if !ok {
		t.Fatal("missing revision query body " + name)
	}
	body, _, _ := strings.Cut(tail, "\n-- name:")
	return body
}

func identityID(t *testing.T, tx pgx.Tx, name string, args ...any) (int64, error) {
	t.Helper()
	rows, err := tx.Query(context.Background(), identityQuery(t, name), args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, err
		}
		return 0, pgx.ErrNoRows
	}
	values, err := rows.Values()
	if err != nil {
		return 0, err
	}
	id, ok := values[0].(int64)
	if !ok {
		t.Fatal("revision query must return bigint id first")
	}
	if rows.Next() {
		t.Fatal("revision query returned multiple rows")
	}
	return id, rows.Err()
}

func requireIdentityID(t *testing.T, tx pgx.Tx, name string, args ...any) int64 {
	t.Helper()
	id, err := identityID(t, tx, name, args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return id
}

func noIdentityRow(t *testing.T, tx pgx.Tx, name string, args ...any) {
	t.Helper()
	if _, err := identityID(t, tx, name, args...); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("%s: want no row, got %v", name, err)
	}
}

func identityCount(t *testing.T, tx pgx.Tx, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Seed only synthetic cross-schema references. Runtime tests commit this fixture
// before connecting through the app login, never SET ROLE as the migrator.
func seedIdentityExecution(t *testing.T, tx pgx.Tx) int64 {
	t.Helper()
	identityExec(t, tx, `INSERT INTO ingest.country_admission
		(id, country_code, admission_epoch, admitted_writer, enabled) VALUES (1, 'vn', 1, 'writer-a', true)`)
	identityExec(t, tx, `INSERT INTO ingest.execution_lease
		(resource_kind, resource_key, owner, attempt_fence, admission_epoch, expires_at)
		VALUES ('document_version', 'version-a', 'owner-a', 1, 1, clock_timestamp() + interval '1 hour')`)
	var id int64
	if err := tx.QueryRow(context.Background(), `INSERT INTO ingest.execution
		(execution_key, workflow_id, workflow_kind, processing_attempt_key, attempt_fence,
		 admission_epoch, scope_kind, resource_kind, resource_key, input_digest, recipe_digest,
		 budget_digest, budget, run_receipts)
		VALUES ($1, 'document-ingest/vn/source-a/version-a', 'DocumentIngest', 'attempt-a', 1,
		 1, 'intake', 'document_version', 'version-a', $2, $2, $2, '{}', '[]') RETURNING id`,
		executionKey, identityDigest).Scan(&id); err != nil {
		t.Fatal(err)
	}
	identityExec(t, tx, `INSERT INTO bronze.source_record (source_id, external_id) VALUES ('source-a', 'external-a')`)
	identityExec(t, tx, `INSERT INTO bronze.blob (sha256, byte_size, media_type, object_ref,
		capture_source, capture_ref, captured_at) VALUES ($1, 1, 'text/plain', 'synthetic-object',
		'source-a', 'capture-00000000000000000000000000000001', clock_timestamp())`, identityDigest)
	identityExec(t, tx, `INSERT INTO bronze.observation_revision (observation_key, source_record_id,
		observation_digest, metadata_digest, source_tree_digest, relations_digest, source_window_key,
		document_version_key, execution_key, result_key, capture_blob_id, observed_at)
		SELECT $1, s.id, $2, $2, $2, $2, 'window-a', 'version-a', $3,
		'result-00000000000000000000000000000001', b.id, clock_timestamp()
		FROM bronze.source_record s CROSS JOIN bronze.blob b`, observationKey, identityDigest, executionKey)
	return id
}

func identityFence(id int64, payload ...any) []any {
	return append([]any{int64(1), "writer-a", "document_version", "version-a", "owner-a", int64(1),
		id, identityDigest, identityDigest, identityDigest}, payload...)
}

func mintArgs(id int64, key string) []any {
	return identityFence(id, key, observationKey, "source-a", "external-a", "50/2024/TT-NHNN", "thong-tu", identityDigest)
}

func revisionArgs(execution, identity int64) []any {
	return identityFence(execution, identity, revisionKey, identityDigest, identityDigest, identityDigest,
		identityDigest, identityDigest, identityDigest, identityDigest, "50/2024/TT-NHNN", "thong-tu")
}

func provisionPayload(key string) string {
	return `[{"provision_key":"` + key + `","local_key":"article-1","ordinal":0,` +
		`"native_citation_hex":"c49069e1bb81752031","source_anchors":[{"page":1}],"text_hash":"` + identityDigest + `"}]`
}

func seedRevisions(t *testing.T, tx pgx.Tx, execution int64) (int64, int64) {
	t.Helper()
	identity := requireIdentityID(t, tx, "MintDocumentIdentity", mintArgs(execution, identityKey)...)
	revision := requireIdentityID(t, tx, "InsertDocumentRevision", revisionArgs(execution, identity)...)
	requireIdentityID(t, tx, "InsertProvisionRevisions", identityFence(execution, revision,
		provisionPayload(revisionKey+":provision:article-1"))...)
	return identity, revision
}

func TestImmutableRevision(t *testing.T) {
	t.Run("replay", testIdentityRevisionReplay)
	t.Run("immutable_tables", testIdentityImmutableTables)
}

func testIdentityRevisionReplay(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	identity, revision := seedRevisions(t, tx, execution)
	if got := requireIdentityID(t, tx, "MintDocumentIdentity", mintArgs(execution, otherIdentityKey)...); got != identity {
		t.Fatal("identical assertion minted another identity")
	}
	if got := requireIdentityID(t, tx, "InsertDocumentRevision", revisionArgs(execution, identity)...); got != revision {
		t.Fatal("identical revision inputs created another row")
	}
	args := revisionArgs(execution, identity)
	args[14] = strings.Repeat("b", 64)
	noIdentityRow(t, tx, "InsertDocumentRevision", args...)
	if got := identityCount(t, tx, "SELECT count(*) FROM silver.document_revision"); got != 1 {
		t.Fatal("metadata conflict wrote a revision")
	}
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.document_revision WHERE metadata_hash = $1 AND intake_execution_id = $2", identityDigest, execution); n != 1 {
		t.Fatal("metadata conflict changed the original hashes or provenance")
	}
	args[11] = identityKey + ":rev:" + strings.Repeat("b", 64)
	requireIdentityID(t, tx, "InsertDocumentRevision", args...)
	if got := requireIdentityID(t, tx, "ReadDocumentIdentity", identityKey); got != identity {
		t.Fatal("identity lookup lost minted identity")
	}
	if got := requireIdentityID(t, tx, "ReadIdentityByAssertion", observationKey, "source-a", "external-a",
		"50/2024/TT-NHNN", "thong-tu", identityDigest); got != identity {
		t.Fatal("assertion lookup lost identity")
	}
	if got := requireIdentityID(t, tx, "ReadDocumentRevision", revisionKey); got != revision {
		t.Fatal("revision lookup lost revision")
	}
	requireIdentityID(t, tx, "InsertProvisionRevisions", identityFence(execution, revision,
		provisionPayload(revisionKey+":provision:article-1"))...)
	if got := identityCount(t, tx, "SELECT count(*) FROM silver.document_identity"); got != 1 {
		t.Fatal("replay left an orphan identity")
	}
	if got := identityCount(t, tx, "SELECT count(*) FROM silver.provision_revision"); got != 1 {
		t.Fatal("provision replay duplicated rows")
	}
	var citation []byte
	var anchors []byte
	var id int64
	var key, local, hash string
	var ordinal int32
	var stable, continuity any
	if err := tx.QueryRow(f.ctx, identityQuery(t, "ReadProvisionRevisions"), revision).
		Scan(&id, &key, &local, &ordinal, &citation, &anchors, &hash, &stable, &continuity); err != nil {
		t.Fatal(err)
	}
	if string(citation) != "Điều 1" || !json.Valid(anchors) || !strings.Contains(string(anchors), "page") {
		t.Fatal("provision lost citation bytes or separate source anchors")
	}
}

func testIdentityImmutableTables(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	identity, _ := seedRevisions(t, tx, execution)
	requireIdentityID(t, tx, "InsertLegacyIdentityMapping", identityFence(execution,
		identityDigest, "document", "legacy-a", "resolved", identity, nil, "vn")...)
	for _, table := range []string{"document_identity", "identity_assertion", "legacy_identity_mapping", "document_revision", "provision_revision"} {
		identityState(t, tx, "55000", "UPDATE silver."+table+" SET id = id")
		identityState(t, tx, "55000", "DELETE FROM silver."+table)
		if n := identityCount(t, tx, "SELECT count(*) FROM silver."+table); n != 1 {
			t.Fatalf("immutable %s lost evidence", table)
		}
	}
}

func TestIdentityCountryGuard(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	wrong := strings.Replace(identityKey, "vn:", "my:", 1)
	noIdentityRow(t, tx, "MintDocumentIdentity", mintArgs(execution, wrong)...)
	identityState(t, tx, "23514", `INSERT INTO silver.document_identity
		(identity_key, entity_kind, created_by_execution_key) VALUES ($1, 'document', $2)`, wrong, executionKey)
	identity, _ := seedRevisions(t, tx, execution)
	args := revisionArgs(execution, identity)
	args[11] = otherIdentityKey + ":rev:" + identityDigest
	identityState(t, tx, "23514", identityQuery(t, "InsertDocumentRevision"), args...)
	identityState(t, tx, "23514", `INSERT INTO silver.document_identity
		(identity_key, entity_kind, created_by_execution_key) VALUES ('vn:doc:bad', 'document', $1)`, executionKey)
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.document_identity"); n != 1 {
		t.Fatal("country rejection left evidence")
	}
}

func TestIdentityKeyRejectsUppercaseUUID(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	seedRevisions(t, tx, execution)
	identityState(t, tx, "23514", `INSERT INTO silver.document_identity
		(identity_key, entity_kind, created_by_execution_key) VALUES ($1, 'document', $2)`,
		"vn:doc:AAAAAAAA-0000-0000-0000-000000000003", executionKey)
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.document_identity"); n != 1 {
		t.Fatal("uppercase UUID rejection changed the identity count")
	}
}

func TestDocumentRevisionConstraintGuards(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	identity, _ := seedRevisions(t, tx, execution)
	insert := `INSERT INTO silver.document_revision (document_identity_id, revision_key,
		source_observation_set_hash, text_hash, metadata_hash, conversion_recipe_digest,
		normalization_recipe_digest, parser_recipe_digest, policy_recipe_digest,
		normalized_number, document_type, document_version_key, intake_execution_id, intake_execution_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`
	// Use an unused revision key so uniqueness cannot mask a missing safeguard.
	args := []any{identity, identityKey + ":rev:" + strings.Repeat("b", 64),
		identityDigest, identityDigest, identityDigest, identityDigest, identityDigest,
		identityDigest, identityDigest, "50/2024/TT-NHNN", "thong-tu", "version-a", execution, executionKey}
	cases := []struct {
		name  string
		index int
		value any
		state string
	}{
		{"revision_digest_63", 1, identityKey + ":rev:" + strings.Repeat("b", 63), "23514"},
		{"revision_digest_65", 1, identityKey + ":rev:" + strings.Repeat("b", 65), "23514"},
		{"revision_digest_non_hex", 1, identityKey + ":rev:" + strings.Repeat("g", 64), "23514"},
		{"source_observation_set_hash", 2, strings.Repeat("g", 64), "23514"},
		{"text_hash", 3, strings.Repeat("g", 64), "23514"},
		{"metadata_hash", 4, strings.Repeat("g", 64), "23514"},
		{"conversion_recipe_digest", 5, strings.Repeat("g", 64), "23514"},
		{"normalization_recipe_digest", 6, strings.Repeat("g", 64), "23514"},
		{"parser_recipe_digest", 7, strings.Repeat("g", 64), "23514"},
		{"policy_recipe_digest", 8, strings.Repeat("g", 64), "23514"},
		{"null_intake_execution_id", 12, nil, "23502"},
		{"null_intake_execution_key", 13, nil, "23502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := append([]any(nil), args...)
			bad[tc.index] = tc.value
			identityState(t, tx, tc.state, insert, bad...)
			if n := identityCount(t, tx, "SELECT count(*) FROM silver.document_revision"); n != 1 {
				t.Fatal("constraint rejection changed the revision count")
			}
		})
	}
	// The otherwise identical direct insert must succeed with valid fields.
	identityExec(t, tx, insert, args...)
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.document_revision"); n != 2 {
		t.Fatal("valid direct revision insert did not create one row")
	}
}

func TestProvisionRevisionsRejectDifferentDocumentVersion(t *testing.T) {
	f := identityFixture(t)
	seed := identityTx(t, f, false)
	executionA := seedIdentityExecution(t, seed)
	identityExec(t, seed, `INSERT INTO ingest.execution_lease
		(resource_kind, resource_key, owner, attempt_fence, admission_epoch, expires_at)
		VALUES ('document_version', 'version-b', 'owner-b', 1, 1, clock_timestamp() + interval '1 hour')`)
	var executionB int64
	if err := seed.QueryRow(f.ctx, `INSERT INTO ingest.execution
		(execution_key, workflow_id, workflow_kind, processing_attempt_key, attempt_fence,
		 admission_epoch, scope_kind, resource_kind, resource_key, input_digest, recipe_digest,
		 budget_digest, budget, run_receipts)
		VALUES ('execution-00000000000000000000000000000002',
		 'document-ingest/vn/source-a/version-b', 'DocumentIngest', 'attempt-b', 1,
		 1, 'intake', 'document_version', 'version-b', $1, $1, $1, '{}', '[]') RETURNING id`,
		identityDigest).Scan(&executionB); err != nil {
		t.Fatal(err)
	}
	if err := seed.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	tx := identityTx(t, f, true)
	identity := requireIdentityID(t, tx, "MintDocumentIdentity", mintArgs(executionA, identityKey)...)
	revision := requireIdentityID(t, tx, "InsertDocumentRevision", revisionArgs(executionA, identity)...)
	// Prove version B's fence is valid through the same app login.
	args := revisionArgs(executionB, identity)
	args[3], args[4] = "version-b", "owner-b"
	args[11] = identityKey + ":rev:" + strings.Repeat("b", 64)
	requireIdentityID(t, tx, "InsertDocumentRevision", args...)
	payload := provisionPayload(revisionKey + ":provision:article-1")
	args = identityFence(executionB, revision, payload)
	args[3], args[4] = "version-b", "owner-b"
	noIdentityRow(t, tx, "InsertProvisionRevisions", args...)
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.provision_revision WHERE document_revision_id = $1", revision); n != 0 {
		t.Fatal("version B inserted provisions under version A's revision")
	}
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.provision_revision"); n != 0 {
		t.Fatal("wrong-version bulk call inserted provisions")
	}
	// Version A must accept the same parent and payload with its own valid fence.
	requireIdentityID(t, tx, "InsertProvisionRevisions", identityFence(executionA, revision, payload)...)
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.provision_revision WHERE document_revision_id = $1", revision); n != 1 {
		t.Fatal("owning version did not insert its provision")
	}
}

func TestLegacyIdentityMappingOutcomes(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	identity, _ := seedRevisions(t, tx, execution)
	args := identityFence(execution, identityDigest, "document", "legacy-a", "resolved", identity, nil, "vn")
	mapping := requireIdentityID(t, tx, "InsertLegacyIdentityMapping", args...)
	if requireIdentityID(t, tx, "InsertLegacyIdentityMapping", args...) != mapping {
		t.Fatal("mapping replay changed identity")
	}
	if requireIdentityID(t, tx, "ResolveLegacyIdentity", "document", "legacy-a") != identity {
		t.Fatal("resolved mapping did not resolve")
	}
	identityExec(t, tx, `INSERT INTO silver.legacy_identity_mapping
		(export_manifest_digest, entity_kind, legacy_id, state, document_identity_id, importing_execution_key)
		VALUES ($1, 'document', 'legacy-a', 'resolved', $2, $3)`, strings.Repeat("b", 64), identity, executionKey)
	requireIdentityID(t, tx, "ResolveLegacyIdentity", "document", "legacy-a")
	identityExec(t, tx, `INSERT INTO silver.legacy_identity_mapping
		(export_manifest_digest, entity_kind, legacy_id, state, candidate_report_hash, importing_execution_key)
		VALUES ($1, 'document', 'legacy-a', 'ambiguous', $2, $3)`, strings.Repeat("c", 64), identityDigest, executionKey)
	noIdentityRow(t, tx, "ResolveLegacyIdentity", "document", "legacy-a")
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.legacy_identity_mapping WHERE legacy_id = 'legacy-a'"); n != 3 {
		t.Fatal("ambiguity erased mapping history")
	}
	for i, state := range []model.LegacyIdentityMappingState{model.LegacyIdentityResolved, model.LegacyIdentityAmbiguous, model.LegacyIdentityUnavailable} {
		var target, report any
		if state == model.LegacyIdentityResolved {
			target = identity
		}
		if state == model.LegacyIdentityAmbiguous {
			report = identityDigest
		}
		payload := identityFence(execution, identityDigest, "document", fmt.Sprintf("state-%d", i), string(state), target, report, "vn")
		requireIdentityID(t, tx, "InsertLegacyIdentityMapping", payload...)
		// Every state's opposite identity nullability is rejected by the CHECK.
		if target == nil {
			target = identity
		} else {
			target = nil
		}
		identityState(t, tx, "23514", `INSERT INTO silver.legacy_identity_mapping
			(export_manifest_digest, entity_kind, legacy_id, state, document_identity_id,
			candidate_report_hash, importing_execution_key) VALUES ($1, 'document', 'bad-state', $2, $3, $4, $5)`,
			identityDigest, string(state), target, report, executionKey)
	}
	identityState(t, tx, "23514", `INSERT INTO silver.legacy_identity_mapping
		(export_manifest_digest, entity_kind, legacy_id, state, importing_execution_key)
		VALUES ($1, 'document', 'missing-report', 'ambiguous', $2)`, identityDigest, executionKey)
	identityState(t, tx, "23514", `INSERT INTO silver.legacy_identity_mapping
		(export_manifest_digest, entity_kind, legacy_id, state, document_identity_id, candidate_report_hash, importing_execution_key)
		VALUES ($1, 'document', 'extra-report', 'resolved', $2, $1, $3)`, identityDigest, identity, executionKey)
	identityState(t, tx, "23514", `INSERT INTO silver.legacy_identity_mapping
		(export_manifest_digest, entity_kind, legacy_id, state, candidate_report_hash, importing_execution_key)
		VALUES ($1, 'document', 'extra-report', 'unavailable', $1, $2)`, identityDigest, executionKey)
	args[16] = "my"
	noIdentityRow(t, tx, "InsertLegacyIdentityMapping", args...)
	noIdentityRow(t, tx, "ResolveLegacyIdentity", "document", "missing")
	noIdentityRow(t, tx, "ResolveLegacyIdentity", "document", "state-1")
	noIdentityRow(t, tx, "ResolveLegacyIdentity", "document", "state-2")
	identityExec(t, tx, `INSERT INTO silver.document_identity
		(identity_key, entity_kind, created_by_execution_key) VALUES ($1, 'document', $2)`, otherIdentityKey, executionKey)
	other := identityCount(t, tx, "SELECT id FROM silver.document_identity WHERE identity_key = $1", otherIdentityKey)
	identityExec(t, tx, `INSERT INTO silver.legacy_identity_mapping
		(export_manifest_digest, entity_kind, legacy_id, state, document_identity_id, importing_execution_key)
		VALUES ($1, 'document', 'disagree', 'resolved', $2, $3),
		($4, 'document', 'disagree', 'resolved', $5, $3)`, identityDigest, identity, executionKey, strings.Repeat("b", 64), other)
	noIdentityRow(t, tx, "ResolveLegacyIdentity", "document", "disagree")
	// The separate read preserves the candidate report rather than choosing an ID.
	rows, err := tx.Query(f.ctx, identityQuery(t, "ReadLegacyIdentityMappings"), "document", "legacy-a")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seen int
	for rows.Next() {
		seen++
		values, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		if values[2] == "ambiguous" && (values[3] != nil || values[4] != identityDigest) {
			t.Fatal("ambiguous mapping selected an identity or lost its report")
		}
	}
	if rows.Err() != nil || seen != 3 {
		t.Fatal("mapping history read lost retained mappings")
	}
}

func TestProvisionKeysAndContinuity(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	identity, revision := seedRevisions(t, tx, execution)
	bad := provisionPayload(identityKey + ":rev:" + strings.Repeat("b", 64) + ":provision:article-1")
	// Use a new local key so a uniqueness constraint cannot mask the key guard.
	bad = strings.ReplaceAll(bad, "article-1", "article-2")
	bad = strings.ReplaceAll(bad, `"ordinal":0`, `"ordinal":1`)
	identityState(t, tx, "23514", identityQuery(t, "InsertProvisionRevisions"), identityFence(execution, revision, bad)...)
	assertion := requireIdentityID(t, tx, "AppendIdentityAssertion", identityFence(execution, identity,
		observationKey, "source-a", "external-a", "50/2024/TT-NHNN", "thong-tu", identityDigest,
		"continuity", nil, "confirmed", identityDigest)...)
	insert := `INSERT INTO silver.provision_revision (document_revision_id, provision_key,
		local_key, ordinal, native_citation, source_anchors, text_hash, stable_provision_uuid, continuity_assertion_id)
		VALUES ($1, $2, 'article-2', 1, $3, '[]', $4, $5, $6)`
	stable := "00000000-0000-0000-0000-000000000003"
	identityState(t, tx, "23514", insert, revision, revisionKey+":provision:article-2", []byte("Điều 2"), identityDigest, stable, nil)
	identityState(t, tx, "23514", insert, revision, revisionKey+":provision:article-2", []byte("Điều 2"), identityDigest, nil, assertion)
	observed := identityCount(t, tx, "SELECT id FROM silver.identity_assertion WHERE kind = 'observed'")
	identityState(t, tx, "23514", insert, revision, revisionKey+":provision:article-2", []byte("Điều 2"), identityDigest, stable, observed)
	unreviewed := requireIdentityID(t, tx, "AppendIdentityAssertion", identityFence(execution, identity,
		observationKey, "source-a", "external-a", "50/2024/TT-NHNN", "thong-tu", identityDigest,
		"continuity", nil, "unreviewed", identityDigest)...)
	identityState(t, tx, "23514", insert, revision, revisionKey+":provision:article-2", []byte("Điều 2"), identityDigest, stable, unreviewed)
	identityExec(t, tx, `INSERT INTO silver.document_identity
		(identity_key, entity_kind, created_by_execution_key) VALUES ($1, 'document', $2)`, otherIdentityKey, executionKey)
	other := identityCount(t, tx, "SELECT id FROM silver.document_identity WHERE identity_key = $1", otherIdentityKey)
	wrongParent := requireIdentityID(t, tx, "AppendIdentityAssertion", identityFence(execution, other,
		observationKey, "source-a", "external-a", "50/2024/TT-NHNN", "thong-tu", identityDigest,
		"continuity", nil, "confirmed", identityDigest)...)
	identityState(t, tx, "23514", insert, revision, revisionKey+":provision:article-2", []byte("Điều 2"), identityDigest, stable, wrongParent)
	identityExec(t, tx, insert, revision, revisionKey+":provision:article-2", []byte("Điều 2"), identityDigest, stable, assertion)
	changed := strings.Replace(provisionPayload(revisionKey+":provision:article-1"), identityDigest, strings.Repeat("b", 64), 1)
	noIdentityRow(t, tx, "InsertProvisionRevisions", identityFence(execution, revision, changed)...)
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.provision_revision"); n != 2 {
		t.Fatal("failed provision insert changed evidence")
	}
}

func TestIdentityClosedVocabularies(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	identity, _ := seedRevisions(t, tx, execution)
	identityExec(t, tx, `INSERT INTO silver.document_identity
		(identity_key, entity_kind, created_by_execution_key) VALUES ($1, $2, $3)`,
		otherIdentityKey, string(model.IdentityEntityDocument), executionKey)
	other := identityCount(t, tx, "SELECT id FROM silver.document_identity WHERE identity_key = $1", otherIdentityKey)
	for _, kind := range []model.IdentityAssertionKind{model.IdentityAssertionObserved, model.IdentityAssertionMerge, model.IdentityAssertionSplit, model.IdentityAssertionContinuity} {
		var related any
		if kind == model.IdentityAssertionMerge || kind == model.IdentityAssertionSplit {
			related = other
		}
		for _, state := range []model.IdentityReviewState{model.IdentityReviewUnreviewed, model.IdentityReviewConfirmed, model.IdentityReviewRejected, model.IdentityReviewUnresolved} {
			args := identityFence(execution, identity, observationKey, "source-a", "external-a", "number-"+string(kind),
				"thong-tu", identityDigest, string(kind), related, string(state), identityDigest)
			id := requireIdentityID(t, tx, "AppendIdentityAssertion", args...)
			if got := requireIdentityID(t, tx, "AppendIdentityAssertion", args...); got != id {
				t.Fatal("assertion replay created another row")
			}
		}
	}
	identityState(t, tx, "23514", `INSERT INTO silver.document_identity
		(identity_key, entity_kind, created_by_execution_key) VALUES ($1, 'outsider', $2)`,
		"vn:doc:00000000-0000-0000-0000-000000000003", executionKey)
	assertionSQL := `INSERT INTO silver.identity_assertion (document_identity_id, observation_key, source_id,
		external_id, normalized_number, document_type, title_hash, kind, review_state, evidence_hash, intake_execution_key)
		VALUES ($1, $2, 'source-a', 'external-a', '', '', $3, $4, $5, $3, $6)`
	identityState(t, tx, "23514", assertionSQL, identity, observationKey, identityDigest, "outsider", "unreviewed", executionKey)
	identityState(t, tx, "23514", assertionSQL, identity, observationKey, identityDigest, "observed", "outsider", executionKey)
	mappingSQL := `INSERT INTO silver.legacy_identity_mapping (export_manifest_digest, entity_kind,
		legacy_id, state, importing_execution_key) VALUES ($1, $2, 'outsider', $3, $4)`
	identityState(t, tx, "23514", mappingSQL, identityDigest, "outsider", "unavailable", executionKey)
	identityState(t, tx, "23514", mappingSQL, identityDigest, "document", "outsider", executionKey)
	// Append-only decisions leave both old keys readable after merges and splits.
	requireIdentityID(t, tx, "ReadDocumentIdentity", identityKey)
	requireIdentityID(t, tx, "ReadDocumentIdentity", otherIdentityKey)
}

func TestIdentityRuntimePrivileges(t *testing.T) {
	f := identityFixture(t)
	seed := identityTx(t, f, false)
	execution := seedIdentityExecution(t, seed)
	if err := seed.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	tx := identityTx(t, f, true)
	identity, revision := seedRevisions(t, tx, execution)
	requireIdentityID(t, tx, "AppendIdentityAssertion", identityFence(execution, identity, observationKey,
		"source-a", "external-a", "50/2024/TT-NHNN", "thong-tu", identityDigest,
		"continuity", nil, "confirmed", identityDigest)...)
	requireIdentityID(t, tx, "InsertLegacyIdentityMapping", identityFence(execution, identityDigest,
		"document", "legacy-app", "resolved", identity, nil, "vn")...)
	requireIdentityID(t, tx, "ReadDocumentIdentity", identityKey)
	requireIdentityID(t, tx, "ReadDocumentRevision", revisionKey)
	requireIdentityID(t, tx, "ReadProvisionRevisions", revision)
	requireIdentityID(t, tx, "ResolveLegacyIdentity", "document", "legacy-app")
	for _, table := range []string{"document_identity", "identity_assertion", "legacy_identity_mapping", "document_revision", "provision_revision"} {
		if identityCount(t, tx, "SELECT count(*) FROM silver."+table) == 0 {
			t.Fatalf("runtime cannot read inserted %s", table)
		}
		identityState(t, tx, "42501", "UPDATE silver."+table+" SET id = id")
		identityState(t, tx, "42501", "DELETE FROM silver."+table)
		identityState(t, tx, "42501", "TRUNCATE silver."+table+" CASCADE")
	}
	// Export can read all five tables but cannot insert or mutate them.
	if err := tx.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	u := *f.u
	u.User = url.User("banhmi_corpus_writer_exporter")
	conn, err := pgx.Connect(f.ctx, u.String())
	if err != nil {
		t.Fatal("connect disposable exporter failed")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	export, err := conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = export.Rollback(context.Background()) })
	for _, table := range []string{"document_identity", "identity_assertion", "legacy_identity_mapping", "document_revision", "provision_revision"} {
		if identityCount(t, export, "SELECT count(*) FROM silver."+table) == 0 {
			t.Fatalf("export cannot read %s", table)
		}
		identityState(t, export, "42501", "INSERT INTO silver."+table+" DEFAULT VALUES")
		identityState(t, export, "42501", "UPDATE silver."+table+" SET id = id")
		identityState(t, export, "42501", "DELETE FROM silver."+table)
		identityState(t, export, "42501", "TRUNCATE silver."+table+" CASCADE")
	}
}

func TestIdentityMutationFences(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	identity, revision := seedRevisions(t, tx, execution)
	mutations := []struct {
		name string
		args []any
	}{
		{"MintDocumentIdentity", mintArgs(execution, otherIdentityKey)},
		{"AppendIdentityAssertion", identityFence(execution, identity, observationKey, "source-a", "external-a",
			"50/2024/TT-NHNN", "thong-tu", identityDigest, "continuity", nil, "confirmed", identityDigest)},
		{"InsertLegacyIdentityMapping", identityFence(execution, identityDigest, "document", "fenced", "resolved", identity, nil, "vn")},
		{"InsertDocumentRevision", revisionArgs(execution, identity)},
		{"InsertProvisionRevisions", identityFence(execution, revision, provisionPayload(revisionKey+":provision:article-1"))},
	}
	for _, mutation := range mutations {
		for index, value := range map[int]any{0: int64(2), 1: "wrong-writer", 2: "source_window", 3: "wrong-version",
			4: "wrong-owner", 5: int64(2), 6: execution + 1000, 7: strings.Repeat("b", 64),
			8: strings.Repeat("b", 64), 9: strings.Repeat("b", 64)} {
			args := append([]any(nil), mutation.args...)
			args[index] = value
			noIdentityRow(t, tx, mutation.name, args...)
		}
	}
	identityExec(t, tx, "UPDATE ingest.execution_lease SET cancelled_at = clock_timestamp()")
	for _, mutation := range mutations {
		noIdentityRow(t, tx, mutation.name, mutation.args...)
	}
	identityExec(t, tx, "UPDATE ingest.execution_lease SET cancelled_at = NULL, expires_at = clock_timestamp() - interval '1 second'")
	for _, mutation := range mutations {
		noIdentityRow(t, tx, mutation.name, mutation.args...)
	}
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.identity_assertion"); n != 1 {
		t.Fatal("stale mutation appended an assertion")
	}
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.legacy_identity_mapping"); n != 0 {
		t.Fatal("stale mutation appended a mapping")
	}
}

func TestProvisionBulkAtomicity(t *testing.T) {
	f := identityFixture(t)
	tx := identityTx(t, f, false)
	execution := seedIdentityExecution(t, tx)
	_, revision := seedRevisions(t, tx, execution)
	first := provisionPayload(revisionKey + ":provision:article-1")
	second := strings.ReplaceAll(first, "article-1", "article-2")
	second = strings.Replace(second, `"ordinal":0`, `"ordinal":1`, 1)
	second = strings.Replace(second, "c49069e1bb81752031", "c49069e1bb81752032", 1)
	both := first[:len(first)-1] + "," + second[1:]
	requireIdentityID(t, tx, "InsertProvisionRevisions", identityFence(execution, revision, both)...)
	requireIdentityID(t, tx, "InsertProvisionRevisions", identityFence(execution, revision, both)...)
	third := strings.ReplaceAll(second, "article-2", "article-3")
	third = strings.Replace(third, `"ordinal":1`, `"ordinal":2`, 1)
	changed := strings.Replace(first, identityDigest, strings.Repeat("b", 64), 1)
	conflict := changed[:len(changed)-1] + "," + third[1:]
	noIdentityRow(t, tx, "InsertProvisionRevisions", identityFence(execution, revision, conflict)...)
	duplicate := third[:len(third)-1] + "," + third[1:]
	noIdentityRow(t, tx, "InsertProvisionRevisions", identityFence(execution, revision, duplicate)...)
	if n := identityCount(t, tx, "SELECT count(*) FROM silver.provision_revision"); n != 2 {
		t.Fatal("bulk conflict partially inserted evidence")
	}
}

func TestIdentityMintRejectsExpiryDuringAdvisoryWait(t *testing.T) {
	f := identityFixture(t)
	seed := identityTx(t, f, false)
	execution := seedIdentityExecution(t, seed)
	identityExec(t, seed, `UPDATE ingest.execution_lease SET expires_at = clock_timestamp() + interval '2 seconds'`)
	if err := seed.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	holder := identityTx(t, f, false)
	// Match the mint function's natural-key lock. This forces a wait after the
	// outer fenced CTE has validated a live lease, without changing the lease.
	identityExec(t, holder, `SELECT pg_advisory_xact_lock(hashtextextended(jsonb_build_array(
		$1::text, 'source-a'::text, 'external-a'::text, '50/2024/TT-NHNN'::text,
		'thong-tu'::text, $2::text)::text, 0))`, observationKey, identityDigest)
	u := *f.u
	u.User = url.User("banhmi_corpus_writer_app")
	conn, err := pgx.Connect(f.ctx, u.String())
	if err != nil {
		t.Fatal("connect waiting identity app failed")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	writer, err := conn.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Rollback(context.Background()) })
	type outcome struct {
		rows int64
		err  error
	}
	done := make(chan outcome, 1)
	workCtx, cancel := context.WithCancel(f.ctx)
	query := identityQuery(t, "MintDocumentIdentity")
	go func() {
		tag, err := writer.Exec(workCtx, query, mintArgs(execution, identityKey)...)
		done <- outcome{rows: tag.RowsAffected(), err: err}
		close(done)
	}()
	// Cleanup order cancels the in-flight query before closing its connection.
	t.Cleanup(func() {
		cancel()
		_ = holder.Rollback(context.Background())
		<-done
	})
	for {
		var blocked, live bool
		if err := holder.QueryRow(f.ctx, `SELECT $1::integer = ANY(pg_blocking_pids($2::integer)),
			clock_timestamp() < expires_at FROM ingest.execution_lease`,
			int32(f.conn.PgConn().PID()), int32(conn.PgConn().PID())).Scan(&blocked, &live); err != nil {
			t.Fatal(err)
		}
		if !live {
			t.Fatal("lease expired before observing the mint blocked on its advisory lock")
		}
		if blocked {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("mint did not wait: %+v", result)
		case <-f.ctx.Done():
			t.Fatal(f.ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	identityExec(t, holder, `SELECT pg_sleep(GREATEST(0, extract(epoch FROM
		(expires_at - clock_timestamp()))) + 0.05) FROM ingest.execution_lease`)
	if err := holder.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil || result.rows != 0 {
			t.Fatalf("expired mint returned a row: rows=%d err=%v", result.rows, result.err)
		}
	case <-f.ctx.Done():
		t.Fatal(f.ctx.Err())
	}
	for _, table := range []string{"document_identity", "identity_assertion"} {
		if n := identityCount(t, writer, "SELECT count(*) FROM silver."+table); n != 0 {
			t.Fatalf("expired mint wrote %s", table)
		}
	}
}
