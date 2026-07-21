package app

import (
	"danny.vn/banhmi/pkg/ingest/vbpl"
	"danny.vn/banhmi/pkg/mcp"
)

// MCPFileLinkOptions returns the MCP options carrying source-specific file-link
// builders into the MCP surface. vbpl serves files only through expiring
// presigned URLs, so its stable files-listing endpoint is the one durable
// download pointer the document tool can offer. Builders are keyed by source
// code, so registering them is unconditional: only corpora holding that
// source's documents ever emit a files_url.
func MCPFileLinkOptions() []mcp.Option {
	return []mcp.Option{mcp.WithFilesListingURL(vbpl.SourceID, vbpl.FilesListingURL)}
}
