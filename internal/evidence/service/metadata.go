package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/internal/evidence/api"
)

// LoadStartupMetadata reads bounded presentation facts once during serving
// startup. Request handlers receive the neutral value and perform no metadata
// discovery.
func LoadStartupMetadata(ctx context.Context, pool *pgxpool.Pool, metadata api.StartupMetadata) (api.StartupMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var loadErrors []error
	const coverageQuery = `SELECT (SELECT count(*) FROM silver.document), (SELECT count(*) FROM gold.chunk), (SELECT count(DISTINCT source) FROM bronze.source_document)`
	var documents, chunks, sources int64
	if err := pool.QueryRow(ctx, coverageQuery).Scan(&documents, &chunks, &sources); err != nil {
		loadErrors = append(loadErrors, fmt.Errorf("load startup coverage: %w", err))
	} else if documents > 0 {
		metadata.Coverage = formatCoverage(metadata.Country, documents, chunks, sources)
	}
	issuers, err := distinctStrings(ctx, pool, `SELECT issuer FROM silver.document WHERE COALESCE(issuer, '') <> '' GROUP BY issuer ORDER BY count(*) DESC, issuer LIMIT 10`)
	if err != nil {
		loadErrors = append(loadErrors, fmt.Errorf("load startup issuers: %w", err))
	} else {
		metadata.IssuerVocabulary = issuers
	}
	documentTypes, err := distinctStrings(ctx, pool, `SELECT DISTINCT btrim(doc_type) FROM silver.document WHERE NULLIF(btrim(doc_type), '') IS NOT NULL ORDER BY 1 LIMIT 100`)
	if err != nil {
		loadErrors = append(loadErrors, fmt.Errorf("load startup document types: %w", err))
	} else {
		metadata.DocumentTypeVocabulary = documentTypes
	}
	return metadata, errors.Join(loadErrors...)
}

func distinctStrings(ctx context.Context, pool *pgxpool.Pool, query string) ([]string, error) {
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func formatCoverage(country string, documents, chunks, sources int64) string {
	product := map[string]string{
		"vn": "banhmi",
		"my": "laksa",
		"id": "rendang",
		"sg": "kaya",
		"th": "tomyum",
		"kh": "amok",
	}[country]
	if product == "" {
		product = "banhmi"
	}
	sourceKind := "official"
	if country == "vn" {
		sourceKind = "government"
	}
	return fmt.Sprintf("%s has extracted and indexed %d official documents (%d provisions) across %d %s sources", product, documents, chunks, sources, sourceKind)
}
