package retrieve

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgvector "github.com/pgvector/pgvector-go"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/scope"
)

// fakeEmbedder returns a fixed unit vector for any input, so the vector arm runs
// deterministically without a live embedding endpoint. The model name is
// test-specific so the seeded chunk_embedding rows never collide with real ones.
type fakeEmbedder struct {
	model string
	dims  int
	vec   []float32
}

func (f *fakeEmbedder) Model() string { return f.model }
func (f *fakeEmbedder) Dims() int     { return f.dims }
func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}

// testPool connects to the configured database, skipping the test cleanly when the
// DB is unreachable or the password env var is absent. pgvector types are
// registered per connection so vector(1024) round-trips.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("BANHMI_DATABASE_PASSWORD") == "" {
		t.Skip("BANHMI_DATABASE_PASSWORD not set; skipping retrieve DB integration test")
	}

	cfg := config.Default()
	cfg.Database.Host = "localhost"
	cfg.Database.Port = 10001
	cfg.Database.Password = os.Getenv("BANHMI_DATABASE_PASSWORD")

	poolCfg, err := pgxpool.ParseConfig(cfg.Database.DSN())
	if err != nil {
		t.Fatalf("parse pool config: %v", err)
	}
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Skipf("cannot create pool (DB unavailable?): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("cannot ping DB, skipping integration test: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedDoc inserts a silver.document and a single validity_period row with the given
// status_class, returning the new document id. Rows are removed in t.Cleanup.
func seedDoc(t *testing.T, pool *pgxpool.Pool, docKey, docNumber, title, statusClass string) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	var docID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO silver.document (doc_key, doc_number, title, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
		RETURNING id`,
		docKey, docNumber, title, now,
	).Scan(&docID)
	if err != nil {
		t.Fatalf("seed document %q: %v", docKey, err)
	}
	t.Cleanup(func() {
		// document_section/validity/chunk all cascade or are cleaned below; the
		// document delete cascades validity_period (FK ON DELETE CASCADE).
		_, _ = pool.Exec(context.Background(), `DELETE FROM silver.document WHERE id = $1`, docID)
	})

	_, err = pool.Exec(ctx, `
		INSERT INTO silver.validity_period
			(document_id, status_code, status_class, eff_from, observed_at)
		VALUES ($1, $2, $3, $4, $4)`,
		docID, statusCode(statusClass), statusClass, now,
	)
	if err != nil {
		t.Fatalf("seed validity for %q: %v", docKey, err)
	}
	return docID
}

func statusCode(class string) string {
	switch class {
	case "in_force":
		return "CHL"
	case "partial":
		return "HHL1P"
	case "expired":
		return "HHL"
	default:
		return "UNK"
	}
}

// seedChunk inserts a gold.chunk and (for a non-empty vec) a gold.chunk_embedding
// under the test model. It returns the chunk id; gold rows cascade-delete with the
// owning document, but we also delete explicitly for clarity.
func seedChunk(t *testing.T, pool *pgxpool.Pool, docID int64, citation, content string, ordinal int, model string, vec []float32) int64 {
	t.Helper()
	ctx := context.Background()

	var chunkID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO gold.chunk (document_id, citation, content, ordinal)
		VALUES ($1, $2, $3, $4)
		RETURNING id`,
		docID, citation, content, ordinal,
	).Scan(&chunkID)
	if err != nil {
		t.Fatalf("seed chunk (doc %d): %v", docID, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM gold.chunk WHERE id = $1`, chunkID)
	})

	if len(vec) > 0 {
		_, err = pool.Exec(ctx, `
			INSERT INTO gold.chunk_embedding (chunk_id, model, dims, embedding)
			VALUES ($1, $2, $3, $4)`,
			chunkID, model, len(vec), pgvector.NewVector(vec),
		)
		if err != nil {
			t.Fatalf("seed embedding (chunk %d): %v", chunkID, err)
		}
	}
	return chunkID
}

func seedRelation(t *testing.T, pool *pgxpool.Pool, fromDocID, toDocID int64, refKey, label, relationType string) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	var refID int64
	err := pool.QueryRow(ctx, `
		INSERT INTO silver.doc_ref (ref_key, document_id, label, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $4)
		RETURNING id`,
		refKey, toDocID, label, now,
	).Scan(&refID)
	if err != nil {
		t.Fatalf("seed doc_ref %q: %v", refKey, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM silver.doc_ref WHERE id = $1`, refID)
	})

	var relationID int64
	rawType := 25
	err = pool.QueryRow(ctx, `
		INSERT INTO silver.document_relation
			(from_document_id, to_ref_id, relation_type, relation_type_raw, source)
		VALUES ($1, $2, $3, $4, 'test')
		RETURNING id`,
		fromDocID, refID, relationType, rawType,
	).Scan(&relationID)
	if err != nil {
		t.Fatalf("seed relation %q: %v", relationType, err)
	}
	return relationID
}

func seedDocumentText(t *testing.T, pool *pgxpool.Pool, docID int64, markdown string, binding, needsReview bool) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	_, err := pool.Exec(ctx, `
		INSERT INTO silver.document_text
			(document_id, authority, source, markdown, is_binding, needs_review, created_at, updated_at)
		VALUES ($1, 'ocr_extractive', 'test', $2, $3, $4, $5, $5)
		ON CONFLICT (document_id, authority, source) DO UPDATE SET
			markdown = EXCLUDED.markdown,
			is_binding = EXCLUDED.is_binding,
			needs_review = EXCLUDED.needs_review,
			updated_at = EXCLUDED.updated_at`,
		docID, markdown, binding, needsReview, now,
	)
	if err != nil {
		t.Fatalf("seed document_text (doc %d): %v", docID, err)
	}
}

// unitVec1024 builds a 1024-dim vector with a 1.0 in slot `hot` (rest 0), so two
// such vectors are cosine-identical iff they share the hot slot and orthogonal
// otherwise — a clean knob for "near" vs "far" in the vector arm.
func unitVec1024(hot int) []float32 {
	v := make([]float32, 1024)
	v[hot] = 1
	return v
}

func TestSearch_currentLawFilterWithVectorArm(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	const model = "test/retrieve-it"
	queryVec := unitVec1024(7)

	// In-force document with a chunk that matches both arms: its embedding shares
	// the query's hot slot (cosine 0 distance) and its content carries the BM25 term.
	liveDoc := seedDoc(t, pool, "it-test-live", "01/2026/TT-NHNN",
		"Quy định an toàn hệ thống thông tin", "in_force")
	liveChunk := seedChunk(t, pool, liveDoc, "Điều 1",
		"tổ chức tín dụng phải bảo đảm an toàn hệ thống thông tin ngân hàng",
		1, model, unitVec1024(7))

	// Repealed (expired) document with a chunk that ALSO matches both arms. The
	// in-force pre-filter must exclude it entirely.
	deadDoc := seedDoc(t, pool, "it-test-dead", "02/2010/TT-NHNN",
		"Văn bản đã hết hiệu lực về thông tin", "expired")
	deadChunk := seedChunk(t, pool, deadDoc, "Điều 1",
		"quy định cũ về an toàn hệ thống thông tin đã hết hiệu lực",
		1, model, unitVec1024(7))

	emb := &fakeEmbedder{model: model, dims: 1024, vec: queryVec}
	r := New(pool, emb, config.Default().Retrieve, nil)

	// --- default: current law leads; the repealed chunk is SURFACED after it,
	// badged expired (evidence-only contract — not excluded). ---
	hits, err := r.Search(ctx, "an toàn hệ thống thông tin", SearchOpts{})
	if err != nil {
		t.Fatalf("Search (default): %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("Search returned no hits; expected the in-force chunk")
	}
	liveRank, deadRank := -1, -1
	for i, h := range hits {
		switch h.ChunkID {
		case liveChunk:
			liveRank = i
		case deadChunk:
			deadRank = i
			if h.Validity.StatusClass != "expired" {
				t.Errorf("surfaced repealed chunk badged %q, want expired", h.Validity.StatusClass)
			}
		}
	}
	if liveRank == -1 {
		t.Fatal("default search missing the in-force chunk")
	}
	if deadRank == -1 {
		t.Errorf("default search should surface repealed chunk %d (badged), not exclude it", deadChunk)
	} else if deadRank < liveRank {
		t.Errorf("repealed chunk ranked %d before current %d; current law must lead", deadRank, liveRank)
	}

	// --- InForceOnly=true (strict): the repealed chunk must NOT appear ---
	strict := true
	strictHits, err := r.Search(ctx, "an toàn hệ thống thông tin", SearchOpts{InForceOnly: &strict})
	if err != nil {
		t.Fatalf("Search (strict): %v", err)
	}
	for _, h := range strictHits {
		if h.ChunkID == deadChunk {
			t.Errorf("strict in-force filter leaked repealed chunk %d (doc %d)", deadChunk, deadDoc)
		}
	}
	// The live chunk must be present via the vector arm. BM25 over freshly
	// inserted synthetic rows is not reliable on every ParadeDB build, so BM25 is
	// covered separately by retrieval-only eval over the real corpus.
	var live *Hit
	for i := range hits {
		if hits[i].ChunkID == liveChunk {
			live = &hits[i]
			break
		}
	}
	if live == nil {
		t.Fatalf("live chunk %d not in results", liveChunk)
	}
	if live.VectorRank == 0 {
		t.Errorf("live chunk vectorRank = 0; vector arm did not return it")
	}
	if live.DocNumber != "01/2026/TT-NHNN" {
		t.Errorf("live DocNumber = %q, want 01/2026/TT-NHNN (citation metadata not hydrated)", live.DocNumber)
	}
	if live.Title == "" || live.Citation == "" {
		t.Errorf("live hit missing citation metadata: title=%q citation=%q", live.Title, live.Citation)
	}
	if live.Score <= 0 {
		t.Errorf("live hit score = %v, want > 0", live.Score)
	}

	vectorHits, err := r.Search(ctx, "an toàn hệ thống thông tin", SearchOpts{Mode: ModeVector})
	if err != nil {
		t.Fatalf("Search (vector-only): %v", err)
	}
	var vectorLive *Hit
	for i := range vectorHits {
		if vectorHits[i].ChunkID == liveChunk {
			vectorLive = &vectorHits[i]
			break
		}
	}
	if vectorLive == nil {
		t.Fatalf("vector-only search did not return live chunk %d", liveChunk)
	}
	if vectorLive.VectorRank == 0 {
		t.Errorf("vector-only VectorRank = 0, want >= 1")
	}
	if vectorLive.BM25Rank != 0 {
		t.Errorf("vector-only BM25Rank = %d, want 0", vectorLive.BM25Rank)
	}

	// --- in_force_only = false: the repealed chunk becomes visible (proves the
	// filter, not a seeding accident, was hiding it) ---
	off := false
	allHits, err := r.Search(ctx, "an toàn hệ thống thông tin", SearchOpts{InForceOnly: &off})
	if err != nil {
		t.Fatalf("Search (in-force off): %v", err)
	}
	foundDead := false
	for _, h := range allHits {
		if h.ChunkID == deadChunk {
			foundDead = true
			break
		}
	}
	if !foundDead {
		t.Errorf("with in_force_only=false the repealed chunk %d should appear", deadChunk)
	}
}

func TestSearch_partialValidityIsCurrent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	doc := seedDoc(t, pool, "it-test-partial", "04/2026/TT-NHNN",
		"Văn bản còn hiệu lực một phần", "partial")
	chunk := seedChunk(t, pool, doc, "Điều 9",
		"quy định còn hiệu lực một phần về an toàn hệ thống thông tin",
		9, "", nil)

	r := New(pool, nil, config.Default().Retrieve, nil)
	hits, err := r.Search(ctx, "còn hiệu lực một phần an toàn hệ thống thông tin", SearchOpts{})
	if err != nil {
		t.Fatalf("Search (partial validity): %v", err)
	}

	for _, h := range hits {
		if h.ChunkID == chunk {
			return
		}
	}
	t.Fatalf("partial-validity chunk %d not returned", chunk)
}

func TestSearch_bm25OnlyWhenNoEmbedder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	doc := seedDoc(t, pool, "it-test-bm25only", "03/2026/TT-NHNN",
		"Quy định về thanh toán điện tử", "in_force")
	chunk := seedChunk(t, pool, doc, "Điều 5",
		"ngân hàng nhà nước quy định về dịch vụ thanh toán điện tử qua internet",
		5, "", nil) // no embedding

	// nil embedder → BM25-only path.
	r := New(pool, nil, config.Default().Retrieve, nil)
	hits, err := r.Search(ctx, "thanh toán điện tử", SearchOpts{})
	if err != nil {
		t.Fatalf("Search (BM25-only): %v", err)
	}

	var found *Hit
	for i := range hits {
		if hits[i].ChunkID == chunk {
			found = &hits[i]
			break
		}
	}
	if found == nil {
		t.Skipf("BM25 did not surface fresh synthetic chunk %d; pg_search is validated on the real corpus eval", chunk)
	}
	if found.BM25Rank == 0 {
		t.Errorf("BM25Rank = 0, want >= 1")
	}
	if found.VectorRank != 0 {
		t.Errorf("VectorRank = %d, want 0 (no embedder)", found.VectorRank)
	}
}

func TestHydrateAttachesConfirmedRelations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	suffix := time.Now().UnixNano()
	actingDoc := seedDoc(t, pool, fmt.Sprintf("it-test-rel-acting-%d", suffix), "25/2025/TT-NHNN",
		"Thông tư sửa đổi", "in_force")
	targetDoc := seedDoc(t, pool, fmt.Sprintf("it-test-rel-target-%d", suffix), "17/2024/TT-NHNN",
		"Thông tư bị sửa đổi", "in_force")
	actingChunk := seedChunk(t, pool, actingDoc, "Điều 1",
		"Thông tư này sửa đổi quy định về mở tài khoản thanh toán.",
		1, "", nil)
	targetChunk := seedChunk(t, pool, targetDoc, "Điều 16",
		"Quy định về mở tài khoản thanh toán bằng phương tiện điện tử.",
		1, "", nil)
	relationID := seedRelation(t, pool, actingDoc, targetDoc,
		fmt.Sprintf("it-test-ref-%d", suffix), "17/2024/TT-NHNN", "amends_supplements")

	r := &hybridRetriever{pool: pool, log: slog.New(slog.DiscardHandler)}
	hits, err := r.hydrate(ctx, []fusedHit{
		{chunkID: actingChunk, score: 1, bm25Rank: 1},
		{chunkID: targetChunk, score: 0.5, bm25Rank: 2},
	})
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}

	var acting, target *Hit
	for i := range hits {
		switch hits[i].DocumentID {
		case actingDoc:
			acting = &hits[i]
		case targetDoc:
			target = &hits[i]
		}
	}
	if acting == nil || target == nil {
		t.Fatalf("missing acting/target hits: %+v", hits)
	}
	if len(acting.Relations) != 1 {
		t.Fatalf("acting relations = %+v, want one outgoing relation", acting.Relations)
	}
	out := acting.Relations[0]
	if out.RelationID != relationID || out.Direction != "outgoing" ||
		out.DocNumber != "17/2024/TT-NHNN" || !out.Resolved {
		t.Fatalf("outgoing relation = %+v, want resolved target relation", out)
	}
	if out.RelationTypeRaw == nil || *out.RelationTypeRaw != 25 {
		t.Fatalf("outgoing relation raw type = %v, want 25", out.RelationTypeRaw)
	}

	if len(target.Relations) != 1 {
		t.Fatalf("target relations = %+v, want one incoming relation", target.Relations)
	}
	in := target.Relations[0]
	if in.RelationID != relationID || in.Direction != "incoming" ||
		in.DocNumber != "25/2025/TT-NHNN" || !in.Resolved {
		t.Fatalf("incoming relation = %+v, want acting document relation", in)
	}
}

func TestSearchEvidenceSurfacesKnownBindingTextGapAsContext(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	suffix := time.Now().UnixNano()
	indexedDoc := seedDoc(t, pool, fmt.Sprintf("it-test-gap-indexed-%d", suffix), fmt.Sprintf("%d/TT-ABC", suffix),
		"Indexed synthetic alpha scope rule", "in_force")
	seedDocumentText(t, pool, indexedDoc, "Binding text for alpha scope.", true, false)
	seedChunk(t, pool, indexedDoc, "Article 1", "alpha scope indexed binding evidence", 1, "test/gap", unitVec1024(1))

	gapDoc := seedDoc(t, pool, fmt.Sprintf("it-test-gap-missing-%d", suffix), fmt.Sprintf("%d/QD-ABC", suffix),
		"Unindexed synthetic alpha scope rule", "in_force")
	seedDocumentText(t, pool, gapDoc, "Non-binding text for alpha scope that still belongs in evidence gaps.", false, true)

	r := New(pool, &fakeEmbedder{model: "test/gap", dims: 1024, vec: unitVec1024(1)},
		config.Default().Retrieve, nil, WithGateConfig(GateConfig{
			ScopeTerms: []scope.Term{{Text: "alpha scope", Class: scope.ClassStrong}},
		}))
	ev, err := r.SearchEvidence(ctx, "alpha scope", SearchOpts{Mode: ModeVector})
	if err != nil {
		t.Fatalf("SearchEvidence: %v", err)
	}
	if ev.Abstain {
		t.Fatalf("Evidence Abstain = true, want false; gaps=%+v", ev.Gaps)
	}

	for _, gap := range ev.Gaps {
		if gap.Kind == GapKnownBindingTextGap && gap.DocumentID == gapDoc {
			if gap.BlocksAnswer {
				t.Fatalf("binding text gap blocks answer; want context warning: %+v", gap)
			}
			return
		}
	}
	t.Fatalf("gaps = %+v, want known binding text gap for doc %d", ev.Gaps, gapDoc)
}

func TestRelatedHitsIntegrationUsesConfirmedRelationTargets(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	const q = `
SELECT
  dr.from_document_id,
  COALESCE(fd.doc_number, ''),
  dr.id,
  dr.relation_type,
  COALESCE(dr.source, ''),
  COALESCE(dr.to_citation, ''),
  td.id,
  COALESCE(td.doc_number, ''),
  COALESCE(td.title, ''),
  c.content
FROM silver.document_relation dr
JOIN silver.document fd ON fd.id=dr.from_document_id
JOIN silver.doc_ref ref ON ref.id=dr.to_ref_id
JOIN silver.document td ON td.id=ref.document_id
JOIN gold.chunk c ON c.document_id=td.id
WHERE EXISTS (
  SELECT 1
  FROM silver.document_text dt
  WHERE dt.document_id=td.id
    AND dt.is_binding
    AND NULLIF(btrim(COALESCE(dt.markdown, '')), '') IS NOT NULL
)
ORDER BY dr.id, c.ordinal
LIMIT 1`
	var (
		baseDocID     int64
		baseDocNumber string
		relationID    int64
		relationType  string
		source        string
		toCitation    string
		targetDocID   int64
		targetDocNum  string
		targetTitle   string
		targetContent string
	)
	err := pool.QueryRow(ctx, q).Scan(
		&baseDocID,
		&baseDocNumber,
		&relationID,
		&relationType,
		&source,
		&toCitation,
		&targetDocID,
		&targetDocNum,
		&targetTitle,
		&targetContent,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			t.Skip("no confirmed indexed relation targets in local corpus")
		}
		t.Fatalf("select relation target: %v", err)
	}

	r := New(pool, nil, config.Default().Retrieve, nil).(*hybridRetriever)
	query := firstWords(targetContent, 16)
	if query == "" {
		t.Skip("selected relation target chunk has no queryable text")
	}
	hits := []Hit{{
		ChunkID:    1,
		DocumentID: baseDocID,
		DocNumber:  baseDocNumber,
		Relations: []Relation{{
			RelationID:           relationID,
			Direction:            "outgoing",
			RelationType:         relationType,
			Source:               source,
			ToCitation:           toCitation,
			DocumentID:           targetDocID,
			DocNumber:            targetDocNum,
			Title:                targetTitle,
			Resolved:             true,
			TargetIndexed:        true,
			TargetHasBindingText: true,
		}},
	}}
	related, err := r.relatedHits(ctx, query, hits, 3)
	if err != nil {
		t.Fatalf("relatedHits: %v", err)
	}
	if len(related) == 0 {
		t.Fatalf("relatedHits returned none for confirmed target doc %d query %q", targetDocID, query)
	}
	if related[0].RelationID != relationID || related[0].DocumentID != targetDocID {
		t.Fatalf("related hit = %+v, want relation %d target doc %d", related[0], relationID, targetDocID)
	}
	if related[0].Rank == 0 {
		t.Fatalf("related rank diagnostics missing: %+v", related[0])
	}
}

func firstWords(s string, n int) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}
