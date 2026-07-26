package pipeline

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ResolveRefsResult reports what a re-resolution pass changed.
type ResolveRefsResult struct {
	Scanned   int
	Resolved  int // pointed at a document (newly or corrected)
	Corrected int // previously pointed at a different document
	Cleared   int // previously non-NULL, now NULL (target not identifiable)
	Ambiguous int // matched more than one document, left NULL
	Unmatched int // no document matched, left NULL
	Unchanged int
}

// embeddedDocNumberRe pulls a canonical document number out of a reference
// string. Sources do not always hand us a bare number: Indonesian relation
// targets arrive as whole sentences ("PERATURAN OTORITAS JASA KEUANGAN NOMOR
// 31/POJK.07/2020 TENTANG ..."), while Vietnamese ones are already bare
// ("24/2018/QH14"). The sector-coded forms are listed first because a bare
// "31/POJK.07/2020" also satisfies the looser slash pattern.
var embeddedDocNumberRe = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(\d+\s*/\s*(?:POJK|SEOJK)\.\d+\s*/\s*\d{4})\b`),
	regexp.MustCompile(`(?i)\b(\d+\s*/\s*\d+\s*/\s*(?:PBI|PADG|DPNP|DKSP|DASP)\s*/\s*\d{4})\b`),
	regexp.MustCompile(`(?i)\b(\d+\s*/\s*\d+\s*/\s*[A-Z]{2,6}\s*/\s*\d{4})\b`),
}

// docTypePrefixRe recovers the short doc-type code that Indonesian document
// numbers carry as a prefix ("POJK 21/2023"), so an embedded number can be
// matched against the stored form.
var docTypePrefixRe = regexp.MustCompile(`(?i)\b(POJK|SEOJK|PBI|PADG|PPATK|BSSN|SEBI)\b`)

// refResolutionCandidates returns the normalized doc-number forms a reference
// key might correspond to, most specific first. The whole key is always a
// candidate (that is the Vietnamese case, where ref_key IS the number); an
// embedded number is added with and without its doc-type prefix, because
// Indonesian documents are stored both ways ("PBI 19/12/PBI/2017" and the
// prefix-less "11/2/PBI/2009").
func refResolutionCandidates(refKey string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		n := normalizeDocNumberForStorage(s)
		if n == "" || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}

	for _, re := range embeddedDocNumberRe {
		m := re.FindStringSubmatch(refKey)
		if m == nil {
			continue
		}
		num := strings.Join(strings.Fields(m[1]), "")
		if p := docTypePrefixRe.FindStringSubmatch(refKey); p != nil {
			add(strings.ToUpper(p[1]) + num)
		}
		add(num)
		break
	}
	add(refKey)
	return out
}

// ResolveRefs re-points silver.doc_ref.document_id at the document each
// reference names. Resolution normally happens once, when relation evidence is
// written, and is never revisited — so rebuilding silver.document reissues
// document ids and leaves every stored pointer dangling at a row that no longer
// exists (measured on the Indonesian corpus: 660 of 660). This pass recomputes
// the pointers from ref_key alone and is safe to re-run: it touches one column
// and never writes sections, chunks or embeddings.
//
// A reference resolves only when it matches exactly one document, mirroring
// relationTargetDocumentID. Anything ambiguous or absent is set NULL, which is
// the honest state — a stale pointer silently reads as a confirmed target.
func (a *Activities) ResolveRefs(ctx context.Context, apply bool) (ResolveRefsResult, error) {
	var res ResolveRefsResult
	if a.dbpool == nil {
		return res, fmt.Errorf("resolve refs: no database pool")
	}

	rows, err := a.dbpool.Query(ctx, `SELECT id, ref_key, document_id FROM silver.doc_ref ORDER BY id`)
	if err != nil {
		return res, fmt.Errorf("list doc_refs: %w", err)
	}
	type ref struct {
		id    int64
		key   string
		docID *int64
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.key, &r.docID); err != nil {
			rows.Close()
			return res, fmt.Errorf("scan doc_ref: %w", err)
		}
		refs = append(refs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("iterate doc_refs: %w", err)
	}

	// Both lookup tables are loaded once. Resolving per row costs one round trip
	// per candidate — tolerable locally, but this pass also runs against RDS over
	// an SSM port-forward, where ~10k round trips do not finish.
	byNorm, err := a.loadDocumentsByNumberNorm(ctx)
	if err != nil {
		return res, err
	}
	byAlias, err := a.loadDocumentsByAlias(ctx)
	if err != nil {
		return res, err
	}

	batch := &pgx.Batch{}
	for _, r := range refs {
		res.Scanned++
		var match *int64
		var ambiguous bool

		// Source-keyed refs ("vbpl:187039") identify their target by alias, not by
		// number — the branch relationTargetDocumentID takes first. Resolving those
		// by doc number finds nothing and would wrongly clear a valid pointer.
		if src, ext, ok := splitSourceRefKey(r.key); ok {
			if id, ok := byAlias[src+":"+ext]; ok {
				match = &id
			}
		} else {
			for _, cand := range refResolutionCandidates(r.key) {
				ids := byNorm[cand]
				if len(ids) == 1 {
					id := ids[0]
					match = &id
					break
				}
				if len(ids) > 1 {
					ambiguous = true
				}
			}
		}

		switch {
		case match != nil && r.docID != nil && *match == *r.docID:
			res.Unchanged++
			continue
		case match != nil:
			res.Resolved++
			if r.docID != nil {
				res.Corrected++
			}
		case r.docID != nil:
			res.Cleared++
		default:
			res.Unchanged++
			continue
		}
		if match == nil && ambiguous {
			res.Ambiguous++
		} else if match == nil {
			res.Unmatched++
		}
		if apply {
			batch.Queue(`UPDATE silver.doc_ref SET document_id = $1 WHERE id = $2`, match, r.id)
		}
	}

	if apply && batch.Len() > 0 {
		br := a.dbpool.SendBatch(ctx, batch)
		for i := 0; i < batch.Len(); i++ {
			if _, err := br.Exec(); err != nil {
				_ = br.Close()
				return res, fmt.Errorf("update doc_ref: %w", err)
			}
		}
		if err := br.Close(); err != nil {
			return res, fmt.Errorf("close doc_ref batch: %w", err)
		}
	}
	return res, nil
}

// splitSourceRefKey recognises the "source:external_id" ref-key form that
// sourceDocRefKey writes. A doc number never contains ':' , so the separator is
// unambiguous; both halves must be non-empty.
func splitSourceRefKey(refKey string) (source, externalID string, ok bool) {
	i := strings.Index(refKey, ":")
	if i <= 0 || i == len(refKey)-1 {
		return "", "", false
	}
	source = strings.TrimSpace(refKey[:i])
	externalID = strings.TrimSpace(refKey[i+1:])
	if source == "" || externalID == "" {
		return "", "", false
	}
	return source, externalID, true
}

// loadDocumentsByNumberNorm maps normalized doc number to the documents carrying
// it. Multiple ids mean the number is ambiguous in this corpus (VN has ~305
// shared doc_numbers), and an ambiguous reference must stay unresolved.
func (a *Activities) loadDocumentsByNumberNorm(ctx context.Context) (map[string][]int64, error) {
	rows, err := a.dbpool.Query(ctx,
		`SELECT doc_number_norm, id FROM silver.document WHERE doc_number_norm IS NOT NULL AND doc_number_norm <> ''`)
	if err != nil {
		return nil, fmt.Errorf("load documents by number: %w", err)
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var norm string
		var id int64
		if err := rows.Scan(&norm, &id); err != nil {
			return nil, fmt.Errorf("scan document number: %w", err)
		}
		out[norm] = append(out[norm], id)
	}
	return out, rows.Err()
}

// loadDocumentsByAlias maps "source:external_id" to its document.
func (a *Activities) loadDocumentsByAlias(ctx context.Context) (map[string]int64, error) {
	rows, err := a.dbpool.Query(ctx, `SELECT source, external_id, document_id FROM silver.document_alias`)
	if err != nil {
		return nil, fmt.Errorf("load document aliases: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var src, ext string
		var id int64
		if err := rows.Scan(&src, &ext, &id); err != nil {
			return nil, fmt.Errorf("scan document alias: %w", err)
		}
		out[src+":"+ext] = id
	}
	return out, rows.Err()
}
