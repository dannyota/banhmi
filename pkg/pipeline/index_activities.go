package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	pgvector "github.com/pgvector/pgvector-go"

	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbgold "danny.vn/banhmi/pkg/store/gold"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// maxDieuTokens is the rough token threshold above which a Điều is split into
// per-Khoản chunks instead of one monolithic chunk. The same ceiling is also
// applied to emitted chunks so very long Khoản text is split into deterministic
// paragraph shards.
const maxDieuTokens = 512

// minChunkContentLen is the minimum character length for chunk content after
// label/heading stripping (normalizeChunkLabel). Chunks shorter than this are
// degenerate fragments — orphan words from paragraph splits, bare form labels,
// table cell debris — that add noise without legal substance. Calibrated against
// VN (banhmi), MY (laksa), and ID (rendang) corpora: everything under 20 chars
// is universally junk across all three jurisdictions.
const minChunkContentLen = 20

// Keep Index requests modest so one large document cannot fail as a single
// oversized embedding request.
const embedBatchSize = 32

// Context prefixes are embedding hints, not primary evidence. Cap each line so a
// very long title or heading cannot dominate the vector text for every chunk.
const maxPrefixFieldRunes = 220

// chunkRecord pairs a written gold.chunk id with the text used to embed it.
type chunkRecord struct {
	id   int64
	text string // context_prefix + "\n" + content
}

// Index reads the document_section tree, chunks by Điều (splitting long Điều
// by Khoản), prepends a deterministic contextual prefix to each chunk, writes
// gold.chunk rows via UpsertChunk, and — when an embedder is configured — embeds
// each chunk's text and upserts the vector into gold.chunk_embedding. Embedding
// is best-effort: a nil embedder or endpoint error is logged and skipped; chunks
// are always written and embeddings can be backfilled later.
func (a *Activities) Index(ctx context.Context, p StageParams) (IndexResult, error) {
	log := a.log
	now := time.Now().UTC()

	// 1. Resolve silver document.
	fd, err := a.ledger.GetFetchDocByID(ctx, p.FetchDocID)
	if err != nil {
		return IndexResult{}, fmt.Errorf("get fetch_doc %d: %w", p.FetchDocID, err)
	}
	sd, err := a.bronze.SourceDocumentByExternalID(ctx, dbbronze.SourceDocumentByExternalIDParams{
		Source: fd.Source, ExternalID: fd.ExternalID,
	})
	if err != nil {
		return IndexResult{}, fmt.Errorf("source_document %s/%s: %w", fd.Source, fd.ExternalID, err)
	}
	doc, err := a.silver.DocumentByKey(ctx, docKey(sd))
	if err != nil {
		return IndexResult{}, fmt.Errorf("silver document for %s: %w", fd.ExternalID, err)
	}

	// 1b. Scope gate for relation-pulled documents. Documents that exist only
	// through relation backfill and fall outside the configured scope vocabulary
	// stay relation context: text and relations remain served (document tool,
	// verbatim amendment clauses), but no chunks enter the searchable corpus.
	// The verdict is persisted on silver.document so enumeration and the MCP
	// status tools can account for it.
	contextOnly, err := a.relationContextOnly(ctx, doc)
	if err != nil {
		return IndexResult{}, err
	}
	indexClass := "primary"
	if contextOnly {
		indexClass = "relation_context"
	}
	if err := a.silver.SetDocumentIndexClass(ctx, dbsilver.SetDocumentIndexClassParams{
		ID:         doc.ID,
		IndexClass: indexClass,
		UpdatedAt:  now,
	}); err != nil {
		return IndexResult{}, fmt.Errorf("set index class doc=%d: %w", doc.ID, err)
	}
	if contextOnly {
		if _, err := a.gold.DeleteChunksByDocument(ctx, doc.ID); err != nil {
			return IndexResult{}, fmt.Errorf("delete out-of-scope chunks doc=%d: %w", doc.ID, err)
		}
		log.Info("index: relation-context document out of scope, not indexed",
			"doc", fd.ExternalID, "document_id", doc.ID)
		return IndexResult{DocumentID: doc.ID}, nil
	}

	// 2. Fetch the flat section list (ordered by ordinal).
	sectionRows, err := a.silver.ListSectionsByDocument(ctx, doc.ID)
	if err != nil {
		return IndexResult{}, fmt.Errorf("list sections doc=%d: %w", doc.ID, err)
	}
	allSections := silverSectionRows(sectionRows)

	goldQ := a.gold
	var tx pgx.Tx
	if a.dbpool != nil {
		tx, err = a.dbpool.Begin(ctx)
		if err != nil {
			return IndexResult{}, fmt.Errorf("begin index transaction: %w", err)
		}
		defer func() {
			if tx != nil {
				_ = tx.Rollback(ctx)
			}
		}()
		goldQ = a.gold.WithTx(tx)
	}

	if _, err := goldQ.DeleteChunksByDocument(ctx, doc.ID); err != nil {
		return IndexResult{}, fmt.Errorf("delete chunks doc=%d: %w", doc.ID, err)
	}
	if len(allSections) == 0 {
		if tx != nil {
			if err := tx.Commit(ctx); err != nil {
				return IndexResult{}, fmt.Errorf("commit empty index transaction: %w", err)
			}
			tx = nil
		}
		log.Warn("index: no sections found, skipping chunking",
			"doc", fd.ExternalID, "document_id", doc.ID)
		return IndexResult{DocumentID: doc.ID}, nil
	}

	// 3. Build a parent-id map for walking the tree without recursion.
	byID := make(map[int64]*dbsilver.SilverDocumentSection, len(allSections))
	for i := range allSections {
		byID[allSections[i].ID] = &allSections[i]
	}
	childrenByParent := buildChildrenByParent(allSections)

	// 4. Collect enclosing Chương/Mục for each Điều by walking parent links.
	enclosing := func(sec *dbsilver.SilverDocumentSection) (chuong, muc string) {
		cur := sec
		chuongKind, mucKind := "", ""
		for cur.ParentID != nil {
			par := byID[*cur.ParentID]
			if par == nil {
				break
			}
			label := labelStr(par)
			if par.Heading != nil {
				label += " " + *par.Heading
			}
			switch par.Kind {
			case "chuong", "part", "bab": // top container slot
				if chuong == "" {
					chuong = label
					chuongKind = par.Kind
				}
			case "muc", "chapter", "bagian": // sub-container slot
				if muc == "" {
					muc = label
					mucKind = par.Kind
				}
			}
			cur = par
		}
		// Thai hierarchy: chapter > part (หมวด > ส่วนที่). Walking bottom-up
		// from a section encounters "part" before "chapter", putting chapter
		// in the sub slot. Swap so the top container (chapter) renders first.
		if chuong != "" && muc != "" && mucKind == "chapter" && chuongKind == "part" {
			chuong, muc = muc, chuong
		}
		return
	}

	// 5. Build the contextual prefix template: số ký hiệu + title + eff date.
	docNumber := ""
	if doc.DocNumber != nil {
		docNumber = *doc.DocNumber
	}
	docTitle := ""
	if doc.Title != nil {
		docTitle = *doc.Title
	}
	effDate := ""
	if sd.EffectiveAt != nil {
		effDate = sd.EffectiveAt.UTC().Format("02/01/2006")
	} else if doc.IssuedAt != nil {
		effDate = doc.IssuedAt.UTC().Format("02/01/2006")
	}

	// 5b. Detect article-level citation collisions: when a document has
	// multiple Điều with the same citation part (restart-numbered per
	// Chương/Mục — common in VN consolidated regulations), prepend the
	// enclosing Chương/Mục to disambiguate.
	collidingDieuCite := buildCollidingDieuCitations(allSections)
	phCiteCounter := make(map[string]int) // running counter for colliding phuluc citations

	// 6. Chunk each Điều.
	// Collect Khoản children for each Điều by citation_path prefix.
	// Sections are ordered; we iterate and emit chunks in ordinal order.
	ordinal := 0
	written := 0
	droppedDedup := 0
	droppedShort := 0

	var chunks []chunkRecord
	seenContent := make(map[string]struct{}) // intra-document content dedup

	emitChunk := func(citation, prefix, content string, sectionID *int64) error {
		// Intra-document content dedup: when two chunks in the same document
		// have identical content text, keep the first (lowest ordinal). Exact
		// byte comparison — whitespace-normalized matching catches only ~5
		// additional duplicates globally and isn't worth the complexity.
		if _, dup := seenContent[content]; dup {
			droppedDedup++
			log.Debug("index: dropped duplicate chunk",
				"doc", fd.ExternalID, "citation", citation, "content_len", len(content))
			return nil
		}
		// Minimum content length: strip label/heading boilerplate (same
		// normalization as labelOnlyChunk) and reject degenerate fragments —
		// orphan words, form labels, table cell debris.
		if len([]rune(normalizeChunkLabel(content))) < minChunkContentLen {
			droppedShort++
			log.Debug("index: dropped short chunk",
				"doc", fd.ExternalID, "citation", citation, "content_len", len(content),
				"normalized_len", len([]rune(normalizeChunkLabel(content))))
			return nil
		}
		seenContent[content] = struct{}{}

		ordinal++
		tc := roughTokenCount(prefix + "\n" + content)
		tc32 := int32(tc) //nolint:gosec
		id, uerr := goldQ.UpsertChunk(ctx, dbgold.UpsertChunkParams{
			DocumentID:        doc.ID,
			DocumentVersionID: nil,
			SectionID:         sectionID,
			Citation:          citation,
			ContextPrefix:     &prefix,
			Content:           content,
			Ordinal:           int32(ordinal), //nolint:gosec
			TokenCount:        &tc32,
		})
		if uerr != nil {
			return fmt.Errorf("upsert chunk %q: %w", citation, uerr)
		}
		written++
		chunks = append(chunks, chunkRecord{id: id, text: prefix + "\n" + content})
		return nil
	}
	// A long leaf split into mechanical passages cites "Đoạn N" (Vietnamese) or
	// "Paragraph N" (e.g. Malaysia's English corpus) — the label comes from the
	// jurisdiction descriptor.
	paraWord := a.jur.ParagraphLabel
	emitSectionChunks := func(sec *dbsilver.SilverDocumentSection, citation, prefix, content string, sectionID *int64) error {
		if labelOnlyChunk(sec, citation, content) {
			return nil
		}
		parts := splitLongChunkContent(content, maxDieuTokens)
		if len(parts) == 0 {
			return nil
		}
		// Filter label-only split fragments (e.g. a heading orphan that is just
		// "Điều N. Heading" after splitting on newlines). Renumber survivors so
		// Đoạn indices are contiguous from 1, and drop the suffix entirely when
		// only one part survives (matching the single-part path).
		if len(parts) > 1 {
			filtered := parts[:0]
			for _, part := range parts {
				if !labelOnlyChunk(sec, citation, part) {
					filtered = append(filtered, part)
				}
			}
			parts = filtered
		}
		if len(parts) == 0 {
			return nil
		}
		if len(parts) == 1 {
			return emitChunk(citation, prefix, parts[0], sectionID)
		}
		for i, part := range parts {
			partCitation := fmt.Sprintf("%s, %s %d", citation, paraWord, i+1)
			if err := emitChunk(partCitation, prefix, part, sectionID); err != nil {
				return err
			}
		}
		return nil
	}

	// emitProvisionChunks chunks one section by the legal hierarchy: if it fits in a
	// chunk, emit one; otherwise split by its structured children (Khoản under Điều,
	// Điểm under Khoản), prepending the parent's lead-in text so each child chunk
	// stays self-contained. A long leaf with no structured children falls back to Đoạn
	// paragraph-splitting — the last resort, not the default for any long Khoản.
	var emitProvisionChunks func(sec *dbsilver.SilverDocumentSection, citation, prefix, lead string) error
	emitProvisionChunks = func(sec *dbsilver.SilverDocumentSection, citation, prefix, lead string) error {
		content := sectionTreeContent(sec, childrenByParent)
		if lead != "" {
			content = strings.TrimSpace(lead + "\n" + content)
		}
		sid := sec.ID
		if roughTokenCount(content) <= maxDieuTokens {
			return emitSectionChunks(sec, citation, prefix, content, &sid)
		}
		children := structuredChildren(sec, childrenByParent)
		if len(children) == 0 {
			return emitSectionChunks(sec, citation, prefix, content, &sid)
		}
		childLead := strings.TrimSpace(lead)
		if own := strings.TrimSpace(sectionOwnText(sec)); own != "" {
			// Guard: if the parent's own text is just its label (e.g. "Pasal 8",
			// "Section 12", "Điều 5", "มาตรา 7"), drop it — the parent label is
			// already in the contextual prefix. Without this, the label text
			// leaks into every child chunk as a 7-byte noise fragment that
			// labelOnlyChunk cannot catch (it only sees the child's label).
			if !labelOnlyChunk(sec, sectionCitationPart(sec), own) {
				childLead = strings.TrimSpace(childLead + "\n" + own)
			}
		}
		for _, c := range children {
			childCitation := strings.Join(nonEmptyStrings(citation, sectionCitationPart(c)), ", ")
			if err := emitProvisionChunks(c, childCitation, prefix, childLead); err != nil {
				return err
			}
		}
		return nil
	}

	for i := range allSections {
		sec := &allSections[i]
		switch sec.Kind {
		case "dieu", "section", "pasal": // MY: Section / ID: Pasal is the article-level chunk unit
			chuong, muc := enclosing(sec)
			basePrefix := buildPrefix(docNumber, docTitle, chuong, muc, effDate, a.jur.EffectiveDateLabel)
			citation := sectionCitationPart(sec)
			// An Điều nested in an appendix (a Quy chế/Quy định "ban hành kèm
			// theo") cites its Phụ lục so it cannot be confused with the
			// enacting document's own Điều of the same number.
			if pl := enclosingPhuLuc(sec, byID, collidingDieuCite); pl != "" {
				citation = pl + ", " + citation
			}
			// When a document restarts Điều numbering per Chương/Mục (common
			// in VN consolidated regulations), prepend the enclosing container
			// to keep citations unique — "Chương I, Điều 1" vs "Chương III, Điều 1".
			if _, collides := collidingDieuCite[citation]; collides {
				parts := nonEmptyStrings(chuong, muc, citation)
				citation = strings.Join(parts, ", ")
			}
			if err := emitProvisionChunks(sec, citation, basePrefix, ""); err != nil {
				return IndexResult{}, err
			}
		case "phuluc", "schedule", "lampiran": // MY: Schedule / ID: Lampiran
			// The appendix's own text (tables, forms, thresholds — anything not
			// under a nested Điều) is real legal substance; chunk it under the
			// "Phụ lục N" citation. Nested Điều are walked by the case above.
			content := strings.TrimSpace(sectionOwnText(sec))
			if content == "" {
				continue
			}
			chuong, muc := enclosing(sec)
			basePrefix := buildPrefix(docNumber, docTitle, chuong, muc, effDate, a.jur.EffectiveDateLabel)
			sid := sec.ID
			phCitation := sectionCitationPart(sec)
			// When multiple appendices share the same citation (e.g. multiple
			// "Phụ lục" without a designator, or "Phụ lục V" appearing many
			// times in a messy document), append a running counter.
			if _, collides := collidingDieuCite[phCitation]; collides {
				phCiteCounter[phCitation]++
				phCitation = phCitation + " (" + strconv.Itoa(phCiteCounter[phCitation]) + ")"
			}
			if err := emitSectionChunks(sec, phCitation, basePrefix, content, &sid); err != nil {
				return IndexResult{}, err
			}
		}
	}

	if written == 0 {
		for _, sec := range fallbackChunkSections(allSections, childrenByParent) {
			content := sectionTreeContent(sec, childrenByParent)
			if strings.TrimSpace(content) == "" {
				continue
			}
			sid := sec.ID
			if err := emitSectionChunks(sec, sectionCitation(sec, byID), buildPrefix(docNumber, docTitle, "", "", effDate, a.jur.EffectiveDateLabel), content, &sid); err != nil {
				return IndexResult{}, err
			}
		}
	}

	if tx != nil {
		if err := tx.Commit(ctx); err != nil {
			return IndexResult{}, fmt.Errorf("commit index transaction: %w", err)
		}
		tx = nil
	}

	// 7. Embed and upsert into gold.chunk_embedding.
	// Embedding is best-effort: a nil embedder or a batch error is logged and
	// skipped — Index never fails over embeddings, which can be backfilled.
	embedded := 0
	if a.embedder != nil && len(chunks) > 0 {
		embedded = a.embedChunks(ctx, chunks)
		log.Info("embedding complete",
			"doc", fd.ExternalID, "document_id", doc.ID, "embedded", embedded, "total", len(chunks))
	}

	_ = now // timestamp available for future use (heartbeat, etc.)
	log.Info("index complete",
		"doc", fd.ExternalID, "document_id", doc.ID, "chunks", written, "embedded", embedded,
		"dropped_dedup", droppedDedup, "dropped_short", droppedShort)
	return IndexResult{DocumentID: doc.ID, ChunksWritten: written}, nil
}

// embedChunks embeds all chunks in one batch and upserts the vectors into
// gold.chunk_embedding. Errors are logged and do not propagate — embeddings are
// supplementary and can be backfilled. Returns the number of embeddings written.
func (a *Activities) embedChunks(ctx context.Context, chunks []chunkRecord) int {
	log := a.log

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.text
	}

	model := a.embedder.Model()
	dims := int32(a.embedder.Dims()) //nolint:gosec
	written := 0
	for _, batch := range chunkRecordBatches(chunks, embedBatchSize) {
		texts = texts[:0]
		for _, c := range batch {
			texts = append(texts, c.text)
		}

		vecs, err := a.embedder.Embed(ctx, texts)
		if err != nil {
			log.Warn("embedding batch failed, skipping batch",
				"err", err, "chunks", len(batch))
			continue
		}
		if len(vecs) != len(batch) {
			log.Warn("embedder returned wrong number of vectors, skipping batch",
				"got", len(vecs), "want", len(batch))
			continue
		}

		for i, c := range batch {
			if vecs[i] == nil {
				log.Warn("nil vector for chunk, skipping", "chunk_id", c.id, "index", i)
				continue
			}
			if _, uerr := a.gold.UpsertChunkEmbedding(ctx, dbgold.UpsertChunkEmbeddingParams{
				ChunkID:   c.id,
				Model:     model,
				Dims:      dims,
				Embedding: pgvector.NewVector(vecs[i]),
			}); uerr != nil {
				log.Warn("upsert chunk_embedding failed, skipping",
					"chunk_id", c.id, "err", uerr)
				continue
			}
			written++
		}
	}
	return written
}

func chunkRecordBatches(chunks []chunkRecord, size int) [][]chunkRecord {
	if len(chunks) == 0 {
		return nil
	}
	if size <= 0 {
		size = embedBatchSize
	}
	out := make([][]chunkRecord, 0, (len(chunks)+size-1)/size)
	for start := 0; start < len(chunks); start += size {
		end := start + size
		if end > len(chunks) {
			end = len(chunks)
		}
		out = append(out, chunks[start:end])
	}
	return out
}

// buildPrefix assembles the deterministic contextual retrieval header that is
// prepended to each chunk before embedding. It follows the pattern:
//
//	[Số ký hiệu] [Tiêu đề]
//	[Chương heading] [Mục heading]
//	Có hiệu lực: [ngày/tháng/năm]
func buildPrefix(docNumber, title, chuong, muc, effDate, effLabel string) string {
	var parts []string
	title = capPrefixField(title)
	chuong = capPrefixField(chuong)
	muc = capPrefixField(muc)
	if docNumber != "" && title != "" {
		parts = append(parts, docNumber+": "+title)
	} else if title != "" {
		parts = append(parts, title)
	} else if docNumber != "" {
		parts = append(parts, docNumber)
	}
	if chuong != "" {
		parts = append(parts, chuong)
	}
	if muc != "" {
		parts = append(parts, muc)
	}
	if effDate != "" {
		parts = append(parts, effLabel+": "+effDate)
	}
	return strings.Join(parts, "\n")
}

func capPrefixField(s string) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) <= maxPrefixFieldRunes {
		return s
	}
	if maxPrefixFieldRunes <= 3 {
		return string(rs[:maxPrefixFieldRunes])
	}
	return strings.TrimSpace(string(rs[:maxPrefixFieldRunes-3])) + "..."
}

func buildChildrenByParent(all []dbsilver.SilverDocumentSection) map[int64][]*dbsilver.SilverDocumentSection {
	childrenByParent := make(map[int64][]*dbsilver.SilverDocumentSection)
	for i := range all {
		if all[i].ParentID == nil {
			continue
		}
		parentID := *all[i].ParentID
		childrenByParent[parentID] = append(childrenByParent[parentID], &all[i])
	}
	for parentID := range childrenByParent {
		sort.SliceStable(childrenByParent[parentID], func(i, j int) bool {
			left := childrenByParent[parentID][i]
			right := childrenByParent[parentID][j]
			if left.Ordinal == right.Ordinal {
				return left.ID < right.ID
			}
			return left.Ordinal < right.Ordinal
		})
	}
	return childrenByParent
}

func fallbackChunkSections(all []dbsilver.SilverDocumentSection, childrenByParent map[int64][]*dbsilver.SilverDocumentSection) []*dbsilver.SilverDocumentSection {
	var khoans []*dbsilver.SilverDocumentSection
	for i := range all {
		sec := &all[i]
		if sec.Kind == "khoan" && strings.TrimSpace(sectionTreeContent(sec, childrenByParent)) != "" {
			khoans = append(khoans, sec)
		}
	}
	if len(khoans) > 0 {
		return khoans
	}

	var leaves []*dbsilver.SilverDocumentSection
	for i := range all {
		sec := &all[i]
		if len(childrenByParent[sec.ID]) == 0 && strings.TrimSpace(sectionTreeContent(sec, childrenByParent)) != "" {
			leaves = append(leaves, sec)
		}
	}
	if len(leaves) > 0 {
		return leaves
	}

	var roots []*dbsilver.SilverDocumentSection
	for i := range all {
		sec := &all[i]
		if sec.ParentID == nil && strings.TrimSpace(sectionTreeContent(sec, childrenByParent)) != "" {
			roots = append(roots, sec)
		}
	}
	return roots
}

// enclosingPhuLuc returns the label of the appendix a section is nested under,
// or "" when the section belongs to the document's main body. When the appendix
// label is generic (no designator) and collides with siblings, the auto-ordinal
// is appended to disambiguate (e.g. "Phụ lục 3").
func enclosingPhuLuc(sec *dbsilver.SilverDocumentSection, byID map[int64]*dbsilver.SilverDocumentSection, collidingPhuLuc map[string]struct{}) string {
	for cur := sec; cur.ParentID != nil; {
		par := byID[*cur.ParentID]
		if par == nil {
			break
		}
		if par.Kind == "phuluc" || par.Kind == "schedule" || par.Kind == "lampiran" { // MY: Schedule / ID: Lampiran
			label := strings.TrimSpace(labelStr(par))
			if _, collides := collidingPhuLuc[label]; collides {
				label = label + " " + strconv.Itoa(int(par.Ordinal))
			}
			return label
		}
		cur = par
	}
	return ""
}

func sectionCitation(sec *dbsilver.SilverDocumentSection, byID map[int64]*dbsilver.SilverDocumentSection) string {
	chain := make([]*dbsilver.SilverDocumentSection, 0, 4)
	for cur := sec; cur != nil; {
		chain = append(chain, cur)
		if cur.ParentID == nil {
			break
		}
		cur = byID[*cur.ParentID]
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	parts := make([]string, 0, len(chain))
	for _, node := range chain {
		if part := sectionCitationPart(node); part != "" {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return labelStr(sec)
	}
	return strings.Join(parts, ", ")
}

func sectionCitationPart(sec *dbsilver.SilverDocumentSection) string {
	label := citationLabel(sec)
	if label == "" {
		return ""
	}
	lower := strings.ToLower(label)
	switch sec.Kind {
	case "chuong":
		base := label
		if !strings.HasPrefix(lower, "chương ") {
			base = "Chương " + label
		}
		if n := vnCitationPathDedupOrdinal(sec.CitationPath); n > 0 {
			return base + ", Đoạn " + strconv.Itoa(n)
		}
		return base
	case "muc":
		base := label
		if !strings.HasPrefix(lower, "mục ") {
			base = "Mục " + label
		}
		if n := vnCitationPathDedupOrdinal(sec.CitationPath); n > 0 {
			return base + ", Đoạn " + strconv.Itoa(n)
		}
		return base
	case "dieu":
		base := label
		if !strings.HasPrefix(lower, "điều ") {
			base = "Điều " + label
		}
		if n := vnCitationPathDedupOrdinal(sec.CitationPath); n > 0 {
			return base + ", Đoạn " + strconv.Itoa(n)
		}
		return base
	case "khoan":
		base := label
		if !strings.HasPrefix(lower, "khoản ") {
			base = "Khoản " + label
		}
		if n := vnCitationPathDedupOrdinal(sec.CitationPath); n > 0 {
			return base + ", Đoạn " + strconv.Itoa(n)
		}
		return base
	case "diem":
		base := label
		if !strings.HasPrefix(lower, "điểm ") {
			base = "Điểm " + label
		}
		if n := vnCitationPathDedupOrdinal(sec.CitationPath); n > 0 {
			return base + ", Đoạn " + strconv.Itoa(n)
		}
		return base
	case "part", "chapter", "section", "subsection", "paragraph", "schedule":
		// Malaysia/Singapore: labels are already citation-ready ("Section 5",
		// "(1)", "(a)") — return the raw label so balanced parens survive.
		// When a definition section restarts (a)/(b)/… per defined term, the
		// parser's uniqueSeg dedup produces citation_path segments like
		// "paragraph-a-2", "paragraph-a-3"; the trailing number disambiguates
		// sibling paragraphs that share the same label. Append "[N]" to keep
		// the human-readable citation unique and stable.
		raw := strings.TrimSpace(labelStr(sec))
		if n := citationPathDedupOrdinal(sec.CitationPath, sec); n > 0 {
			raw += " [" + strconv.Itoa(n) + "]"
		}
		return raw
	case "pasal":
		if strings.HasPrefix(lower, "pasal ") {
			return label
		}
		return "Pasal " + label
	case "ayat":
		if strings.HasPrefix(lower, "ayat (") {
			// The ID parser stores citation-ready labels ("ayat (1)");
			// citationLabel trimmed the closing paren, so return the raw
			// label to keep the parens balanced.
			return strings.TrimSpace(labelStr(sec))
		}
		return "ayat (" + label + ")"
	case "huruf":
		if strings.HasPrefix(lower, "huruf ") {
			return label
		}
		return "huruf " + label
	case "phuluc":
		// VN appendix: pass through label, with dedup disambiguation.
		raw := strings.TrimSpace(labelStr(sec))
		if n := vnCitationPathDedupOrdinal(sec.CitationPath); n > 0 {
			raw += ", Đoạn " + strconv.Itoa(n)
		}
		return raw
	case "bab", "bagian", "paragraf", "penjelasan", "lampiran":
		// Indonesian container/annex kinds: pass through the raw label.
		return strings.TrimSpace(labelStr(sec))
	default:
		return label
	}
}

func citationLabel(sec *dbsilver.SilverDocumentSection) string {
	label := strings.TrimSpace(labelStr(sec))
	label = strings.TrimRight(label, ".):")
	label = strings.TrimSpace(label)
	return label
}

// vnCitationPathDedupOrdinal extracts the dedup ordinal from the VN parser's
// uniqueCitationPath "~N" suffix. The VN parser appends "~2", "~3", … when
// siblings share the same base citation_path segment (e.g. amendment decrees
// where Điều 1 has multiple Khoản all labelled "1." produce "khoan-1",
// "khoan-1~2", …, "khoan-1~21"). Returns N (>=2), or 0 when there is no
// dedup suffix.
func vnCitationPathDedupOrdinal(path string) int {
	seg := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		seg = path[i+1:]
	}
	idx := strings.LastIndex(seg, "~")
	if idx < 0 {
		return 0
	}
	n, err := strconv.Atoi(seg[idx+1:])
	if err != nil || n < 2 {
		return 0
	}
	return n
}

// citationPathDedupOrdinal extracts the dedup ordinal from a citation_path's
// last segment for a MY/SG-family section. uniqueSeg (in malaysiaparse.go)
// appends "-2", "-3", … when siblings share the same base segment (e.g.
// definition sections that restart (a)/(b) per defined term produce
// "paragraph-a", "paragraph-a-2", …, "paragraph-a-24"). The function
// reconstructs the expected base segment from kind+label to distinguish a real
// dedup suffix ("-2" in "paragraph-a-2") from a legitimate label part ("-14" in
// "section-14"). Returns the suffix number (>=2), or 0 when there is no dedup.
func citationPathDedupOrdinal(path string, sec *dbsilver.SilverDocumentSection) int {
	seg := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		seg = path[i+1:]
	}
	// Reconstruct the base segment the parser would have produced before
	// uniqueSeg ran. MY parser uses "kind-{slug}" (paragraph-a, subsection-1,
	// section-14a, …). Schedules use slug(line) which is not kind-prefixed.
	kind := sec.Kind
	if kind == "schedule" {
		// Schedule segments are arbitrary slugs; dedup is theoretically
		// possible but rare. Not worth the fragile heuristic.
		return 0
	}
	label := strings.TrimSpace(labelStr(sec))
	// The parser stores labels like "(a)", "(1)", "Section 5", "Part IV".
	// The slug used in the path is the lowercased content inside parens
	// or after the kind word. Match what push() does:
	//   paragraph  -> "paragraph-" + tok            (tok = "a")
	//   subsection -> "subsection-" + lower(m[1])   (m[1] = "1")
	//   section    -> "section-" + lower(m[1])       (m[1] = "14A" -> "14a")
	//   part       -> "part-" + lower(m[1])          (m[1] = "IV" -> "iv")
	//   chapter    -> "chapter-" + m[1]              (m[1] = "1")
	// Subparagraphs use kind "paragraph" in the struct but "subparagraph-" prefix.
	prefix := kind + "-"
	if strings.HasPrefix(seg, "subparagraph-") {
		prefix = "subparagraph-"
	}
	if !strings.HasPrefix(seg, prefix) {
		return 0
	}
	rest := seg[len(prefix):]
	// rest is the label slug possibly followed by "-N" dedup. Derive the label
	// slug: strip parentheses and the kind word, lowercase.
	labelSlug := strings.ToLower(strings.NewReplacer("(", "", ")", "", " ", "-").Replace(label))
	// Strip "section-"/"part-"/etc. from the label slug if the label itself
	// included the kind word (e.g. "Section 5" -> "section-5" -> slug "5").
	labelSlug = strings.TrimPrefix(labelSlug, kind+"-")
	labelSlug = strings.TrimPrefix(labelSlug, "subparagraph-")

	if rest == labelSlug {
		return 0 // no dedup suffix
	}
	if !strings.HasPrefix(rest, labelSlug+"-") {
		return 0
	}
	tail := rest[len(labelSlug)+1:]
	n, err := strconv.Atoi(tail)
	if err != nil || n < 2 {
		return 0
	}
	return n
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func labelOnlyChunk(sec *dbsilver.SilverDocumentSection, citation, content string) bool {
	content = normalizeChunkLabel(content)
	if content == "" {
		return true
	}
	candidates := []string{
		labelStr(sec),
		sectionCitationPart(sec),
		citation,
	}
	// When a section has a heading, the first split part of sectionOwnText is
	// "Label. Heading" (or "Label Heading") — a label-only fragment that carries
	// no legal body. Include both punctuated and bare forms so the guard catches
	// heading orphans across all jurisdictions.
	if sec.Heading != nil && *sec.Heading != "" {
		label := strings.TrimSpace(labelStr(sec))
		heading := strings.TrimSpace(*sec.Heading)
		candidates = append(candidates, label+". "+heading, label+" "+heading)
	}
	for _, candidate := range candidates {
		if content == normalizeChunkLabel(candidate) {
			return true
		}
	}
	return false
}

func normalizeChunkLabel(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	s = strings.Trim(s, " .:;,)(")
	return s
}

// structuredChildren returns the legal sub-provisions a section is split into when
// it is too long for one chunk: Khoản under a Điều, Điểm under a Khoản. Lower levels
// (Điểm and below) have no structured split and fall back to Đoạn paragraph-splitting.
func structuredChildren(sec *dbsilver.SilverDocumentSection, childrenByParent map[int64][]*dbsilver.SilverDocumentSection) []*dbsilver.SilverDocumentSection {
	var want string
	switch sec.Kind {
	case "dieu":
		want = "khoan"
	case "khoan":
		want = "diem"
	case "section": // MY/SG: Section → Subsection; TH/ID: Section → Paragraph
		want = "subsection"
		// Thai and ID sections have "paragraph" children, not "subsection".
		// Check for subsection first; if none, try paragraph below.
	case "subsection": // Malaysia: Subsection split into Paragraphs
		want = "paragraph"
	case "pasal": // Indonesia: Pasal split into Ayat
		want = "ayat"
	case "ayat": // Indonesia: Ayat split into Huruf
		want = "huruf"
	default:
		return nil
	}
	var out []*dbsilver.SilverDocumentSection
	for _, c := range childrenByParent[sec.ID] {
		if c.Kind == want {
			out = append(out, c)
		}
	}
	// TH/ID: sections may use "paragraph" children instead of "subsection".
	if len(out) == 0 && want == "subsection" {
		for _, c := range childrenByParent[sec.ID] {
			if c.Kind == "paragraph" {
				out = append(out, c)
			}
		}
	}
	return out
}

func sectionTreeContent(sec *dbsilver.SilverDocumentSection, childrenByParent map[int64][]*dbsilver.SilverDocumentSection) string {
	lines := make([]string, 0, 1+len(childrenByParent[sec.ID]))
	if own := sectionOwnText(sec); own != "" {
		lines = append(lines, own)
	}
	for _, child := range childrenByParent[sec.ID] {
		if childText := sectionTreeContent(child, childrenByParent); childText != "" {
			lines = append(lines, childText)
		}
	}
	return strings.Join(lines, "\n")
}

func sectionOwnText(sec *dbsilver.SilverDocumentSection) string {
	label := strings.TrimSpace(labelStr(sec))
	heading := ""
	if sec.Heading != nil {
		heading = strings.TrimSpace(*sec.Heading)
	}
	content := strings.TrimSpace(contentStr(sec))
	if label == "Toàn văn" && content != "" {
		return content
	}
	switch {
	case label != "" && heading != "" && content != "":
		return label + ". " + heading + "\n" + content
	case label != "" && heading != "":
		return label + ". " + heading
	case label != "" && content != "":
		return label + " " + content
	case content != "":
		return content
	default:
		return label
	}
}

func splitLongChunkContent(content string, maxTokens int) []string {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if maxTokens <= 0 || roughTokenCount(content) <= maxTokens {
		return []string{content}
	}

	var parts []string
	current := ""
	flush := func() {
		current = strings.TrimSpace(current)
		if current != "" {
			parts = append(parts, current)
			current = ""
		}
	}

	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if roughTokenCount(line) > maxTokens {
			flush()
			parts = append(parts, splitLongTextByWords(line, maxTokens)...)
			continue
		}
		next := line
		if current != "" {
			next = current + "\n" + line
		}
		if current != "" && roughTokenCount(next) > maxTokens {
			flush()
			current = line
			continue
		}
		current = next
	}
	flush()
	return parts
}

func splitLongTextByWords(text string, maxTokens int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var parts []string
	current := ""
	for _, word := range words {
		next := word
		if current != "" {
			next = current + " " + word
		}
		if current != "" && roughTokenCount(next) > maxTokens {
			parts = append(parts, current)
			current = word
			continue
		}
		current = next
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

// labelStr returns the section label, or its citation_path as fallback.
func labelStr(s *dbsilver.SilverDocumentSection) string {
	if s.Label != nil && *s.Label != "" {
		return *s.Label
	}
	// Synthesize a label from citation_path last segment.
	parts := strings.Split(s.CitationPath, "/")
	return parts[len(parts)-1]
}

// contentStr returns the section content, or an empty string.
func contentStr(s *dbsilver.SilverDocumentSection) string {
	if s.Content == nil {
		return ""
	}
	return *s.Content
}

// khoanContent builds the text for a Khoản chunk: heading + content body.
func khoanContent(k *dbsilver.SilverDocumentSection) string {
	label := labelStr(k)
	content := contentStr(k)
	if content == "" {
		return label
	}
	return label + " " + content
}

// roughTokenCount estimates the token count for a string using a simple
// rune-based approximation: Vietnamese text averages ~2 runes/token in BPE;
// ASCII averages ~4 chars/token. We use rune_count/2 as a cheap estimate.
// This is deliberately rough — the field is advisory for the retrieval ranker.
func roughTokenCount(s string) int {
	n := utf8.RuneCountInString(s)
	if n == 0 {
		return 0
	}
	est := n / 2
	if est == 0 {
		est = 1
	}
	return est
}

// buildCollidingDieuCitations scans the section list for article-level and
// appendix kinds whose sectionCitationPart would collide within the document —
// i.e. multiple sections produce the same base citation string. Returns a set
// of the colliding citation strings. This is used to decide whether the
// enclosing Chương/Mục must be prepended (for dieu) or an ordinal appended
// (for phuluc) to disambiguate.
func buildCollidingDieuCitations(secs []dbsilver.SilverDocumentSection) map[string]struct{} {
	counts := make(map[string]int)
	for i := range secs {
		switch secs[i].Kind {
		case "dieu", "section", "pasal", "phuluc", "schedule", "lampiran":
			cite := sectionCitationPart(&secs[i])
			counts[cite]++
		}
	}
	colliding := make(map[string]struct{})
	for cite, n := range counts {
		if n > 1 {
			colliding[cite] = struct{}{}
		}
	}
	return colliding
}

// relationContextOnly reports whether doc exists only through relation backfill
// (every ledger observation has provenance='relation') and falls outside the
// configured scope vocabulary. Missing ledger rows or an empty scope vocabulary
// never demote — fail open and index.
func (a *Activities) relationContextOnly(ctx context.Context, doc dbsilver.SilverDocument) (bool, error) {
	if a.dbpool == nil {
		return false, nil
	}
	const q = `
SELECT
    count(*),
    COALESCE(bool_or(fd.provenance <> 'relation'), false)
FROM silver.document_alias da
JOIN ingest.fetch_doc fd
  ON fd.source = da.source
 AND fd.external_id = da.external_id
WHERE da.document_id = $1`
	var observations int64
	var hasPrimary bool
	if err := a.dbpool.QueryRow(ctx, q, doc.ID).Scan(&observations, &hasPrimary); err != nil {
		return false, fmt.Errorf("document provenance doc=%d: %w", doc.ID, err)
	}
	if observations == 0 || hasPrimary {
		return false, nil
	}
	matcher, err := a.loadMatcher(ctx)
	if err != nil {
		return false, fmt.Errorf("load scope matcher: %w", err)
	}
	if matcher.Empty() {
		return false, nil
	}
	num, title := nullableString(doc.DocNumber), nullableString(doc.Title)
	res := matcher.Match(num, title, "")
	if res.InScope {
		return false, nil
	}
	// Rescue: source titles sometimes have partial diacritic errors (e.g. vbpl.vn
	// "dung" instead of "dùng"). Try folded matching before demoting.
	res = matcher.MatchFolded(num, title, "")
	return !res.InScope, nil
}
