// Package ingest defines the Bronze-layer source contract shared by every
// crawler under pkg/ingest/{source}. A Source discovers newly published
// documents from a newest-first feed, fetches a document's server-rendered
// detail (metadata + downloadable file references or inline HTML body), and
// downloads the raw files. Each source package is self-contained; this file
// holds only the cross-source types.
package ingest

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// DocType is a Vietnamese legal document type (loại văn bản), e.g. "Thông tư",
// "Nghị định", "Quyết định", "Văn bản hợp nhất".
type DocType string

// FileRef points to a downloadable raw file (PDF/DOCX/DOC) attached to a document.
// URL is the absolute download URL as scraped from the source; the bytes are
// retrieved lazily via Source.Download so discovery stays cheap.
type FileRef struct {
	URL      string // absolute download URL (e.g. the CDN stream link)
	Name     string // file name as advertised by the source
	Ext      string // lowercase extension without the dot: "pdf", "docx", "doc"
	Kind     string // bronze.raw_file role: main, appendix, original_scan, attachment
	MIMEType string // best-effort content type, may be empty until downloaded
}

// Relation is an edge in a document's amendment/replacement graph, populated
// only when the source exposes relations. Type uses the source's own label
// (normalized later); Target identifies the related document by số ký hiệu
// and/or the source's external id.
type Relation struct {
	Type         string `json:"type"`          // e.g. "amends", "replaces", "guides" (source label)
	TypeRaw      int    `json:"type_raw"`      // source relation code when the source exposes one (0 = unknown)
	TargetNumber string `json:"target_number"` // số ký hiệu of the related document, when known
	TargetID     string `json:"target_id"`     // source external id of the related document, when known
	TargetTitle  string `json:"target_title"`  // target title, when known
	TargetURL    string `json:"target_url"`    // detail URL of the related document, when known
}

// DiscoveredDoc is one document observed from a source. Discovery populates the
// fields cheaply available from the feed; FetchDetail enriches it from the
// detail page. Some sources serve the body inline as HTML (vbpl/phapluat),
// others only as downloadable Files (congbao).
type DiscoveredDoc struct {
	SourceID    string     // "congbao" | "vbpl" | "sbv_hanoi" | "phapluat"
	ExternalID  string     // site id or UUID
	DocGUID     string     // cross-source opaque id when the source exposes one
	Number      string     // số ký hiệu, e.g. "11/2026/TT-NHNN"
	Title       string     // trích yếu
	Abstract    string     // vbpl docAbs: body/preamble text from the feed, used for scope matching (may be empty)
	DocType     DocType    // loại văn bản
	DocTypeCode string     // source code for loại văn bản, when known
	Issuer      string     // cơ quan ban hành
	IssuerCode  string     // source code for cơ quan ban hành, when known
	Signer      string     // người ký
	IssuedAt    time.Time  // ngày ban hành
	EffectiveAt time.Time  // ngày hiệu lực (zero when the source omits it)
	ExpireAt    time.Time  // ngày hết hiệu lực (zero when the source omits it)
	Status      string     // tình trạng hiệu lực (CHL, HHL, ...); empty if unknown
	DetailURL   string     // canonical human/detail URL
	HTML        string     // inline body when the source serves it
	Files       []FileRef  // downloadable PDF/DOCX/DOC (congbao/vbpl; attachments)
	Relations   []Relation // amends/replaces/... when the source exposes them
	HasContent  bool       // first-party content flag when the source exposes one

	// IsConsolidated marks VBHN/consolidated documents when the source exposes the
	// flag or the document type/number makes it deterministic.
	IsConsolidated bool

	// PublishedAt is the feed timestamp used as the discovery watermark
	// (congbao RSS <pubDate>). It may differ from IssuedAt.
	PublishedAt time.Time

	// Gazette metadata (congbao): số công báo and its publish date.
	GazetteNumber string
	GazetteDate   time.Time

	// RawMeta is the source's raw record for this document as returned by the feed
	// (e.g. one vbpl doc/all item), persisted verbatim to bronze.source_document
	// (raw_meta JSONB). It preserves fields not mapped to typed columns — docAbs,
	// effFrom/effTo, documentRelatedList — for audit and offline scope re-tuning.
	RawMeta json.RawMessage
}

// DetailRef identifies a document for the heavier detail/enrichment fetch.
// ExternalID is the source API identity discovered from the feed and must be
// treated as opaque text (vbpl uses both numeric ItemIDs and UUIDs). DetailURL
// is the human/source URL when a source needs it, or for operator inspection.
type DetailRef struct {
	ExternalID string
	DetailURL  string
}

// Source is a self-contained Bronze crawler for one official site. Discovery is
// newest-first and watermark-bounded so the hourly Discover schedule stays
// nearly free; FetchDetail and Download are the heavier stages run per genuinely
// new document. Temporal bounds fetch concurrency; sources may also apply
// operator-configured pacing.
type Source interface {
	// ID returns the stable source identifier ("congbao", "vbpl", "sbv_hanoi", "phapluat").
	ID() string

	// Discover reads the newest-first feed for a query keyword and returns
	// documents published strictly after the watermark (pass the zero time to
	// take the whole slice). keyword is the per-source query term — e.g. a vbpl
	// search keyword; sources with a single global feed (congbao RSS) ignore it.
	// Results are newest-first as the feed orders them.
	Discover(ctx context.Context, since time.Time, keyword string) ([]DiscoveredDoc, error)

	// FetchDetail fetches a document's detail page/API record and returns the
	// parsed metadata plus any downloadable file references (and inline HTML when
	// the source serves it).
	FetchDetail(ctx context.Context, ref DetailRef) (*DiscoveredDoc, error)

	// Download retrieves a file's bytes into w and returns the number of bytes
	// written and their SHA-256, lowercase hex.
	Download(ctx context.Context, ref FileRef, w io.Writer) (n int64, sha256Hex string, err error)
}

// NumberSearcher is implemented by sources that can look up one document by its
// exact số ký hiệu. It is used for cross-source backfills, e.g. a stale VBPL
// placeholder can enqueue the matching congbao gazette file without widening the
// normal discovery crawl. titleHint is the caller's known title, passed as a
// disambiguating term for fuzzy source search APIs; implementations may ignore it
// but must always re-verify normalized số-ký-hiệu equality before returning a hit.
type NumberSearcher interface {
	SearchByNumber(ctx context.Context, number, titleHint string) (*DiscoveredDoc, bool, error)
}

// TreeProvider is implemented by sources that expose a first-party provision
// tree. The returned content is source-native JSON; ok=false means the source has
// no usable tree for this document yet and callers should fall back to text
// parsing without treating the document as failed.
type TreeProvider interface {
	FetchTree(ctx context.Context, ref DetailRef) (content string, ok bool, err error)
}
