package pipeline

// DiscoverParams selects the slice to discover: a source and an optional keyword.
// An empty keyword means the source's whole newest-first feed (e.g. congbao RSS);
// keyword-filtered sources (vbpl) run one Discover per keyword. Limit caps the
// number of in-scope documents enqueued per call (0 = unlimited).
type DiscoverParams struct {
	Source  string
	Keyword string
	Limit   int
}

// DiscoverResult summarizes one Discover run.
type DiscoverResult struct {
	Discovered int    // documents the feed returned after the watermark
	Enqueued   int    // in-scope documents written to the ledger this run
	Skipped    int    // documents skipped as out of scope, excluded, or duplicate
	Watermark  string // new watermark persisted to discover_cursor (RFC3339)
}
