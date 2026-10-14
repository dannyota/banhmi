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

func (r ByteRange) Validate() error {
	if r.Start < 0 || r.End <= r.Start {
		return fmt.Errorf("byte range requires 0 <= start < end")
	}
	return nil
}

type CoordinateSpace string

const (
	OriginalBytes CoordinateSpace = "original_bytes"
	DecodedMember CoordinateSpace = "decoded_package_member"
	RawText       CoordinateSpace = "raw_converter_text"
	NormalizedText CoordinateSpace = "normalized_text"
)

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

func (s Span) Validate() error {
	if err := s.ArtifactHash.Validate(); err != nil {
		return err
	}
	if !s.Space.Valid() || (s.Page != nil && *s.Page < 1) || (s.TokenOrdinal != nil && *s.TokenOrdinal < 0) {
		return fmt.Errorf("invalid span coordinate or ordinal")
	}
	if s.Range != nil {
		return s.Range.Validate()
	}
	if s.ReasonCode != "BYTE_MAPPING_UNAVAILABLE" || (s.Page == nil && s.Object == "" && s.Member == "") {
		return fmt.Errorf("unavailable byte mapping requires reason and usable anchor")
	}
	return nil
}

type BlockKind string

const (
	BlockHeading   BlockKind = "heading"
	BlockParagraph BlockKind = "paragraph"
	BlockListItem  BlockKind = "list_item"
	BlockTable     BlockKind = "table"
	BlockImage     BlockKind = "image"
	BlockPageBreak BlockKind = "page_break"
)

func (k BlockKind) Valid() bool {
	return k == BlockHeading || k == BlockParagraph || k == BlockListItem || k == BlockTable || k == BlockImage || k == BlockPageBreak
}

type Authority string

const (
	AuthorityBinding    Authority = "binding"
	AuthorityNonBinding Authority = "non_binding"
	AuthorityUnknown    Authority = "unknown"
)

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

func (b Block) Validate() error {
	if !utf8.ValidString(b.RawText) {
		return fmt.Errorf("block text must be UTF-8")
	}
	if b.ID == "" || b.ParentID == b.ID || b.Ordinal < 0 || !b.Kind.Valid() || !b.Authority.Valid() || b.Language == "" || len(b.SourceSpans) == 0 {
		return fmt.Errorf("invalid block identity, kind, authority or provenance")
	}
	if err := b.NormalizedText.Validate(); err != nil {
		return err
	}
	for _, s := range b.SourceSpans {
		if err := s.Validate(); err != nil {
			return err
		}
	}
	if b.Content != nil {
		if err := b.Content.Validate(); err != nil {
			return err
		}
	}
	return validateFindings(b.Findings)
}

// OffsetMapSegment supports ordered many-to-many transformations. Full maps
// remain hashed artifacts; artifact-size checks belong to output validation.
type OffsetMapSegment struct {
	Sources      []Span
	Destinations []Span
	Transform    string
	Ordinal      int
}

func (m OffsetMapSegment) Validate() error {
	if len(m.Sources) == 0 || len(m.Destinations) == 0 || m.Transform == "" || m.Ordinal < 0 {
		return fmt.Errorf("map segment requires both sides and transform order")
	}
	for _, spans := range [][]Span{m.Sources, m.Destinations} {
		for _, s := range spans {
			if err := s.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

type EditKind string

const (
	EditDecode    EditKind = "decode"
	EditNormalize EditKind = "normalize"
	EditReplace   EditKind = "replace"
	EditInsert    EditKind = "insert"
	EditDelete    EditKind = "delete"
)

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
				return err
			}
		}
	}
	for _, a := range []*AssetRef{e.Removed, e.Replacement} {
		if a != nil {
			if err := a.Validate(); err != nil {
				return err
			}
		}
	}
	return e.Evidence.Validate()
}
