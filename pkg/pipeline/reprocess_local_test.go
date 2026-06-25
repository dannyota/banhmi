package pipeline

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/testsuite"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/extract"
	"danny.vn/banhmi/pkg/ingest"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbconfig "danny.vn/banhmi/pkg/store/config"
	dbgold "danny.vn/banhmi/pkg/store/gold"
	dbingest "danny.vn/banhmi/pkg/store/ingest"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// TestLocalReprocessFetchDoc re-runs the real Extract→Normalize→Index activities
// against a live database, without Temporal and without an embedder (Index writes
// chunks; embedding is best-effort and skipped when nil). It is a manual
// validation harness, skipped unless one of these env vars is set:
//
//   - BANHMI_REPROCESS_FETCH_DOC=<id> — re-process an existing fetch_doc
//     (Extract→Normalize→Index).
//
//   - BANHMI_REPROCESS_EXTID=<source:external_id> — look up an existing primary
//     doc's fetch_doc by natural key (no id guessing across DBs, provenance
//     untouched), then Extract→Normalize→Index. Preferred for primary docs.
//
//   - BANHMI_REPROCESS_SEED_EXTID=<source:external_id> — seed a relation-provenance
//     fetch_doc, then Extract→Normalize over the existing (already clean) bronze
//     content_html — no network. For relation_context targets that have no
//     fetch_doc; Index is skipped because such docs are served but not indexed.
//
//     BANHMI_REPROCESS_FETCH_DOC=223 \
//     BANHMI_DATABASE_DSN='postgres://banhmi:banhmi@localhost:10001/banhmi?sslmode=disable' \
//     BANHMI_MARKITDOWN_SCRIPT="$PWD/tools/markitdown_convert.py" \
//     go test -run TestLocalReprocessFetchDoc ./pkg/pipeline/ -v
func TestLocalReprocessFetchDoc(t *testing.T) {
	fetchEnv := os.Getenv("BANHMI_REPROCESS_FETCH_DOC")
	extIDEnv := os.Getenv("BANHMI_REPROCESS_EXTID")
	seedExtID := os.Getenv("BANHMI_REPROCESS_SEED_EXTID")
	if fetchEnv == "" && extIDEnv == "" && seedExtID == "" {
		t.Skip("set BANHMI_REPROCESS_FETCH_DOC, BANHMI_REPROCESS_EXTID, or BANHMI_REPROCESS_SEED_EXTID to run the local re-process harness")
	}
	dsn := os.Getenv("BANHMI_DATABASE_DSN")
	if dsn == "" {
		dsn = "postgres://banhmi:banhmi@localhost:10001/banhmi?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	ledger := dbingest.New(pool)
	md := extract.NewMarkItDownClient("python3", os.Getenv("BANHMI_MARKITDOWN_SCRIPT"))
	a := NewActivities(
		pool,
		ledger,
		dbbronze.New(pool),
		dbsilver.New(pool),
		dbgold.New(pool),
		dbconfig.New(pool),
		map[string]ingest.Source{},
		"", // storageDir: unused for the inline-HTML path
		md,
		nil, // embedder: skip embedding, write chunks only
		"",
		"vn",
	)

	var fetchDocID int64
	// Skip Index for docs whose chunks are already correct and only need their
	// served text refreshed (avoids needless gold.chunk churn + re-embedding).
	skipIndex := os.Getenv("BANHMI_REPROCESS_SKIP_INDEX") != ""
	switch {
	case extIDEnv != "":
		// Look up an existing primary doc's fetch_doc by natural key — robust across
		// databases (no id guessing) and leaves provenance untouched. Full pipeline.
		source, extID, ok := strings.Cut(extIDEnv, ":")
		if !ok || source == "" || extID == "" {
			t.Fatalf("BANHMI_REPROCESS_EXTID must be source:external_id, got %q", extIDEnv)
		}
		fd, gerr := ledger.GetFetchDoc(ctx, dbingest.GetFetchDocParams{Source: source, ExternalID: extID})
		if gerr != nil {
			t.Fatalf("get fetch_doc %s/%s: %v", source, extID, gerr)
		}
		fetchDocID = fd.ID
	case seedExtID != "":
		source, extID, ok := strings.Cut(seedExtID, ":")
		if !ok || source == "" || extID == "" {
			t.Fatalf("BANHMI_REPROCESS_SEED_EXTID must be source:external_id, got %q", seedExtID)
		}
		// Seed a relation-provenance fetch_doc (idempotent) so Extract re-runs over
		// the existing bronze content_html. The source_document + payload already
		// exist, so this re-extracts in place with no source API call.
		fd, err := ledger.UpsertFetchDoc(ctx, dbingest.UpsertFetchDocParams{
			Source:       source,
			ExternalID:   extID,
			InScope:      true,
			Provenance:   "relation",
			DiscoveredAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("seed fetch_doc %s/%s: %v", source, extID, err)
		}
		fetchDocID = fd.ID
		skipIndex = true // relation_context docs are served but not indexed
	default:
		id, perr := strconv.ParseInt(fetchEnv, 10, 64)
		if perr != nil {
			t.Fatalf("BANHMI_REPROCESS_FETCH_DOC: %v", perr)
		}
		fetchDocID = id
	}

	// Run each activity inside a Temporal test activity environment so
	// activity.GetLogger and friends get a real activity context — no server.
	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.RegisterActivity(a.Extract)
	env.RegisterActivity(a.Normalize)
	env.RegisterActivity(a.Index)
	p := StageParams{FetchDocID: fetchDocID}

	exVal, err := env.ExecuteActivity(a.Extract, p)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	var ex ExtractResult
	if err := exVal.Get(&ex); err != nil {
		t.Fatalf("Extract result: %v", err)
	}
	t.Logf("Extract: document_id=%d needs_review=%v confidence=%.3f", ex.DocumentID, ex.NeedsReview, ex.Confidence)

	nzVal, err := env.ExecuteActivity(a.Normalize, p)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	var nz NormalizeResult
	if err := nzVal.Get(&nz); err != nil {
		t.Fatalf("Normalize result: %v", err)
	}
	t.Logf("Normalize: %+v", nz)

	if skipIndex {
		return
	}
	ixVal, err := env.ExecuteActivity(a.Index, p)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	var ix IndexResult
	if err := ixVal.Get(&ix); err != nil {
		t.Fatalf("Index result: %v", err)
	}
	t.Logf("Index: %+v", ix)
}

// TestLocalEmbedAll drives the EmbedAll activity (Kaggle batch) against a live
// database, without Temporal. Force=false embeds only chunks missing a vector, so
// after a re-index it backfills exactly the new chunks. Skipped unless
// BANHMI_EMBED_ALL is set; needs KAGGLE_API_TOKEN and a reachable RDS DSN.
//
//	BANHMI_EMBED_ALL=1 \
//	BANHMI_DATABASE_DSN='postgres://banhmi:PW@HOST:5432/banhmi?sslmode=require' \
//	go test -run TestLocalEmbedAll ./pkg/pipeline/ -v -timeout 60m
func TestLocalEmbedAll(t *testing.T) {
	if os.Getenv("BANHMI_EMBED_ALL") == "" {
		t.Skip("set BANHMI_EMBED_ALL=1 to run the Kaggle embed harness")
	}
	dsn := os.Getenv("BANHMI_DATABASE_DSN")
	if dsn == "" {
		dsn = "postgres://banhmi:banhmi@localhost:10001/banhmi?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// config.yaml supplies the Kaggle params (dataset/accelerator/owner) and loads
	// KaggleToken from KAGGLE_API_TOKEN. The DB target comes from the DSN above.
	cfg, err := config.Load("../../config/config.yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.KaggleToken == "" {
		t.Fatal("KAGGLE_API_TOKEN is not set")
	}

	a := NewActivities(
		pool,
		dbingest.New(pool),
		dbbronze.New(pool),
		dbsilver.New(pool),
		dbgold.New(pool),
		dbconfig.New(pool),
		map[string]ingest.Source{},
		"",
		extract.NewMarkItDownClient("python3", os.Getenv("BANHMI_MARKITDOWN_SCRIPT")),
		nil,
		cfg.KaggleToken,
		"vn",
	)

	env := (&testsuite.WorkflowTestSuite{}).NewTestActivityEnvironment()
	env.SetTestTimeout(60 * time.Minute)
	env.RegisterActivity(a.EmbedAll)
	val, err := env.ExecuteActivity(a.EmbedAll, EmbedAllParams{
		Owner:        cfg.Embed.Kaggle.Owner,
		ModelDataset: cfg.Embed.Kaggle.ModelDataset,
		Accelerator:  cfg.Embed.Kaggle.Accelerator,
		Dims:         config.EmbedDims,
		Force:        false,
		Limit:        0,
	})
	if err != nil {
		t.Fatalf("EmbedAll: %v", err)
	}
	var res EmbedAllResult
	if err := val.Get(&res); err != nil {
		t.Fatalf("EmbedAll result: %v", err)
	}
	t.Logf("EmbedAll embedded=%d", res.Embedded)
}
