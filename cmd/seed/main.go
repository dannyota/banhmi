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
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"

	seed "danny.vn/banhmi/deploy/seed"
	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/base/db"
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
	rows, err := readSeedCSV("scope_term.csv")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := q.InsertSeedScopeTerm(ctx, dbconfig.InsertSeedScopeTermParams{
			Jurisdiction: "vn", Term: r[0], TermClass: r[1], Theme: r[2],
		}); err != nil {
			return fmt.Errorf("insert scope_term %q: %w", r[0], err)
		}
	}
	scopeTotal := len(rows)
	myScope, err := readSeedCSV("scope_term_my.csv")
	if err != nil {
		return err
	}
	for _, r := range myScope {
		if err := q.InsertSeedScopeTerm(ctx, dbconfig.InsertSeedScopeTermParams{
			Jurisdiction: "my", Term: r[0], TermClass: r[1], Theme: r[2],
		}); err != nil {
			return fmt.Errorf("insert my scope_term %q: %w", r[0], err)
		}
	}
	counts["scope_term"] = scopeTotal + len(myScope)

	if err := q.DeleteSeedIssuerCodes(ctx); err != nil {
		return fmt.Errorf("clear issuer_code seed: %w", err)
	}
	rows, err = readSeedCSV("issuer_code.csv")
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
		if err := q.InsertSeedRelationType(ctx, dbconfig.InsertSeedRelationTypeParams{
			Source: r[0], Code: r[1], Label: r[2], IsAmending: isAmending,
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
