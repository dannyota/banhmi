// Command ingest runs a source crawler end to end against the live site and
// prints what it discovers. It is a developer harness for the Bronze layer, not
// part of the Temporal pipeline: it runs Discover, then FetchDetail on the
// first few documents, then attempts to Download their files into ./data.
//
// Usage:
//
//	go run ./cmd/ingest [-source congbao|sbv_hanoi] [-keyword TEXT] [-detail N] [-out ./data/source] [-since RFC3339]
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/ingest"
	"danny.vn/banhmi/pkg/ingest/congbao"
	"danny.vn/banhmi/pkg/ingest/sbvhanoi"
	dbbronze "danny.vn/banhmi/pkg/store/bronze"
)

func main() {
	var (
		sourceID    = flag.String("source", "congbao", "source id to run (currently: congbao, sbv_hanoi)")
		keyword     = flag.String("keyword", "", "source-specific discovery keyword/search term")
		detailCount = flag.Int("detail", 5, "number of discovered docs to FetchDetail + Download")
		outDir      = flag.String("out", "data/congbao", "directory to write downloaded files (gitignored)")
		sinceStr    = flag.String("since", "", "only discover docs published after this RFC3339 time (default: all)")
		timeout     = flag.Duration("timeout", 5*time.Minute, "overall run timeout")
		persist     = flag.Bool("persist", false, "upsert discovered docs into bronze.source_document")
		cfgPath     = flag.String("config", "config/config.yaml", "config file (used with -persist)")
	)
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))

	var src ingest.Source
	switch *sourceID {
	case congbao.SourceID:
		src = congbao.New(nil, log)
	case sbvhanoi.SourceID:
		src = sbvhanoi.New(nil, log)
	default:
		log.Error("unknown source", "source", *sourceID)
		os.Exit(2)
	}

	var since time.Time
	if *sinceStr != "" {
		t, err := time.Parse(time.RFC3339, *sinceStr)
		if err != nil {
			log.Error("parse -since", "err", err)
			os.Exit(2)
		}
		since = t
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ctx, cancelTimeout := context.WithTimeout(ctx, *timeout)
	defer cancelTimeout()

	var store *dbbronze.Queries
	if *persist {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			log.Error("load config", "err", err)
			os.Exit(1)
		}
		pool, err := db.NewPool(ctx, cfg.Database)
		if err != nil {
			log.Error("connect database", "err", err)
			os.Exit(1)
		}
		defer pool.Close()
		store = dbbronze.New(pool)
		log.Info("persisting to bronze", "db", cfg.Database.Redacted())
	}

	if err := run(ctx, log, src, since, *keyword, *detailCount, *outDir, store); err != nil {
		log.Error("ingest run", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, src ingest.Source, since time.Time, keyword string, detailCount int, outDir string, store *dbbronze.Queries) error {
	log.Info("discover starting", "source", src.ID(), "since", sinceLabel(since), "keyword", keyword)
	docs, err := src.Discover(ctx, since, keyword)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	log.Info("discover done", "count", len(docs))

	if store != nil {
		n, err := persistDocs(ctx, store, src.ID(), docs)
		if err != nil {
			return fmt.Errorf("persist: %w", err)
		}
		log.Info("persisted to bronze.source_document", "rows", n)
	}

	fmt.Println()
	fmt.Printf("=== Discovered %d documents (%s) ===\n", len(docs), src.ID())
	sbv := 0
	for i, d := range docs {
		marker := ""
		if isSBV(d) {
			marker = "  [SBV/NHNN]"
			sbv++
		}
		fmt.Printf("%2d. %-22s %-22s id=%-8s pub=%s%s\n",
			i+1, valueOr(d.Number, "?"), typeLabel(d.DocType), valueOr(d.ExternalID, "?"),
			dateLabel(d.PublishedAt), marker)
		fmt.Printf("    %s\n", d.DetailURL)
		if d.Title != "" {
			fmt.Printf("    trích yếu: %s\n", truncate(d.Title, 140))
		}
	}
	fmt.Printf("\nSBV/NHNN documents in feed: %d\n", sbv)

	if detailCount > len(docs) {
		detailCount = len(docs)
	}
	if detailCount <= 0 {
		return nil
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create out dir: %w", err)
	}

	fmt.Printf("\n=== FetchDetail + Download for first %d ===\n", detailCount)
	for i := 0; i < detailCount; i++ {
		d := docs[i]
		fmt.Printf("\n--- [%d/%d] %s ---\n", i+1, detailCount, d.DetailURL)

		detail, err := src.FetchDetail(ctx, ingest.DetailRef{
			ExternalID: d.ExternalID,
			DetailURL:  d.DetailURL,
		})
		if err != nil {
			log.Error("fetch detail", "url", d.DetailURL, "err", err)
			continue
		}
		printDetail(detail)
		if store != nil {
			enriched := *detail
			if enriched.ExternalID == "" {
				enriched.ExternalID = d.ExternalID
			}
			if _, err := store.UpsertSourceDocument(ctx, toUpsertParams(src.ID(), enriched)); err != nil {
				log.Error("persist detail", "id", enriched.ExternalID, "err", err)
			} else {
				log.Info("enriched bronze row", "number", enriched.Number)
			}
		}
		downloadFiles(ctx, log, src, detail, outDir)
	}
	return nil
}

func printDetail(d *ingest.DiscoveredDoc) {
	fmt.Printf("    số ký hiệu     : %s\n", valueOr(d.Number, "-"))
	fmt.Printf("    loại văn bản   : %s\n", valueOr(string(d.DocType), "-"))
	fmt.Printf("    cơ quan ban hành: %s\n", valueOr(d.Issuer, "-"))
	fmt.Printf("    trích yếu      : %s\n", valueOr(d.Title, "-"))
	fmt.Printf("    người ký       : %s\n", valueOr(d.Signer, "-"))
	fmt.Printf("    ngày ban hành  : %s\n", dateLabel(d.IssuedAt))
	fmt.Printf("    ngày hiệu lực  : %s\n", dateLabel(d.EffectiveAt))
	fmt.Printf("    số công báo    : %s\n", valueOr(d.GazetteNumber, "-"))
	if len(d.Files) == 0 {
		fmt.Printf("    files          : (none found)\n")
		return
	}
	fmt.Printf("    files          : %d\n", len(d.Files))
	for _, f := range d.Files {
		fmt.Printf("      - [%s] %s\n", strings.ToUpper(valueOr(f.Ext, "?")), f.Name)
		fmt.Printf("        %s\n", f.URL)
	}
}

func downloadFiles(ctx context.Context, log *slog.Logger, src ingest.Source, d *ingest.DiscoveredDoc, outDir string) {
	for _, f := range d.Files {
		dest := filepath.Join(outDir, safeName(d.ExternalID, f))
		file, err := os.Create(dest)
		if err != nil {
			log.Error("create file", "dest", dest, "err", err)
			continue
		}
		n, sum, err := src.Download(ctx, f, file)
		closeErr := file.Close()
		if err != nil {
			// Expected in restricted networks: the CDN host fails TLS. Report
			// the URL and move on; discovery + metadata are the deliverable.
			log.Warn("download failed (url still captured)", "name", f.Name, "url", f.URL, "err", err)
			_ = os.Remove(dest)
			continue
		}
		if closeErr != nil {
			log.Error("close file", "dest", dest, "err", closeErr)
			continue
		}
		log.Info("download ok", "name", f.Name, "bytes", n, "sha256", sum, "dest", dest)
		fmt.Printf("        downloaded %d bytes -> %s (sha256=%s)\n", n, dest, sum)
	}
}

// safeName builds a collision-resistant local file name from the doc id and the
// file's name/extension.
func safeName(id string, f ingest.FileRef) string {
	base := strings.ReplaceAll(f.Name, "/", "_")
	if base == "" {
		base = "file"
		if f.Ext != "" {
			base += "." + f.Ext
		}
	}
	if id == "" {
		return base
	}
	return id + "_" + base
}

func isSBV(d ingest.DiscoveredDoc) bool {
	hay := strings.ToUpper(d.Number + " " + d.Issuer + " " + d.DetailURL)
	return strings.Contains(hay, "NHNN") ||
		strings.Contains(strings.ToUpper(d.Issuer), "NGÂN HÀNG NHÀ NƯỚC")
}

func sinceLabel(t time.Time) string {
	if t.IsZero() {
		return "(all)"
	}
	return t.UTC().Format(time.RFC3339)
}

func dateLabel(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02")
}

func typeLabel(t ingest.DocType) string {
	if t == "" {
		return "?"
	}
	return string(t)
}

func valueOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// persistDocs upserts discovered documents into bronze.source_document,
// idempotent on (source, external_id) so re-running converges.
func persistDocs(ctx context.Context, q *dbbronze.Queries, source string, docs []ingest.DiscoveredDoc) (int, error) {
	n := 0
	for _, d := range docs {
		if strings.TrimSpace(d.ExternalID) == "" {
			continue
		}
		if _, err := q.UpsertSourceDocument(ctx, toUpsertParams(source, d)); err != nil {
			return n, fmt.Errorf("upsert %s/%s: %w", source, d.ExternalID, err)
		}
		n++
	}
	return n, nil
}

func toUpsertParams(source string, d ingest.DiscoveredDoc) dbbronze.UpsertSourceDocumentParams {
	return dbbronze.UpsertSourceDocumentParams{
		Source:       source,
		ExternalID:   d.ExternalID,
		DocNumber:    strPtr(d.Number),
		Title:        strPtr(d.Title),
		DocType:      strPtr(string(d.DocType)),
		Issuer:       strPtr(d.Issuer),
		IssuedAt:     timePtr(d.IssuedAt),
		EffectiveAt:  timePtr(d.EffectiveAt),
		StatusRaw:    strPtr(d.Status),
		DetailUrl:    strPtr(d.DetailURL),
		ContentHash:  strPtr(discoveryHash(d)),
		RawMeta:      discoveryMeta(d),
		DiscoveredAt: time.Now().UTC(),
		FetchedAt:    nil,
	}
}

func strPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

// discoveryHash fingerprints the discovery-time fields to detect changes on re-crawl.
func discoveryHash(d ingest.DiscoveredDoc) string {
	sum := sha256.Sum256([]byte(d.Number + "|" + d.Title + "|" + d.DetailURL + "|" + string(d.DocType)))
	return hex.EncodeToString(sum[:])
}

// discoveryMeta captures non-column fields (signer, gazette, file URLs) as JSONB.
func discoveryMeta(d ingest.DiscoveredDoc) []byte {
	m := map[string]any{}
	if d.Signer != "" {
		m["signer"] = d.Signer
	}
	if d.GazetteNumber != "" {
		m["gazette_number"] = d.GazetteNumber
	}
	if !d.PublishedAt.IsZero() {
		m["published_at"] = d.PublishedAt.UTC().Format(time.RFC3339)
	}
	if len(d.Files) > 0 {
		urls := make([]string, 0, len(d.Files))
		for _, f := range d.Files {
			urls = append(urls, f.URL)
		}
		m["file_urls"] = urls
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
}
