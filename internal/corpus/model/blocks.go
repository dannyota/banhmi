package model

import (
	"fmt"
	"unicode/utf8"
)

// ByteRange uses half-open UTF-8 byte offsets for text, never rune offsets.
type ByteRange struct {
	Start int64
	End   int64
}

// Validate requires nonnegative, nonempty half-open byte offsets.
func (r ByteRange) Validate() error {
	if r.Start < 0 || r.End <= r.Start {
		return fmt.Errorf("byte range requires 0 <= start < end")
	}
	return nil
}

// CoordinateSpace names the artifact coordinate system for a span.
type CoordinateSpace string

const (
	OriginalBytes  CoordinateSpace = "original_bytes"
	DecodedMember  CoordinateSpace = "decoded_package_member"
	RawText        CoordinateSpace = "raw_converter_text"
	NormalizedText CoordinateSpace = "normalized_text"
)

// Valid reports membership in the closed contract vocabulary.
func (s CoordinateSpace) Valid() bool {
	return s == OriginalBytes || s == DecodedMember || s == RawText || s == NormalizedText
}

// Span permits unavailable byte mappings only with an explicit reason and a
// usable page, object or member anchor. Decoded member offsets are not ZIP offsets.
type Span struct {
	ArtifactHash Digest
	BlockID      string
	Space        CoordinateSpace
	Range        *ByteRange
	Page         *int
	Object       string
	Member       string
	TokenOrdinal *int
	ReasonCode   string
}

// Validate checks coordinate bounds and requires an anchor for unavailable byte mappings.
func (s Span) Validate() error {
	if err := s.ArtifactHash.Validate(); err != nil {
		return fmt.Errorf("ArtifactHash: %w", err)
	}
	if !s.Space.Valid() || (s.Page != nil && *s.Page < 1) || (s.TokenOrdinal != nil && *s.TokenOrdinal < 0) {
		return fmt.Errorf("invalid span coordinate or ordinal")
	}
	if s.Range != nil {
		if err := s.Range.Validate(); err != nil {
			return fmt.Errorf("s.Range: %w", err)
		}
		return nil
	}
	if s.ReasonCode != "BYTE_MAPPING_UNAVAILABLE" || (s.Page == nil && s.Object == "" && s.Member == "") {
		return fmt.Errorf("unavailable byte mapping requires reason and usable anchor")
	}
	return nil
}

// BlockKind names the supported converted block kinds.
type BlockKind string

const (
	BlockHeading   BlockKind = "heading"
	BlockParagraph BlockKind = "paragraph"
	BlockListItem  BlockKind = "list_item"
	BlockTable     BlockKind = "table"
	BlockImage     BlockKind = "image"
	BlockPageBreak BlockKind = "page_break"
)

// Valid reports membership in the closed contract vocabulary.
func (k BlockKind) Valid() bool {
	return k == BlockHeading || k == BlockParagraph || k == BlockListItem || k == BlockTable || k == BlockImage || k == BlockPageBreak
}

// Authority records binding, non-binding or unknown evidence authority.
type Authority string

const (
	AuthorityBinding    Authority = "binding"
	AuthorityNonBinding Authority = "non_binding"
	AuthorityUnknown    Authority = "unknown"
)

// Valid reports membership in the closed contract vocabulary.
func (a Authority) Valid() bool {
	return a == AuthorityBinding || a == AuthorityNonBinding || a == AuthorityUnknown
}

// Block retains exact raw text; normalized text is NFC by contract, without
// normalization in Validate. Large table, image and style content is referenced.
type Block struct {
	ID             string
	ParentID       string
	Ordinal        int
	Kind           BlockKind
	RawText        string
	NormalizedText AssetRef
	Language       string
	Authority      Authority
	SourceSpans    []Span
	Content        *AssetRef
	Findings       []Finding
}

// Validate checks text encoding, identity, ordering, authority and source provenance.
func (b Block) Validate() error {
	if !utf8.ValidString(b.RawText) {
		return fmt.Errorf("block text must be UTF-8")
	}
	if b.ID == "" || b.ParentID == b.ID || b.Ordinal < 0 || !b.Kind.Valid() || !b.Authority.Valid() || b.Language == "" || len(b.SourceSpans) == 0 {
		return fmt.Errorf("invalid block identity, kind, authority or provenance")
	}
	if err := b.NormalizedText.Validate(); err != nil {
		return fmt.Errorf("NormalizedText: %w", err)
	}
	for _, s := range b.SourceSpans {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("source span: %w", err)
		}
	}
	if b.Content != nil {
		if err := b.Content.Validate(); err != nil {
			return fmt.Errorf("Content: %w", err)
		}
	}
	if err := validateFindings(b.Findings); err != nil {
		return fmt.Errorf("findings: %w", err)
	}
	return nil
}

// OffsetMapSegment supports ordered many-to-many transformations. Full maps
// remain hashed artifacts; artifact-size checks belong to output validation.
type OffsetMapSegment struct {
	Sources      []Span
	Destinations []Span
	Transform    string
	Ordinal      int
}

// Validate requires both mapping sides, provenance and transform order.
func (m OffsetMapSegment) Validate() error {
	if len(m.Sources) == 0 || len(m.Destinations) == 0 || m.Transform == "" || m.Ordinal < 0 {
		return fmt.Errorf("map segment requires both sides and transform order")
	}
	for _, spans := range [][]Span{m.Sources, m.Destinations} {
		for _, s := range spans {
			if err := s.Validate(); err != nil {
				return fmt.Errorf("source span: %w", err)
			}
		}
	}
	return nil
}

// EditKind names a reversible text transformation operation.
type EditKind string

const (
	EditDecode    EditKind = "decode"
	EditNormalize EditKind = "normalize"
	EditReplace   EditKind = "replace"
	EditInsert    EditKind = "insert"
	EditDelete    EditKind = "delete"
)

// Valid reports membership in the closed contract vocabulary.
func (k EditKind) Valid() bool {
	return k == EditDecode || k == EditNormalize || k == EditReplace || k == EditInsert || k == EditDelete
}

// EditLedgerEntry references removed/replacement bytes rather than embedding
// them. Insert/delete have an absent side, never an invalid empty byte range.
type EditLedgerEntry struct {
	Kind        EditKind
	Ordinal     int
	Rule        string
	Version     string
	Inputs      []Span
	Outputs     []Span
	Removed     *AssetRef
	Replacement *AssetRef
	Evidence    AssetRef
}

// Validate checks the applicable edit sides, operation and staged evidence.
func (e EditLedgerEntry) Validate() error {
	if !e.Kind.Valid() || e.Ordinal < 0 || e.Rule == "" || e.Version == "" {
		return fmt.Errorf("invalid edit operation or rule")
	}
	if (e.Kind != EditInsert && len(e.Inputs) == 0) || (e.Kind != EditDelete && len(e.Outputs) == 0) || (len(e.Inputs) == 0 && len(e.Outputs) == 0) {
		return fmt.Errorf("edit requires applicable input and output spans")
	}
	for _, spans := range [][]Span{e.Inputs, e.Outputs} {
		for _, s := range spans {
			if err := s.Validate(); err != nil {
				return fmt.Errorf("source span: %w", err)
			}
		}
	}
	for _, a := range []*AssetRef{e.Removed, e.Replacement} {
		if a != nil {
			if err := a.Validate(); err != nil {
				return fmt.Errorf("asset reference: %w", err)
			}
		}
	}
	if err := e.Evidence.Validate(); err != nil {
		return fmt.Errorf("e.Evidence: %w", err)
	}
	return nil
}

// RuleLimits is a frozen local bound resolved by the owning consumer from policy.
// Country rules cannot increase it or obtain storage and transport clients.
type RuleLimits struct {
	MaxItems int
	MaxBytes int64
}

// Validate rejects unbounded rule inputs.
func (l RuleLimits) Validate() error {
	if l.MaxItems <= 0 || l.MaxBytes <= 0 {
		return fmt.Errorf("rule limits must be positive")
	}
	return nil
}

// DecodedBlock supplies verified raw and normalized values to pure block rules.
// Spans are provenance, never instructions to resolve content inside a rule.
type DecodedBlock struct {
	ID             string
	ParentID       string
	Kind           BlockKind
	RawText        string
	NormalizedText string
	Language       string
	Authority      Authority
	SourceSpans    []Span
}

// DecodedDocument contains bounded decoded blocks, not asset locators.
type DecodedDocument struct {
	OriginalHash    Digest
	CandidateDigest Digest
	Blocks          []DecodedBlock
	Limits          RuleLimits
}

// Validate checks decoded values before a pure rule call.
func (d DecodedDocument) Validate() error {
	if err := d.Limits.Validate(); err != nil {
		return fmt.Errorf("limits: %w", err)
	}
	if len(d.Blocks) > d.Limits.MaxItems {
		return fmt.Errorf("decoded blocks exceed frozen bound")
	}
	for name, hash := range map[string]Digest{"original": d.OriginalHash, "candidate": d.CandidateDigest} {
		if err := hash.Validate(); err != nil {
			return fmt.Errorf("%s hash: %w", name, err)
		}
	}
	remaining := d.Limits.MaxBytes
	remainingSpans := d.Limits.MaxItems
	seen := make(map[string]bool, len(d.Blocks))
	for i, b := range d.Blocks {
		if b.ID == "" || seen[b.ID] || !b.Kind.Valid() || !b.Authority.Valid() || b.Language == "" || len(b.SourceSpans) == 0 {
			return fmt.Errorf("decoded block %d requires identity and provenance", i)
		}
		seen[b.ID] = true
		for _, value := range []string{b.ID, b.ParentID, b.RawText, b.NormalizedText, b.Language} {
			if !utf8.ValidString(value) || int64(len(value)) > remaining {
				return fmt.Errorf("decoded block %d exceeds text bound or has invalid UTF-8", i)
			}
			remaining -= int64(len(value))
		}
		if len(b.SourceSpans) > remainingSpans {
			return fmt.Errorf("decoded source spans exceed frozen item bound")
		}
		remainingSpans -= len(b.SourceSpans)
		for j, span := range b.SourceSpans {
			if err := span.Validate(); err != nil {
				return fmt.Errorf("block %d span %d: %w", i, j, err)
			}
		}
	}
	return nil
}

// TextInput is verified UTF-8 text with exact provenance for pure normalization.
type TextInput struct {
	Text    string
	Sources []Span
	Limits  RuleLimits
}

// Validate checks the text byte bound and provenance before normalization.
func (i TextInput) Validate() error {
	if err := i.Limits.Validate(); err != nil {
		return fmt.Errorf("limits: %w", err)
	}
	if !utf8.ValidString(i.Text) || int64(len(i.Text)) > i.Limits.MaxBytes || len(i.Sources) == 0 || len(i.Sources) > i.Limits.MaxItems {
		return fmt.Errorf("text input requires bounded UTF-8 and source spans")
	}
	for n, span := range i.Sources {
		if err := span.Validate(); err != nil {
			return fmt.Errorf("source span %d: %w", n, err)
		}
	}
	return nil
}

// DecodedEdit returns changed bytes to the caller that stages the edit ledger.
type DecodedEdit struct {
	Kind        EditKind
	Rule        string
	Version     string
	Inputs      []Span
	Outputs     []Span
	Removed     string
	Replacement string
}

// MappedValue returns computed text, offset maps and reversible edits locally.
// The owning consumer validates and stages these values to obtain MappedText refs.
type MappedValue struct {
	Text       string
	OffsetMap  []OffsetMapSegment
	EditLedger []DecodedEdit
}

// SourceNode is a decoded native source-tree node and its exact provenance.
type SourceNode struct {
	ID       string
	ParentID string
	Label    string
	BlockIDs []string
	Sources  []Span
}

// SourceTree is a bounded decoded tree for pure structure rules.
type SourceTree struct {
	Nodes  []SourceNode
	Limits RuleLimits
}

// Validate bounds decoded tree values before pure structure calls.
func (t SourceTree) Validate() error {
	if err := t.Limits.Validate(); err != nil {
		return fmt.Errorf("limits: %w", err)
	}
	if len(t.Nodes) > t.Limits.MaxItems {
		return fmt.Errorf("source tree exceeds frozen node bound")
	}
	remaining := t.Limits.MaxBytes
	for i, node := range t.Nodes {
		if node.ID == "" || len(node.Sources) == 0 || len(node.Sources) > t.Limits.MaxItems || len(node.BlockIDs) > t.Limits.MaxItems {
			return fmt.Errorf("source node %d requires bounded identity and provenance", i)
		}
		values := []string{node.ID, node.ParentID, node.Label}
		values = append(values, node.BlockIDs...)
		for _, value := range values {
			if !utf8.ValidString(value) || int64(len(value)) > remaining {
				return fmt.Errorf("source node %d exceeds byte bound or has invalid UTF-8", i)
			}
			remaining -= int64(len(value))
		}
		for j, span := range node.Sources {
			if err := span.Validate(); err != nil {
				return fmt.Errorf("source node %d span %d: %w", i, j, err)
			}
		}
	}
	return nil
}

// BodyValue returns computed tree nodes to the caller responsible for staging.
type BodyValue struct {
	Nodes           []SourceNode
	CandidateDigest Digest
}
