// Command seed loads banhmi's default config — scope terms, issuer codes, and
// discovery keywords — from the embedded deploy/seed/*.csv into the config schema.
//
// It is re-runnable: each table's origin='seed' rows are deleted and reinserted
// from the CSV, while operator customizations (origin='user' rows) are preserved
// (the inserts skip rows that collide with a user override). Edit a CSV and
// re-run to refresh the shipped defaults.
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	seed "danny.vn/banhmi/deploy/seed"
	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
	"danny.vn/banhmi/pkg/base/jurisdiction"
	blog "danny.vn/banhmi/pkg/base/log"
	dbconfig "danny.vn/banhmi/pkg/store/config"
)

func main() {
	cfgPath := flag.String("config", "config/config.yaml", "path to config file")
	flag.Parse()

	log := blog.New(os.Getenv("BANHMI_LOG_LEVEL"))
	if err := run(*cfgPath, log); err != nil {
		log.Error("seed", "err", err)
		os.Exit(1)
	}
}

func run(cfgPath string, log *slog.Logger) error {
	ctx := context.Background()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	log.Info("banhmi seed", "db", cfg.Database.Redacted())

	pool, err := db.NewPool(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()

	// One transaction so a partial CSV never leaves config half-seeded.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := dbconfig.New(tx)

	counts := map[string]int{}

	if err := q.DeleteSeedScopeTerms(ctx); err != nil {
		return fmt.Errorf("clear scope_term seed: %w", err)
	}
	var scopeTotal int
	for _, jur := range jurisdiction.All() {
		rows, err := readSeedCSV(jur.ScopeSeedFile)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := q.InsertSeedScopeTerm(ctx, dbconfig.InsertSeedScopeTermParams{
				Jurisdiction: jur.Code, Term: r[0], TermClass: r[1], Theme: r[2],
			}); err != nil {
				return fmt.Errorf("insert scope_term %q (%s): %w", r[0], jur.Code, err)
			}
		}
		scopeTotal += len(rows)
	}
	counts["scope_term"] = scopeTotal

	if err := q.DeleteSeedIssuerCodes(ctx); err != nil {
		return fmt.Errorf("clear issuer_code seed: %w", err)
	}
	rows, err := readSeedCSV("issuer_code.csv")
	if err != nil {
		return err
	}
	for _, r := range rows {
		inScope, err := strconv.ParseBool(r[3])
		if err != nil {
			return fmt.Errorf("issuer_code %q/%q in_scope: %w", r[0], r[1], err)
		}
		isSBV, err := strconv.ParseBool(r[4])
		if err != nil {
			return fmt.Errorf("issuer_code %q/%q is_sbv: %w", r[0], r[1], err)
		}
		if err := q.InsertSeedIssuerCode(ctx, dbconfig.InsertSeedIssuerCodeParams{
			Source: r[0], Code: r[1], Name: r[2], InScope: inScope, IsSbv: isSBV,
		}); err != nil {
			return fmt.Errorf("insert issuer_code %q/%q: %w", r[0], r[1], err)
		}
	}
	counts["issuer_code"] = len(rows)

	if err := q.DeleteSeedDiscoveryKeywords(ctx); err != nil {
		return fmt.Errorf("clear discovery_keyword seed: %w", err)
	}
	rows, err = readSeedCSV("discovery_keyword.csv")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := q.InsertSeedDiscoveryKeyword(ctx, dbconfig.InsertSeedDiscoveryKeywordParams{
			Term: r[0], Source: r[1],
		}); err != nil {
			return fmt.Errorf("insert discovery_keyword %q: %w", r[0], err)
		}
	}
	counts["discovery_keyword"] = len(rows)

	if err := q.DeleteSeedSettings(ctx); err != nil {
		return fmt.Errorf("clear setting seed: %w", err)
	}
	// Per-jurisdiction overrides load FIRST: InsertSeedSetting is ON CONFLICT
	// (key) DO NOTHING, so the first write for a key wins. Settings are global
	// per database (config.setting has no jurisdiction column) and each
	// jurisdiction owns its database, so the active jurisdiction decides.
	// Vocabularies such as amendment.lead_verbs are language-specific: seeding
	// the Vietnamese defaults everywhere left ID/MY/SG/TH/KH matching nothing.
	settingRows := 0
	overrideCSV := fmt.Sprintf("setting_%s.csv", cfg.Jurisdiction)
	overrides, err := readSeedCSV(overrideCSV)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, r := range overrides {
		if err := q.InsertSeedSetting(ctx, dbconfig.InsertSeedSettingParams{
			Key: r[0], Value: r[1],
		}); err != nil {
			return fmt.Errorf("insert setting override %q (%s): %w", r[0], cfg.Jurisdiction, err)
		}
		settingRows++
	}
	rows, err = readSeedCSV("setting.csv")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := q.InsertSeedSetting(ctx, dbconfig.InsertSeedSettingParams{
			Key: r[0], Value: r[1],
		}); err != nil {
			return fmt.Errorf("insert setting %q: %w", r[0], err)
		}
	}
	counts["setting"] = len(rows)
	counts["setting_override"] = settingRows

	if err := q.DeleteSeedValidityStatuses(ctx); err != nil {
		return fmt.Errorf("clear validity_status seed: %w", err)
	}
	rows, err = readSeedCSV("validity_status.csv")
	if err != nil {
		return err
	}
	for _, r := range rows {
		isCurrent, err := strconv.ParseBool(r[3])
		if err != nil {
			return fmt.Errorf("validity_status %q/%q is_current_law: %w", r[0], r[1], err)
		}
		if err := q.InsertSeedValidityStatus(ctx, dbconfig.InsertSeedValidityStatusParams{
			Source: r[0], Code: r[1], StatusClass: r[2], IsCurrentLaw: isCurrent,
		}); err != nil {
			return fmt.Errorf("insert validity_status %q/%q: %w", r[0], r[1], err)
		}
	}
	counts["validity_status"] = len(rows)

	if err := q.DeleteSeedDiacriticRestore(ctx); err != nil {
		return fmt.Errorf("clear diacritic_restore seed: %w", err)
	}
	var drTotal int
	for _, jur := range jurisdiction.All() {
		csvName := fmt.Sprintf("diacritic_restore_%s.csv", jur.Code)
		drRows, err := readSeedCSV(csvName)
		if err != nil {
			// Not every jurisdiction has a diacritic-restore dictionary (e.g. MY, ID).
			// A missing CSV is fine — skip silently.
			if os.IsNotExist(err) {
				continue
			}
			// readSeedCSV wraps os.Open errors in fmt.Errorf; check the underlying
			// error via string match (embedded FS returns *fs.PathError).
			if strings.Contains(err.Error(), "file does not exist") || strings.Contains(err.Error(), "no such file") {
				continue
			}
			return err
		}
		// Bulk-loaded, not row-by-row: this dictionary is ~28K rows per jurisdiction
		// and a per-row INSERT costs one network round-trip each — 25 minutes over a
		// WAN link (measured against prod RDS 2026-07-25), versus seconds via COPY.
		batch := make([][]any, 0, len(drRows))
		for _, r := range drRows {
			share, err := strconv.ParseFloat(r[3], 32)
			if err != nil {
				return fmt.Errorf("diacritic_restore %q share: %w", r[1], err)
			}
			batch = append(batch, []any{r[0], r[1], r[2], float32(share), "seed"})
		}
		if err := copySeedRows(ctx, tx, "diacritic_restore",
			[]string{"jurisdiction", "folded_token", "restored_token", "share", "origin"}, batch); err != nil {
			return fmt.Errorf("copy diacritic_restore (%s): %w", jur.Code, err)
		}
		drTotal += len(drRows)
	}
	counts["diacritic_restore"] = drTotal

	if err := q.DeleteSeedAbbreviationExpand(ctx); err != nil {
		return fmt.Errorf("clear abbreviation_expand seed: %w", err)
	}
	var abbrTotal int
	for _, jur := range jurisdiction.All() {
		csvName := fmt.Sprintf("abbreviation_expand_%s.csv", jur.Code)
		abbrRows, err := readSeedCSV(csvName)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if strings.Contains(err.Error(), "file does not exist") || strings.Contains(err.Error(), "no such file") {
				continue
			}
			return err
		}
		for _, r := range abbrRows {
			if err := q.InsertSeedAbbreviationExpand(ctx, dbconfig.InsertSeedAbbreviationExpandParams{
				Jurisdiction: r[0],
				Abbreviation: r[1],
				Expansion:    r[2],
			}); err != nil {
				return fmt.Errorf("insert abbreviation_expand %q (%s): %w", r[1], jur.Code, err)
			}
		}
		abbrTotal += len(abbrRows)
	}
	counts["abbreviation_expand"] = abbrTotal

	if err := q.DeleteSeedRelationTypes(ctx); err != nil {
		return fmt.Errorf("clear relation_type seed: %w", err)
	}
	rows, err = readSeedCSV("relation_type.csv")
	if err != nil {
		return err
	}
	for _, r := range rows {
		isAmending, err := strconv.ParseBool(r[3])
		if err != nil {
			return fmt.Errorf("relation_type %q/%q is_amending: %w", r[0], r[1], err)
		}
		isSuperseding, err := strconv.ParseBool(r[4])
		if err != nil {
			return fmt.Errorf("relation_type %q/%q is_superseding: %w", r[0], r[1], err)
		}
		if err := q.InsertSeedRelationType(ctx, dbconfig.InsertSeedRelationTypeParams{
			Source: r[0], Code: r[1], Label: r[2], IsAmending: isAmending, IsSuperseding: isSuperseding,
		}); err != nil {
			return fmt.Errorf("insert relation_type %q/%q: %w", r[0], r[1], err)
		}
	}
	counts["relation_type"] = len(rows)

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	log.Info("seeded config",
		"scope_term", counts["scope_term"],
		"issuer_code", counts["issuer_code"],
		"discovery_keyword", counts["discovery_keyword"],
		"diacritic_restore", counts["diacritic_restore"],
		"abbreviation_expand", counts["abbreviation_expand"],
		"setting", counts["setting"],
		"validity_status", counts["validity_status"],
		"relation_type", counts["relation_type"],
	)
	return nil
}

// readSeedCSV reads an embedded seed CSV and returns its data rows with the
// header dropped. FieldsPerRecord stays at the header width, so a malformed row
// is rejected rather than silently widening the table.
func readSeedCSV(name string) ([][]string, error) {
	f, err := seed.FS.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()

	recs, err := csv.NewReader(f).ReadAll()
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	if len(recs) <= 1 {
		return nil, nil
	}
	return recs[1:], nil
}

// copySeedRows bulk-loads seed rows into a config table via COPY, preserving the
// per-row path's ON CONFLICT DO NOTHING semantics: COPY cannot express conflict
// handling, so it lands in a temp table first and the INSERT..SELECT applies the
// conflict rule. That keeps operator rows (origin='user') on the same natural key
// intact, exactly as the row-by-row inserts did.
func copySeedRows(ctx context.Context, tx pgx.Tx, table string, cols []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	tmp := "seed_copy_" + table
	list := strings.Join(cols, ", ")
	// Shaped from the target's own columns (WITH NO DATA), not LIKE: LIKE carries
	// the identity column's NOT NULL without its generator, so COPY would reject
	// every row on a null id.
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		"CREATE TEMP TABLE %s ON COMMIT DROP AS SELECT %s FROM config.%s WITH NO DATA", tmp, list, table)); err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{tmp}, cols, pgx.CopyFromRows(rows)); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(
		"INSERT INTO config.%s (%s) SELECT %s FROM %s ON CONFLICT DO NOTHING", table, list, list, tmp)); err != nil {
		return fmt.Errorf("insert from temp: %w", err)
	}
	if _, err := tx.Exec(ctx, "DROP TABLE "+tmp); err != nil {
		return fmt.Errorf("drop temp: %w", err)
	}
	return nil
}
