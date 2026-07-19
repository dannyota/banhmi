package serc

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

// entryRe matches listing entries with a link to a PDF file.
// Pattern: data_dir/{boardID}/{fileID}.pdf in href
var entryRe = regexp.MustCompile(`<a[^>]+href=["'](?:(?:https?://serc\.gov\.kh)?/boards/)?data_dir/([^/]+)/([^"']+\.pdf)["'][^>]*>`)

// pageCountRe finds pagination info to determine total pages.
var pageCountRe = regexp.MustCompile(`p=(\d+)`)

// Discover iterates all English board sections and returns discovered PDF documents.
// The since and keyword parameters are ignored — the SERC corpus is small.
func (s *Source) Discover(ctx context.Context, _ time.Time, _ string) ([]ingest.DiscoveredDoc, error) {
	var docs []ingest.DiscoveredDoc
	for _, board := range englishBoards {
		boardDocs, err := s.discoverBoard(ctx, board)
		if err != nil {
			s.log.Warn("serc board discover failed", "board", board.ID, "err", err)
			continue
		}
		docs = append(docs, boardDocs...)
	}
	s.log.Info("serc discover", "docs", len(docs))
	return docs, nil
}

// discoverBoard pages through one board's listing and returns discovered docs.
func (s *Source) discoverBoard(ctx context.Context, board boardSection) ([]ingest.DiscoveredDoc, error) {
	var docs []ingest.DiscoveredDoc
	seen := map[string]bool{}

	for page := 1; ; page++ {
		if page > 1 {
			if err := sleep(ctx, pacePage); err != nil {
				return docs, err
			}
		}

		pageURL := fmt.Sprintf("%s/boards/index.php?bid=%s&nav=list&p=%d", s.baseURL, board.ID, page)
		body, err := s.get(ctx, pageURL)
		if err != nil {
			return docs, fmt.Errorf("serc board %s page %d: %w", board.ID, page, err)
		}

		entries := parseBoardEntries(body, board, s.baseURL)
		for _, doc := range entries {
			if seen[doc.ExternalID] {
				continue
			}
			seen[doc.ExternalID] = true
			docs = append(docs, doc)
		}

		// Check if there's a next page.
		if !hasNextPage(body, page) {
			break
		}
		// Safety cap.
		if page >= 50 {
			break
		}
	}

	s.log.Info("serc board done", "board", board.ID, "type", board.DocType, "docs", len(docs))
	return docs, nil
}

// parseBoardEntries extracts document entries from a board listing page.
func parseBoardEntries(html string, board boardSection, baseURL string) []ingest.DiscoveredDoc {
	var docs []ingest.DiscoveredDoc
	for _, m := range entryRe.FindAllStringSubmatch(html, -1) {
		boardID := m[1]
		filename := m[2]
		fileID := strings.TrimSuffix(filename, ".pdf")
		fileID = strings.TrimSuffix(fileID, ".PDF")

		pdfURL := baseURL + "/boards/data_dir/" + boardID + "/" + filename
		title := slugToTitle(fileID)

		docs = append(docs, ingest.DiscoveredDoc{
			SourceID:   SourceID,
			ExternalID: boardID + "/" + fileID,
			Title:      title,
			Abstract:   title,
			DocType:    ingest.DocType(board.DocType),
			DetailURL:  baseURL + "/boards/index.php?bid=" + board.ID + "&nav=list",
			Files: []ingest.FileRef{{
				URL:      pdfURL,
				Name:     filename,
				Ext:      "pdf",
				Kind:     "main",
				MIMEType: "application/pdf",
			}},
		})
	}
	return docs
}

// hasNextPage checks whether there is a page link for page+1.
func hasNextPage(html string, currentPage int) bool {
	next := strconv.Itoa(currentPage + 1)
	for _, m := range pageCountRe.FindAllStringSubmatch(html, -1) {
		if m[1] == next {
			return true
		}
	}
	return false
}

// slugToTitle converts a filename slug to a readable title.
func slugToTitle(slug string) string {
	s := strings.ReplaceAll(slug, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return slug
	}
	words := strings.Fields(s)
	for i, w := range words {
		if len(w) > 0 {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
