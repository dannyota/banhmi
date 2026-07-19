package pipeline

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/jurisdiction"
	"danny.vn/banhmi/pkg/ingest"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbconfig "danny.vn/banhmi/pkg/store/config"
	dbgold "danny.vn/banhmi/pkg/store/gold"
	dbingest "danny.vn/banhmi/pkg/store/ingest"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// TestLocalReprocessFetchDoc re-runs the real Extract→Normalize→Index activities
// against a live database, without an embedder (Index writes chunks; embedding is
// best-effort and skipped when nil). It is a manual validation harness, skipped
// unless one of these env vars is set:
//
//   - BANHMI_REPROCESS_FETCH_DOC=<id>
//
//   - BANHMI_REPROCESS_EXTID=<source:external_id>
//
//   - BANHMI_REPROCESS_SEED_EXTID=<source:external_id>
//
//     BANHMI_REPROCESS_FETCH_DOC=223 \
//     BANHMI_DATABASE_DSN='postgres://banhmi:banhmi@localhost:10001/banhmi?sslmode=disable' \
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
	a := NewActivities(
		slog.Default(),
		pool,
		ledger,
		dbbronze.New(pool),
		dbsilver.New(pool),
		dbgold.New(pool),
		dbconfig.New(pool),
		map[string]ingest.Source{},
		"",
		nil,
		nil,
		"",
		jurisdiction.For("vn"),
	)

	var fetchDocID int64
	skipIndex := os.Getenv("BANHMI_REPROCESS_SKIP_INDEX") != ""
	switch {
	case extIDEnv != "":
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
		skipIndex = true
	default:
		id, perr := strconv.ParseInt(fetchEnv, 10, 64)
		if perr != nil {
			t.Fatalf("BANHMI_REPROCESS_FETCH_DOC: %v", perr)
		}
		fetchDocID = id
	}

	p := StageParams{FetchDocID: fetchDocID}

	ex, err := a.Extract(ctx, p)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	t.Logf("Extract: document_id=%d needs_review=%v confidence=%.3f", ex.DocumentID, ex.NeedsReview, ex.Confidence)

	nz, err := a.Normalize(ctx, p)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	t.Logf("Normalize: %+v", nz)

	if skipIndex {
		return
	}
	ix, err := a.Index(ctx, p)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	t.Logf("Index: %+v", ix)
}

// TestLocalEmbedAll drives the EmbedAll activity (Kaggle batch) against a live
// database. Force=false embeds only chunks missing a vector. Skipped unless
// BANHMI_EMBED_ALL is set; needs KAGGLE_API_TOKEN and a reachable DSN.
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

	cfg, err := config.Load("../../config/config.yaml")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.KaggleToken == "" {
		t.Fatal("KAGGLE_API_TOKEN is not set")
	}

	a := NewActivities(
		slog.Default(),
		pool,
		dbingest.New(pool),
		dbbronze.New(pool),
		dbsilver.New(pool),
		dbgold.New(pool),
		dbconfig.New(pool),
		map[string]ingest.Source{},
		"",
		nil,
		nil,
		cfg.KaggleToken,
		jurisdiction.For("vn"),
	)

	res, err := a.EmbedAll(ctx, EmbedAllParams{
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
	t.Logf("EmbedAll embedded=%d", res.Embedded)
}
