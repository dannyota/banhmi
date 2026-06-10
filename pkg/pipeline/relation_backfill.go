package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/pkg/ingest"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// BackfillRelationTargetsParams controls a bounded relation-target enqueue pass.
type BackfillRelationTargetsParams struct {
	// Limit caps the number of unresolved relation targets inspected. 0 uses a
	// conservative default so one manual run cannot enqueue the whole graph by
	// accident.
	Limit int32
}

// BackfillRelationTargetsResult summarizes the enqueue pass.
type BackfillRelationTargetsResult struct {
	Candidates int
	Enqueued   int
	Skipped    int
}

type relationBackfillRef struct {
	Source       string `json:"source"`
	TargetID     string `json:"target_id"`
	TargetNumber string `json:"target_number"`
	TargetTitle  string `json:"target_title"`
}

type relationTargetBackfillCandidate struct {
	docRefID      int64
	refKey        string
	label         *string
	srcRef        json.RawMessage
	relationType  string
	srcFetchDocID int64
}

// BackfillRelationTargets enqueues promoted official VBPL relation targets as
// normal fetch_doc rows. It does not call source APIs; Fetch later resolves each
// target through the normal source detail path.
func (a *Activities) BackfillRelationTargets(ctx context.Context, p BackfillRelationTargetsParams) (BackfillRelationTargetsResult, error) {
	limit := p.Limit
	if limit <= 0 {
		limit = 500
	}
	rows, err := a.listRelationTargetBackfillCandidates(ctx, limit)
	if err != nil {
		return BackfillRelationTargetsResult{}, fmt.Errorf("list relation target backfill candidates: %w", err)
	}
	now := time.Now().UTC()
	var res BackfillRelationTargetsResult
	res.Candidates = len(rows)
	for _, row := range rows {
		ref, ok := parseRelationBackfillRef(row.srcRef)
		if !ok {
			res.Skipped++
			continue
		}
		enqueued, err := a.enqueueRelationTargetDoc(ctx,
			ref,
			row.refKey,
			nullableString(row.label),
			row.srcFetchDocID,
			row.relationType,
			now,
		)
		if err != nil {
			return res, err
		}
		if enqueued {
			res.Enqueued++
		} else {
			res.Skipped++
		}
	}
	activity.GetLogger(ctx).Info("relation target backfill complete",
		"candidates", res.Candidates, "enqueued", res.Enqueued, "skipped", res.Skipped)
	return res, nil
}

func (a *Activities) listRelationTargetBackfillCandidates(ctx context.Context, limit int32) ([]relationTargetBackfillCandidate, error) {
	if a.dbpool == nil {
		return nil, fmt.Errorf("db pool is required")
	}
	const q = `
WITH candidates AS (
    SELECT DISTINCT ON (r.id)
        r.id AS doc_ref_id,
        r.ref_key,
        r.label,
        r.src_ref,
        ev.relation_type,
        fd.id AS src_fetch_doc_id
    FROM silver.doc_ref r
    JOIN silver.relation_evidence ev ON ev.target_ref_id = r.id
    JOIN silver.document d ON d.id = ev.from_document_id
    JOIN bronze.source_document sd ON sd.id = d.source_document_id
    JOIN ingest.fetch_doc fd ON fd.source = sd.source AND fd.external_id = sd.external_id
    LEFT JOIN ingest.fetch_doc target_fd
        ON target_fd.source = r.src_ref->>'source'
       AND target_fd.external_id = r.src_ref->>'target_id'
    LEFT JOIN silver.document_alias target_alias
        ON target_alias.source = r.src_ref->>'source'
       AND target_alias.external_id = r.src_ref->>'target_id'
    WHERE r.document_id IS NULL
      AND target_fd.id IS NULL
      AND target_alias.document_id IS NULL
      AND ev.promoted
      AND ev.evidence_kind = 'structured_relation'
      AND ev.source = 'vbpl'
      AND r.src_ref->>'source' = 'vbpl'
      AND COALESCE(r.src_ref->>'target_id', '') <> ''
      AND fd.provenance <> 'relation'
    ORDER BY r.id, ev.confidence DESC, ev.id
)
SELECT doc_ref_id, ref_key, label, src_ref, relation_type, src_fetch_doc_id
FROM candidates
ORDER BY doc_ref_id
LIMIT $1`
	rows, err := a.dbpool.Query(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []relationTargetBackfillCandidate
	for rows.Next() {
		var row relationTargetBackfillCandidate
		if err := rows.Scan(
			&row.docRefID,
			&row.refKey,
			&row.label,
			&row.srcRef,
			&row.relationType,
			&row.srcFetchDocID,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *Activities) backfillRelationCandidatesBestEffort(
	ctx context.Context,
	target normalizeTarget,
	candidates []relationCandidate,
	now time.Time,
	result *NormalizeResult,
) {
	enqueued, err := a.backfillRelationCandidates(ctx, target, candidates, now)
	if err != nil {
		result.Warnings = append(result.Warnings, "relation_target_backfill_failed")
		activity.GetLogger(ctx).Warn("normalize: relation target backfill failed",
			"doc", target.fetchDoc.ExternalID, "document_id", target.document.ID, "err", err)
		return
	}
	result.RelationTargetsEnqueued = enqueued
}

func (a *Activities) backfillRelationCandidates(
	ctx context.Context,
	target normalizeTarget,
	candidates []relationCandidate,
	now time.Time,
) (int, error) {
	if target.fetchDoc.Provenance == "relation" {
		return 0, nil
	}
	seen := map[string]bool{}
	enqueued := 0
	for _, candidate := range candidates {
		if !candidate.promoted ||
			candidate.evidenceKind != "structured_relation" ||
			candidate.source != "vbpl" ||
			strings.TrimSpace(candidate.targetID) == "" ||
			strings.TrimSpace(candidate.targetNumber) == "" {
			continue
		}
		key := candidate.source + "/" + candidate.targetID
		if seen[key] {
			continue
		}
		seen[key] = true
		ref := relationBackfillRef{
			Source:       candidate.source,
			TargetID:     candidate.targetID,
			TargetNumber: candidate.targetNumber,
			TargetTitle:  candidate.targetTitle,
		}
		if alreadyResolved, err := a.relationTargetAlreadyResolved(ctx, ref); err != nil {
			return enqueued, err
		} else if alreadyResolved {
			continue
		}
		ok, err := a.enqueueRelationTargetDoc(ctx,
			ref,
			relationTargetRefKey(candidate),
			candidate.targetTitle,
			target.fetchDoc.ID,
			relationTypeOrMention(candidate.relationType),
			now,
		)
		if err != nil {
			return enqueued, err
		}
		if ok {
			enqueued++
		}
	}
	return enqueued, nil
}

func (a *Activities) relationTargetAlreadyResolved(ctx context.Context, ref relationBackfillRef) (bool, error) {
	if sourceKey := sourceDocRefKey(ref.Source, ref.TargetID); sourceKey != "" {
		_, err := a.silver.DocumentIDByAlias(ctx, dbsilver.DocumentIDByAliasParams{
			Source:     strings.TrimSpace(ref.Source),
			ExternalID: strings.TrimSpace(ref.TargetID),
		})
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, pgx.ErrNoRows):
			return false, nil
		default:
			return false, fmt.Errorf("check relation target alias %s: %w", sourceKey, err)
		}
	}
	norm := normalizeDocNumberForStorage(ref.TargetNumber)
	if norm == "" {
		return false, nil
	}
	ids, err := a.silver.DocumentIDsByNumberNorm(ctx, norm)
	if err != nil {
		return false, fmt.Errorf("check relation target document %s: %w", ref.TargetNumber, err)
	}
	return len(ids) > 0, nil
}

func (a *Activities) enqueueRelationTargetDoc(
	ctx context.Context,
	ref relationBackfillRef,
	refKey string,
	label string,
	srcFetchDocID int64,
	relationType string,
	now time.Time,
) (bool, error) {
	source := strings.TrimSpace(ref.Source)
	targetID := strings.TrimSpace(ref.TargetID)
	if source != "vbpl" || targetID == "" {
		return false, nil
	}
	number := strings.TrimSpace(ref.TargetNumber)
	if number == "" {
		number = strings.TrimSpace(label)
	}
	if number == "" {
		number = strings.TrimSpace(refKey)
	}
	title := strings.TrimSpace(ref.TargetTitle)
	if title == "" {
		title = strings.TrimSpace(label)
	}
	doc := ingest.DiscoveredDoc{
		SourceID:    source,
		ExternalID:  targetID,
		DocGUID:     targetID,
		Number:      number,
		Title:       title,
		DetailURL:   "https://vbpl.vn/van-ban/chi-tiet/" + targetID,
		PublishedAt: now,
		RawMeta:     json.RawMessage(`{"source":"relation_backfill"}`),
	}
	if err := a.recordDiscoveredDoc(ctx,
		source,
		"relation",
		"relation",
		doc,
		[]string{number},
		srcFetchDocID,
		relationType,
		now,
	); err != nil {
		return false, fmt.Errorf("enqueue relation target %s/%s: %w", source, targetID, err)
	}
	return true, nil
}

func parseRelationBackfillRef(raw json.RawMessage) (relationBackfillRef, bool) {
	if len(raw) == 0 {
		return relationBackfillRef{}, false
	}
	var ref relationBackfillRef
	if err := json.Unmarshal(raw, &ref); err != nil {
		return relationBackfillRef{}, false
	}
	ref.Source = strings.TrimSpace(ref.Source)
	ref.TargetID = strings.TrimSpace(ref.TargetID)
	ref.TargetNumber = strings.TrimSpace(ref.TargetNumber)
	ref.TargetTitle = strings.TrimSpace(ref.TargetTitle)
	return ref, ref.Source != "" && ref.TargetID != ""
}

// BackfillRelationsWorkflow runs one bounded relation-target enqueue pass.
func BackfillRelationsWorkflow(ctx workflow.Context, p BackfillRelationTargetsParams) (BackfillRelationTargetsResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           LocalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    3,
		},
	})

	var a *Activities
	var res BackfillRelationTargetsResult
	if err := workflow.ExecuteActivity(ctx, a.BackfillRelationTargets, p).Get(ctx, &res); err != nil {
		return BackfillRelationTargetsResult{}, err
	}
	workflow.GetLogger(ctx).Info("relation backfill workflow complete",
		"candidates", res.Candidates, "enqueued", res.Enqueued, "skipped", res.Skipped)
	return res, nil
}
