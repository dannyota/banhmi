// Command dictgen generates the diacritic-restore dictionary for a jurisdiction's
// corpus. It scans gold.chunk content, tokenizes BEFORE folding to collect the
// diacritized forms of each syllable, then folds each token to find the mapping
// (folded → most frequent diacritized form). Only unambiguous tokens (one form
// covers ≥90% of occurrences) are emitted; ambiguous syllables are excluded.
//
// Output: a CSV file (default deploy/seed/diacritic_restore_<jur>.csv) suitable
// for loading via cmd/seed. The CSV has columns: jurisdiction, folded_token,
// restored_token, share.
//
// Usage:
//
//	go run ./cmd/dictgen                                # VN, default output
//	go run ./cmd/dictgen -jurisdiction my -out dict.csv  # MY, custom path
//	go run ./cmd/dictgen -min-freq 10 -min-share 0.90   # tune thresholds
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
	"danny.vn/banhmi/pkg/base/jurisdiction"
	blog "danny.vn/banhmi/pkg/base/log"
	"danny.vn/banhmi/pkg/rag/lexical"

	"golang.org/x/text/unicode/norm"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	jur := flag.String("jurisdiction", "", "jurisdiction code (default: BANHMI_JURISDICTION or vn)")
	out := flag.String("out", "", "output CSV path (default: deploy/seed/diacritic_restore_<jur>.csv)")
	minFreq := flag.Int("min-freq", 10, "minimum total occurrences of the folded token")
	minShare := flag.Float64("min-share", 0.90, "minimum share of the dominant form (0.0-1.0)")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(*cfgPath, *jur, *out, *minFreq, *minShare, log); err != nil {
		log.Error("dictgen", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath, jurCode, outPath string, minFreq int, minShare float64, log *slog.Logger) error {
	ctx := context.Background()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if jurCode == "" {
		jurCode = cfg.Jurisdiction
	}
	desc, ok := jurisdiction.Lookup(jurCode)
	if !ok {
		return fmt.Errorf("unknown jurisdiction %q", jurCode)
	}
	normalizer := lexical.NormalizerFor(desc.TextNormalizer)

	if outPath == "" {
		outPath = fmt.Sprintf("deploy/seed/diacritic_restore_%s.csv", desc.Code)
	}

	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	// Load all chunk content from the corpus.
	log.Info("dictgen: loading corpus", "jurisdiction", desc.Code)
	rows, err := pool.Query(ctx, `SELECT content FROM gold.chunk ORDER BY id`)
	if err != nil {
		return fmt.Errorf("load chunks: %w", err)
	}
	defer rows.Close()

	// For each token in the corpus (BEFORE folding), collect: original form → count.
	// Then fold to get the mapping: folded → {original → count}.
	type formCount struct {
		form  string
		count int
	}

	// foldedStats: folded_token → map[original_form]count
	foldedStats := make(map[string]map[string]int)
	totalTokens := 0
	chunkCount := 0

	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return fmt.Errorf("scan chunk: %w", err)
		}
		chunkCount++

		// Split into tokens BEFORE folding: we need the original diacritized forms.
		// Use the same splitting logic (lower-case, split on non-letter/digit) but
		// without the NFD diacritic strip.
		origTokens := splitPreserving(content)
		for _, tok := range origTokens {
			totalTokens++
			// Fold the token using the normalizer to get the folded form.
			folded := strings.TrimSpace(lexical.TokenizeRaw(tok, normalizer))
			if folded == "" {
				continue
			}
			if foldedStats[folded] == nil {
				foldedStats[folded] = make(map[string]int)
			}
			foldedStats[folded][tok]++
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate chunks: %w", err)
	}
	log.Info("dictgen: corpus scanned", "chunks", chunkCount, "tokens", totalTokens, "unique_folded", len(foldedStats))

	// Build the dictionary: for each folded token, find the most frequent original.
	// Only include if: total freq >= minFreq AND dominant share >= minShare AND
	// the restored form differs from the folded form (otherwise it's already ASCII).
	type entry struct {
		folded   string
		restored string
		share    float64
	}
	var entries []entry
	excluded := 0
	identicalSkipped := 0

	for folded, forms := range foldedStats {
		total := 0
		for _, c := range forms {
			total += c
		}
		if total < minFreq {
			continue
		}

		// Find the dominant form.
		var best formCount
		for form, c := range forms {
			if c > best.count {
				best = formCount{form: form, count: c}
			}
		}

		share := float64(best.count) / float64(total)
		if share < minShare {
			excluded++
			continue
		}

		// Skip if the restored form is the same as the folded form (no diacritics).
		if best.form == folded {
			identicalSkipped++
			continue
		}

		entries = append(entries, entry{folded: folded, restored: best.form, share: share})
	}

	// Sort by folded token for deterministic output.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].folded < entries[j].folded
	})

	log.Info("dictgen: dictionary built",
		"entries", len(entries),
		"excluded_ambiguous", excluded,
		"identical_skipped", identicalSkipped,
		"min_freq", minFreq,
		"min_share", minShare,
	)

	// Show some examples.
	examples := 5
	if len(entries) < examples {
		examples = len(entries)
	}
	for i := 0; i < examples; i++ {
		log.Info("dictgen: example", "folded", entries[i].folded, "restored", entries[i].restored, "share", fmt.Sprintf("%.1f%%", entries[i].share*100))
	}

	// Write CSV.
	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer func() { _ = f.Close() }()

	w := csv.NewWriter(f)
	if err := w.Write([]string{"jurisdiction", "folded_token", "restored_token", "share"}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	for _, e := range entries {
		if err := w.Write([]string{desc.Code, e.folded, e.restored, strconv.FormatFloat(e.share, 'f', 4, 64)}); err != nil {
			return fmt.Errorf("write row: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}

	log.Info("dictgen: wrote CSV", "path", outPath, "entries", len(entries))
	return nil
}

// splitPreserving splits text into tokens preserving diacritics. It lower-cases
// and splits on non-letter/digit boundaries, but does NOT strip diacritics.
// This is the "before folding" tokenizer that captures the original diacritized
// forms of Vietnamese syllables.
func splitPreserving(s string) []string {
	// NFC-normalize to canonical composed form so the same syllable always
	// produces the same string regardless of source encoding.
	s = norm.NFC.String(strings.ToLower(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isLetterOrDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

func isLetterOrDigit(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
