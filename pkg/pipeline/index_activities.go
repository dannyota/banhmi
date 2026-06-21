package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	pgvector "github.com/pgvector/pgvector-go"
	"go.temporal.io/sdk/activity"

	dbbronze "danny.vn/banhmi/pkg/store/bronze"
	dbgold "danny.vn/banhmi/pkg/store/gold"
	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// maxDieuTokens is the rough token threshold above which a Điều is split into
// per-Khoản chunks instead of one monolithic chunk. The same ceiling is also
// applied to emitted chunks so very long Khoản text is split into deterministic
// paragraph shards.
const maxDieuTokens = 512

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
	log := activity.GetLogger(ctx)
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
		for cur.ParentID != nil {
			par := byID[*cur.ParentID]
			if par == nil {
				break
			}
			switch par.Kind {
			case "chuong", "part": // Malaysia: Part fills the top container slot
				if chuong == "" {
					if par.Heading != nil {
						chuong = labelStr(par) + " " + *par.Heading
					} else {
						chuong = labelStr(par)
					}
				}
			case "muc", "chapter": // Malaysia: Chapter fills the sub-container slot
				if muc == "" {
					if par.Heading != nil {
						muc = labelStr(par) + " " + *par.Heading
					} else {
						muc = labelStr(par)
					}
				}
			}
			cur = par
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

	// 6. Chunk each Điều.
	// Collect Khoản children for each Điều by citation_path prefix.
	// Sections are ordered; we iterate and emit chunks in ordinal order.
	ordinal := 0
	written := 0

	var chunks []chunkRecord

	emitChunk := func(sec *dbsilver.SilverDocumentSection, citation, prefix, content string, sectionID *int64) error {
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
	// "Paragraph N" (other jurisdictions, e.g. Malaysia's English corpus).
	paraWord := "Đoạn"
	if a.jurisdiction != "vn" {
		paraWord = "Paragraph"
	}
	emitSectionChunks := func(sec *dbsilver.SilverDocumentSection, citation, prefix, content string, sectionID *int64) error {
		if labelOnlyChunk(sec, citation, content) {
			return nil
		}
		parts := splitLongChunkContent(content, maxDieuTokens)
		if len(parts) == 0 {
			return nil
		}
		if len(parts) == 1 {
			return emitChunk(sec, citation, prefix, parts[0], sectionID)
		}
		for i, part := range parts {
			partCitation := fmt.Sprintf("%s, %s %d", citation, paraWord, i+1)
			if err := emitChunk(sec, partCitation, prefix, part, sectionID); err != nil {
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
			childLead = strings.TrimSpace(childLead + "\n" + own)
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
		case "dieu", "section": // Malaysia: Section is the article-level chunk unit
			chuong, muc := enclosing(sec)
			basePrefix := buildPrefix(docNumber, docTitle, chuong, muc, effDate)
			citation := sectionCitationPart(sec)
			// An Điều nested in an appendix (a Quy chế/Quy định "ban hành kèm
			// theo") cites its Phụ lục so it cannot be confused with the
			// enacting document's own Điều of the same number.
			if pl := enclosingPhuLuc(sec, byID); pl != "" {
				citation = pl + ", " + citation
			}
			if err := emitProvisionChunks(sec, citation, basePrefix, ""); err != nil {
				return IndexResult{}, err
			}
		case "phuluc", "schedule": // Malaysia: Schedule is the appendix-equivalent
			// The appendix's own text (tables, forms, thresholds — anything not
			// under a nested Điều) is real legal substance; chunk it under the
			// "Phụ lục N" citation. Nested Điều are walked by the case above.
			content := strings.TrimSpace(sectionOwnText(sec))
			if content == "" {
				continue
			}
			chuong, muc := enclosing(sec)
			basePrefix := buildPrefix(docNumber, docTitle, chuong, muc, effDate)
			sid := sec.ID
			if err := emitSectionChunks(sec, sectionCitationPart(sec), basePrefix, content, &sid); err != nil {
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
			if err := emitSectionChunks(sec, sectionCitation(sec, byID), buildPrefix(docNumber, docTitle, "", "", effDate), content, &sid); err != nil {
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
		"doc", fd.ExternalID, "document_id", doc.ID, "chunks", written, "embedded", embedded)
	return IndexResult{DocumentID: doc.ID, ChunksWritten: written}, nil
}

// embedChunks embeds all chunks in one batch and upserts the vectors into
// gold.chunk_embedding. Errors are logged and do not propagate — embeddings are
// supplementary and can be backfilled. Returns the number of embeddings written.
func (a *Activities) embedChunks(ctx context.Context, chunks []chunkRecord) int {
	log := activity.GetLogger(ctx)

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
func buildPrefix(docNumber, title, chuong, muc, effDate string) string {
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
		parts = append(parts, "Có hiệu lực: "+effDate)
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
// or "" when the section belongs to the document's main body.
func enclosingPhuLuc(sec *dbsilver.SilverDocumentSection, byID map[int64]*dbsilver.SilverDocumentSection) string {
	for cur := sec; cur.ParentID != nil; {
		par := byID[*cur.ParentID]
		if par == nil {
			break
		}
		if par.Kind == "phuluc" || par.Kind == "schedule" { // Malaysia: Schedule
			return strings.TrimSpace(labelStr(par))
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
		if strings.HasPrefix(lower, "chương ") {
			return label
		}
		return "Chương " + label
	case "muc":
		if strings.HasPrefix(lower, "mục ") {
			return label
		}
		return "Mục " + label
	case "dieu":
		if strings.HasPrefix(lower, "điều ") {
			return label
		}
		return "Điều " + label
	case "khoan":
		if strings.HasPrefix(lower, "khoản ") {
			return label
		}
		return "Khoản " + label
	case "diem":
		if strings.HasPrefix(lower, "điểm ") {
			return label
		}
		return "Điểm " + label
	case "part", "chapter", "section", "subsection", "paragraph", "schedule":
		// Malaysia: labels are already citation-ready ("Section 5", "(1)",
		// "(a)") — return the raw label so balanced parens survive.
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
	for _, candidate := range []string{
		labelStr(sec),
		sectionCitationPart(sec),
		citation,
	} {
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
	case "section": // Malaysia: Section split into Subsections
		want = "subsection"
	case "subsection": // Malaysia: Subsection split into Paragraphs
		want = "paragraph"
	default:
		return nil
	}
	var out []*dbsilver.SilverDocumentSection
	for _, c := range childrenByParent[sec.ID] {
		if c.Kind == want {
			out = append(out, c)
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
	res := matcher.Match(nullableString(doc.DocNumber), nullableString(doc.Title), "")
	return !res.InScope, nil
}
