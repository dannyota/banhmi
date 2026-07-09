// Command discovercheck runs each VN discovery keyword against the vbpl API and
// reports how many docs each returns, then cross-checks against the local DB to
// find new (undiscovered) documents. Temporary tool — delete after use.
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	apiURL    = "https://vbpl-bientap-gateway.moj.gov.vn/api/qtdc/public/doc/all"
	originURL = "https://vbpl.vn"
	userAgent = "banhmi/0.1 (+https://github.com/dannyota/banhmi)"
	pageSize  = 100
)

// Non-SBV agency IDs from deploy/seed/issuer_code.csv.
var nonSbvAgencyIDs = []string{"55", "56", "1", "57", "3", "14", "169", "2"}

type apiReq struct {
	PageNumber    int      `json:"pageNumber"`
	PageSize      int      `json:"pageSize"`
	SortBy        string   `json:"sortBy"`
	SortDirection string   `json:"sortDirection"`
	GroupVbpl     bool     `json:"groupVbpl"`
	AgencyLevel   string   `json:"agencyLevel"`
	OptionDoc     string   `json:"optionDoc"`
	MatchMode     string   `json:"matchMode"`
	AgencyIds     []string `json:"agencyIds,omitempty"`
	Keyword       string   `json:"keyword,omitempty"`
}

type apiResp struct {
	Success bool `json:"success"`
	Data    struct {
		Total int `json:"total"`
		Items []struct {
			ID         string `json:"id"`
			DocNum     string `json:"docNum"`
			Title      string `json:"title"`
			AgencyName string `json:"agencyName"`
		} `json:"items"`
	} `json:"data"`
}

type kwResult struct {
	Keyword string
	Total   int
	IDs     []string
}

func main() {
	keywords, err := loadKeywords("deploy/seed/discovery_keyword.csv")
	if err != nil {
		log.Fatalf("load keywords: %v", err)
	}
	fmt.Fprintf(os.Stderr, "Loaded %d keywords\n", len(keywords))

	client := &http.Client{Timeout: 60 * time.Second}
	ctx := context.Background()

	var results []kwResult
	allIDs := map[string]string{} // id → first keyword that found it
	grandTotal := 0

	for i, kw := range keywords {
		total, ids, err := searchKeyword(ctx, client, kw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%d/%d] %s — ERROR: %v\n", i+1, len(keywords), kw, err)
			continue
		}
		results = append(results, kwResult{Keyword: kw, Total: total, IDs: ids})
		newForKw := 0
		for _, id := range ids {
			if _, seen := allIDs[id]; !seen {
				allIDs[id] = kw
				newForKw++
			}
		}
		grandTotal += total
		fmt.Fprintf(os.Stderr, "[%d/%d] %-45s %4d docs (%d unique new)\n", i+1, len(keywords), kw, total, newForKw)
		time.Sleep(300 * time.Millisecond) // polite
	}

	fmt.Println()
	fmt.Println("=== KEYWORD RESULTS ===")
	fmt.Printf("%-45s %6s\n", "KEYWORD", "DOCS")
	fmt.Println(strings.Repeat("-", 55))
	for _, r := range results {
		fmt.Printf("%-45s %6d\n", r.Keyword, r.Total)
	}
	fmt.Println(strings.Repeat("-", 55))
	fmt.Printf("%-45s %6d\n", "TOTAL (with duplicates)", grandTotal)
	fmt.Printf("%-45s %6d\n", "UNIQUE DOCS (deduplicated)", len(allIDs))

	// Cross-check against local DB.
	dbURL := os.Getenv("BANHMI_DATABASE_URL")
	if dbURL == "" {
		pw := os.Getenv("BANHMI_DATABASE_PASSWORD")
		if pw == "" {
			pw = "banhmi"
		}
		dbURL = fmt.Sprintf("postgres://banhmi:%s@localhost:5432/banhmi?sslmode=disable", pw)
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nCannot connect to DB (%s): %v\n", dbURL, err)
		fmt.Println("\n(Skipping DB cross-check — run with local DB up)")
		return
	}
	defer pool.Close()

	// Check which IDs already exist in bronze.source_document.
	idList := make([]string, 0, len(allIDs))
	for id := range allIDs {
		idList = append(idList, id)
	}

	existing := map[string]bool{}
	rows, err := pool.Query(ctx,
		"SELECT external_id FROM bronze.source_document WHERE source_id = 'vbpl' AND external_id = ANY($1)",
		idList)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nDB query error: %v\n", err)
		return
	}
	for rows.Next() {
		var eid string
		if err := rows.Scan(&eid); err == nil {
			existing[eid] = true
		}
	}
	rows.Close()

	newCount := 0
	var newDocs []string
	for _, id := range idList {
		if !existing[id] {
			newCount++
			newDocs = append(newDocs, id)
		}
	}

	fmt.Println()
	fmt.Println("=== DB CROSS-CHECK ===")
	fmt.Printf("Unique docs from keywords:    %d\n", len(allIDs))
	fmt.Printf("Already in DB:                %d\n", len(existing))
	fmt.Printf("NEW (not in DB):              %d\n", newCount)

	if newCount > 0 && newCount <= 50 {
		sort.Strings(newDocs)
		fmt.Println("\nNew doc IDs (not yet discovered):")
		for _, id := range newDocs {
			fmt.Printf("  %s (keyword: %s)\n", id, allIDs[id])
		}
	} else if newCount > 50 {
		fmt.Printf("\n(Too many to list — %d new docs)\n", newCount)
	}
}

func searchKeyword(ctx context.Context, client *http.Client, keyword string) (int, []string, error) {
	req := apiReq{
		PageNumber:    1,
		PageSize:      pageSize,
		SortBy:        "issueDate",
		SortDirection: "desc",
		GroupVbpl:     false,
		AgencyLevel:   "TRUNG_UONG",
		OptionDoc:     "title",
		MatchMode:     "all_words",
		AgencyIds:     nonSbvAgencyIDs,
		Keyword:       keyword,
	}

	var allIDs []string
	total := 0

	for page := 1; ; page++ {
		req.PageNumber = page
		resp, err := doPost(ctx, client, req)
		if err != nil {
			return 0, nil, err
		}
		if page == 1 {
			total = resp.Data.Total
		}
		if len(resp.Data.Items) == 0 {
			break
		}
		for _, it := range resp.Data.Items {
			allIDs = append(allIDs, it.ID)
		}
		if page*pageSize >= total {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	return total, allIDs, nil
}

func doPost(ctx context.Context, client *http.Client, body apiReq) (*apiResp, error) {
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", originURL)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out apiResp
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func loadKeywords(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	_ = header
	var kws []string
	for {
		row, err := r.Read()
		if err != nil {
			break
		}
		if len(row) > 0 && strings.TrimSpace(row[0]) != "" {
			kws = append(kws, strings.TrimSpace(row[0]))
		}
	}
	return kws, nil
}
