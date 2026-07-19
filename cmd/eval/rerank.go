package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

const rerankTimeout = 5 * time.Minute

type rerankClient struct {
	endpoint     string
	model        string
	qwenTemplate bool
	instruction  string
	client       *http.Client
}

func newRerankClient(endpoint, model string, qwenTemplate bool, instruction string) *rerankClient {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if !strings.HasSuffix(endpoint, "/rerank") {
		endpoint += "/rerank"
	}
	return &rerankClient{
		endpoint:     endpoint,
		model:        strings.TrimSpace(model),
		qwenTemplate: qwenTemplate,
		instruction:  instruction,
		client:       &http.Client{Timeout: rerankTimeout},
	}
}

type rerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type rerankResponse struct {
	Results []struct {
		Index          int     `json:"index"`
		RelevanceScore float64 `json:"relevance_score"`
	} `json:"results"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (c *rerankClient) Rerank(ctx context.Context, query string, hits []retrieve.Hit) ([]retrieve.Hit, time.Duration, error) {
	if len(hits) == 0 {
		return nil, 0, nil
	}

	documents := make([]string, len(hits))
	for i, h := range hits {
		documents[i] = rerankDocument(h)
	}
	if c.qwenTemplate {
		query = qwenQuery(query, c.instruction)
		for i, doc := range documents {
			documents[i] = qwenDocument(doc)
		}
	}

	body, err := json.Marshal(rerankRequest{Model: c.model, Query: query, Documents: documents})
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.client.Do(req)
	dur := time.Since(start)
	if err != nil {
		return nil, dur, fmt.Errorf("POST %s: %w", c.endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, dur, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var apiErr rerankResponse
		if jerr := json.Unmarshal(raw, &apiErr); jerr == nil && apiErr.Error != nil {
			return nil, dur, fmt.Errorf("endpoint returned %d: %s", resp.StatusCode, apiErr.Error.Message)
		}
		return nil, dur, fmt.Errorf("endpoint returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var result rerankResponse
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, dur, fmt.Errorf("parse response: %w", err)
	}
	if result.Error != nil {
		return nil, dur, fmt.Errorf("API error: %s", result.Error.Message)
	}
	if len(result.Results) == 0 {
		return nil, dur, fmt.Errorf("endpoint returned no results for %d candidates", len(hits))
	}

	ranked := make([]retrieve.Hit, 0, len(result.Results))
	seen := make(map[int]bool, len(result.Results))
	for _, r := range result.Results {
		if r.Index < 0 || r.Index >= len(hits) {
			return nil, dur, fmt.Errorf("response index %d out of range [0,%d)", r.Index, len(hits))
		}
		h := hits[r.Index]
		h.Score = r.RelevanceScore
		ranked = append(ranked, h)
		seen[r.Index] = true
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})

	// Cohere-compatible servers usually return every input document. If a server
	// returns only a top subset, append unrated candidates after the scored list so
	// recall@k remains deterministic and conservative.
	for i, h := range hits {
		if !seen[i] {
			ranked = append(ranked, h)
		}
	}
	return ranked, dur, nil
}

func rerankDocument(h retrieve.Hit) string {
	var parts []string
	header := strings.TrimSpace(strings.Join([]string{h.DocNumber, h.Title, h.Citation}, " "))
	if header != "" {
		parts = append(parts, header)
	}
	if strings.TrimSpace(h.ContextPrefix) != "" {
		parts = append(parts, strings.TrimSpace(h.ContextPrefix))
	}
	if strings.TrimSpace(h.Content) != "" {
		parts = append(parts, strings.TrimSpace(h.Content))
	}
	return strings.Join(parts, "\n\n")
}

func qwenQuery(query, instruction string) string {
	const prefix = "<|im_start|>system\nJudge whether the Document meets the requirements based on the Query and the Instruct provided. Note that the answer can only be \"yes\" or \"no\".<|im_end|>\n<|im_start|>user\n"
	return fmt.Sprintf("%s<Instruct>: %s\n<Query>: %s\n", prefix, strings.TrimSpace(instruction), strings.TrimSpace(query))
}

func qwenDocument(doc string) string {
	const suffix = "<|im_end|>\n<|im_start|>assistant\n<think>\n\n</think>\n\n"
	return fmt.Sprintf("<Document>: %s%s", strings.TrimSpace(doc), suffix)
}

func effectiveTopK(cfg *config.Config, override int) int {
	switch {
	case override > 0:
		return override
	case cfg.Retrieve.TopK > 0:
		return cfg.Retrieve.TopK
	default:
		return 8
	}
}

func truncateHits(hits []retrieve.Hit, n int) []retrieve.Hit {
	if n <= 0 || len(hits) <= n {
		return hits
	}
	return hits[:n]
}
