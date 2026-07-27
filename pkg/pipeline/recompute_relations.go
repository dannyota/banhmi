package pipeline

import (
	"context"
	"fmt"
	"time"
)

// RecomputeRelationsResult reports what a relation-evidence recompute changed.
type RecomputeRelationsResult struct {
	Scanned          int
	Processed        int
	Skipped          int
	EvidenceWritten  int
	RelationsWritten int
	Failed           int
}

// RecomputeRelations re-derives relation evidence for every document of a
// source from what is already in bronze and silver.
//
// Relation evidence is normally written by Normalize, so picking up a source
// parser fix would otherwise mean re-normalizing — and that is not parity-safe:
// replaceNormalizeSections deletes and re-inserts sections, reissuing every
// silver.document_section.id, while gold.chunk.section_id is a bare BIGINT with
// no foreign key. It would dangle silently and force a re-index and re-embed of
// the affected documents.
//
// This pass instead drives persistRelationEvidence directly. That function READS
// sections and bronze payloads and writes only doc_ref, relation_evidence and
// document_relation, so chunks, embeddings and citations are untouched.
func (a *Activities) RecomputeRelations(ctx context.Context, source string, apply bool, limit int) (RecomputeRelationsResult, error) {
	var res RecomputeRelationsResult
	if a.dbpool == nil {
		return res, fmt.Errorf("recompute relations: no database pool")
	}

	q := `SELECT id FROM ingest.fetch_doc
	       WHERE state IN ('complete','partial')` + sourceFilterSQL(source) + `
	       ORDER BY id`
	args := []any{}
	if source != "" {
		args = append(args, source)
	}
	rows, err := a.dbpool.Query(ctx, q, args...)
	if err != nil {
		return res, fmt.Errorf("list fetch_docs: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, fmt.Errorf("scan fetch_doc id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("iterate fetch_docs: %w", err)
	}
	if limit > 0 && len(ids) > limit {
		ids = ids[:limit]
	}

	for _, id := range ids {
		res.Scanned++
		if err := ctx.Err(); err != nil {
			// A cancelled context is not a skip. Reporting it as one made a
			// timed-out run look like 609 documents missing from the database.
			return res, fmt.Errorf("recompute relations cancelled after %d/%d documents: %w",
				res.Processed, len(ids), err)
		}
		target, err := a.loadNormalizeTarget(ctx, StageParams{FetchDocID: id})
		if err != nil {
			// A fetch_doc without a silver document has nothing to recompute.
			// Log it: a silent skip once hid a 609-document discrepancy between
			// two databases holding the same corpus.
			res.Skipped++
			if res.Skipped <= 5 {
				a.log.Warn("recompute-relations: skipped", "fetch_doc", id, "err", err)
			}
			continue
		}
		result, err := a.reconstructNormalizeResult(ctx, target)
		if err != nil {
			return res, err
		}
		if !apply {
			res.Processed++
			continue
		}
		evidence, relations, err := a.persistRelationEvidence(ctx, target, time.Now().UTC(), &result)
		if err != nil {
			a.log.Warn("recompute-relations: document failed",
				"fetch_doc", id, "document_id", target.document.ID, "err", err)
			res.Failed++
			continue
		}
		res.Processed++
		res.EvidenceWritten += evidence
		res.RelationsWritten += relations
	}
	return res, nil
}

func sourceFilterSQL(source string) string {
	if source == "" {
		return ""
	}
	return ` AND source = $1`
}

// reconstructNormalizeResult rebuilds the one field persistRelationEvidence
// reads from the normalize run: TextAuthority, which decides the recorded
// source authority of every text-derived row. Leaving it empty would silently
// downgrade existing evidence to "unknown".
func (a *Activities) reconstructNormalizeResult(ctx context.Context, target normalizeTarget) (NormalizeResult, error) {
	var res NormalizeResult

	// Documents built from the vbpl provision tree carry a node_key on their
	// sections; Normalize records that as the tree authority rather than the
	// authority of any extracted text.
	var treeSections int
	if err := a.dbpool.QueryRow(ctx,
		`SELECT count(*) FROM silver.document_section WHERE document_id = $1 AND node_key IS NOT NULL`,
		target.document.ID).Scan(&treeSections); err != nil {
		return res, fmt.Errorf("count tree sections doc=%d: %w", target.document.ID, err)
	}
	if treeSections > 0 {
		res.TextAuthority = "vbpl_provision_tree"
		return res, nil
	}

	texts, err := a.silver.ListTextsByDocument(ctx, target.document.ID)
	if err != nil {
		return res, fmt.Errorf("list document texts doc=%d: %w", target.document.ID, err)
	}
	for _, txt := range texts {
		if txt.IsBinding {
			res.TextAuthority = txt.Authority
			return res, nil
		}
	}
	return res, nil
}
