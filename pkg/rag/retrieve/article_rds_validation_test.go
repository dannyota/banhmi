package retrieve

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxvec "github.com/pgvector/pgvector-go/pgx"

	"danny.vn/banhmi/pkg/base/config"
)

// TestAttachArticlesRDS validates full-Điều reassembly against the real corpus.
// Guarded: runs only when BANHMI_RDS_VALIDATE=1 and BANHMI_DATABASE_PASSWORD is set,
// so it never runs in normal `make test`. One-time validation harness for the
// provision (full-Điều) feature; connects via BANHMI_DATABASE_* env (RDS or local).
func TestAttachArticlesRDS(t *testing.T) {
	if os.Getenv("BANHMI_RDS_VALIDATE") != "1" || os.Getenv("BANHMI_DATABASE_PASSWORD") == "" {
		t.Skip("set BANHMI_RDS_VALIDATE=1 and BANHMI_DATABASE_PASSWORD to run the RDS validation")
	}
	pool := rdsValidationPool(t)
	r := New(pool, nil, config.RetrieveConfig{}, nil).(*hybridRetriever)
	ctx := context.Background()

	// CASE 1 — a normal multi-Khoản Điều that fits the cap: full attach + lead dedup.
	t.Run("normal long Điều: full attach, lead-in once", func(t *testing.T) {
		const find = `
SELECT document_id, split_part(citation, ',', 1) AS dieu, count(*) n
FROM gold.chunk
WHERE citation LIKE 'Điều %, %'
GROUP BY 1, 2
HAVING count(*) BETWEEN 3 AND 8 AND sum(length(content)) < 11000
ORDER BY n DESC, document_id
LIMIT 1`
		var doc int64
		var dieu string
		var n int
		if err := pool.QueryRow(ctx, find).Scan(&doc, &dieu, &n); err != nil {
			t.Skipf("no normal multi-Khoản Điều found: %v", err)
		}
		var citation, content string
		if err := pool.QueryRow(ctx,
			`SELECT citation, content FROM gold.chunk
			 WHERE document_id=$1 AND citation LIKE $2 || ', %' ORDER BY ordinal, id LIMIT 1`,
			doc, dieu).Scan(&citation, &content); err != nil {
			t.Fatalf("matched chunk: %v", err)
		}
		hits := []Hit{{DocumentID: doc, Citation: citation, Content: content}}
		r.attachArticles(ctx, hits)
		got := hits[0]
		t.Logf("doc=%d %q (%d chunks)", doc, dieu, n)
		if got.ArticleCitation != dieu || got.Article == "" || got.ArticleTruncated {
			t.Fatalf("want full attach for %q: citation=%q len=%d truncated=%v", dieu, got.ArticleCitation, len([]rune(got.Article)), got.ArticleTruncated)
		}
		if len([]rune(got.Article)) <= len([]rune(content)) {
			t.Errorf("Article (%d runes) not longer than matched snippet (%d runes)", len([]rune(got.Article)), len([]rune(content)))
		}
		firstLine, _, _ := strings.Cut(got.Article, "\n")
		if c := strings.Count(got.Article, firstLine); c != 1 {
			t.Errorf("Điều heading %q appears %d times, want 1 (lead-in dedup)", firstLine, c)
		}
		t.Logf("MATCHED %s (%d runes):\n%s", citation, len([]rune(content)), truncForLog(content, 280))
		t.Logf("FULL %s (%d runes):\n%s", got.ArticleCitation, len([]rune(got.Article)), truncForLog(got.Article, 1600))
	})

	// CASE 2 — the most-split Điều (amendment-law mega article): pointer, no text.
	t.Run("mega amendment Điều: pointer not truncated blob", func(t *testing.T) {
		const find = `
SELECT document_id, split_part(citation, ',', 1) AS dieu, count(*) n
FROM gold.chunk WHERE citation LIKE 'Điều %, %'
GROUP BY 1, 2 ORDER BY n DESC LIMIT 1`
		var doc int64
		var dieu string
		var n int
		if err := pool.QueryRow(ctx, find).Scan(&doc, &dieu, &n); err != nil {
			t.Fatalf("find mega Điều: %v", err)
		}
		var citation, content string
		if err := pool.QueryRow(ctx,
			`SELECT citation, content FROM gold.chunk
			 WHERE document_id=$1 AND citation LIKE $2 || ', %' ORDER BY ordinal, id LIMIT 1`,
			doc, dieu).Scan(&citation, &content); err != nil {
			t.Fatalf("matched chunk: %v", err)
		}
		hits := []Hit{{DocumentID: doc, Citation: citation, Content: content}}
		r.attachArticles(ctx, hits)
		got := hits[0]
		t.Logf("doc=%d %q across %d chunks", doc, dieu, n)
		if n <= 20 {
			t.Logf("largest Điều is only %d chunks; corpus has no mega article — skipping pointer assertion", n)
			return
		}
		if got.ArticleCitation != dieu || !got.ArticleTruncated || got.Article != "" {
			t.Errorf("mega Điều %q: want pointer (citation set, truncated, no text); got citation=%q truncated=%v textlen=%d",
				dieu, got.ArticleCitation, got.ArticleTruncated, len([]rune(got.Article)))
		} else {
			t.Logf("mega Điều %q (%d chunks) → pointer (truncated, no inline text) ✓", dieu, n)
		}
	})

	// CASE 3 — a short single-chunk Điều: Article equals the chunk verbatim.
	t.Run("short Điều: verbatim single chunk", func(t *testing.T) {
		var doc int64
		var citation, content string
		err := pool.QueryRow(ctx,
			`SELECT document_id, citation, content FROM gold.chunk
			 WHERE citation ~ '^Điều [0-9]+$'
			   AND document_id NOT IN (SELECT document_id FROM gold.chunk WHERE citation LIKE 'Điều %, %')
			 ORDER BY length(content) DESC LIMIT 1`).Scan(&doc, &citation, &content)
		if err != nil {
			t.Skipf("no clean short Điều found: %v", err)
		}
		hits := []Hit{{DocumentID: doc, Citation: citation, Content: content}}
		r.attachArticles(ctx, hits)
		if strings.TrimSpace(hits[0].Article) != strings.TrimSpace(content) {
			t.Errorf("short Điều %q: Article should equal the single chunk verbatim", citation)
		}
		t.Logf("SHORT %s: Article == chunk (%d runes) ✓", citation, len([]rune(content)))
	})
}

func truncForLog(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + " …[truncated]"
}

func rdsValidationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	dsn := fmt.Sprintf("connect_timeout=10 host=%s port=%s user=%s dbname=%s sslmode=%s password=%s",
		get("BANHMI_DATABASE_HOST", "localhost"),
		get("BANHMI_DATABASE_PORT", "5432"),
		get("BANHMI_DATABASE_USER", "banhmi"),
		get("BANHMI_DATABASE_NAME", "banhmi"),
		get("BANHMI_DATABASE_SSLMODE", "require"),
		os.Getenv("BANHMI_DATABASE_PASSWORD"),
	)
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	poolCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return pgxvec.RegisterTypes(ctx, conn)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
