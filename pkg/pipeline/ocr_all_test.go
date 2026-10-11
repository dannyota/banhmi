package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/extract"
	ocrbatch "danny.vn/banhmi/pkg/rag/ocr/kagglebatch"
)

func TestProcessOCRShardReturnsOneBoundedPageAndCursor(t *testing.T) {
	all := []ocrScan{{documentID: 2}, {documentID: 5}, {documentID: 8}, {documentID: 13}}
	var cursors []int64
	load := func(_ context.Context, afterID int64, limit int) ([]ocrScan, error) {
		cursors = append(cursors, afterID)
		var page []ocrScan
		for _, scan := range all {
			if scan.documentID > afterID && len(page) < limit {
				page = append(page, scan)
			}
		}
		return page, nil
	}
	var pageSizes []int
	result, err := processOCRShard(context.Background(), 0, 3, load,
		func(_ context.Context, scans []ocrScan) (OcrAllResult, error) {
			pageSizes = append(pageSizes, len(scans))
			return OcrAllResult{Processed: len(scans), Complete: true}, nil
		})
	if err != nil {
		t.Fatalf("processOCRShard: %v", err)
	}
	if result != (OcrAllResult{Processed: 3, NextCursor: 8}) {
		t.Fatalf("result = %+v", result)
	}
	if !reflect.DeepEqual(cursors, []int64{0}) {
		t.Fatalf("cursors = %v, want [0]", cursors)
	}
	if !reflect.DeepEqual(pageSizes, []int{3}) {
		t.Fatalf("page sizes = %v, want [3]", pageSizes)
	}
}

func TestProcessOCRShardReturnsPartialResult(t *testing.T) {
	wantErr := errors.New("provider stopped")
	result, err := processOCRShard(context.Background(), 0, 2,
		func(context.Context, int64, int) ([]ocrScan, error) {
			return []ocrScan{{documentID: 1}, {documentID: 2}}, nil
		},
		func(context.Context, []ocrScan) (OcrAllResult, error) {
			return OcrAllResult{Processed: 1, Failed: 1, Complete: true}, wantErr
		})
	if result != (OcrAllResult{Processed: 1, Failed: 1, NextCursor: 2, Complete: true}) {
		t.Fatalf("result = %+v", result)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want provider error", err)
	}
}

func TestProcessOCRShardPreservesAggregateBoundaryCursor(t *testing.T) {
	result, err := processOCRShard(context.Background(), 10, 2,
		func(context.Context, int64, int) ([]ocrScan, error) {
			return []ocrScan{{documentID: 11}, {documentID: 20}, {documentID: 30}}, nil
		},
		func(context.Context, []ocrScan) (OcrAllResult, error) {
			return OcrAllResult{Processed: 1, NextCursor: 11, Complete: false}, nil
		})
	if err != nil {
		t.Fatalf("processOCRShard: %v", err)
	}
	if result.NextCursor != 11 || result.Complete {
		t.Fatalf("result = %+v, want incomplete cursor 11", result)
	}
}

func TestValidateOCRBudgetRequiresEveryBound(t *testing.T) {
	valid := OcrAllParams{
		MaxItems: 1, MaxInputBytes: 1, MaxPages: 1, MaxElapsed: time.Second,
		MaxConcurrent: 1,
	}
	tests := []struct {
		name   string
		mutate func(*OcrAllParams)
	}{
		{name: "items", mutate: func(p *OcrAllParams) { p.MaxItems = 0 }},
		{name: "bytes", mutate: func(p *OcrAllParams) { p.MaxInputBytes = 0 }},
		{name: "pages", mutate: func(p *OcrAllParams) { p.MaxPages = 0 }},
		{name: "elapsed", mutate: func(p *OcrAllParams) { p.MaxElapsed = 0 }},
		{name: "concurrent", mutate: func(p *OcrAllParams) { p.MaxConcurrent = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := valid
			tt.mutate(&params)
			if err := validateOCRBudget(params); err == nil {
				t.Fatal("validateOCRBudget accepted a missing bound")
			}
		})
	}
}

func TestCheckOCRInputBudgetDistinguishesRejectFromFull(t *testing.T) {
	params := OcrAllParams{MaxInputBytes: 10, MaxPages: 4}
	tests := []struct {
		name                  string
		inputBytes, usedBytes int64
		pages, usedPages      int
		want                  ocrBudgetDecision
	}{
		{name: "within bounds", inputBytes: 6, pages: 2, want: ocrBudgetAccept},
		{name: "aggregate bytes", inputBytes: 6, usedBytes: 5, pages: 1, want: ocrBudgetFull},
		{name: "single byte size", inputBytes: 11, pages: 1, want: ocrBudgetReject},
		{name: "single page count", inputBytes: 1, pages: 5, want: ocrBudgetReject},
		{name: "aggregate pages", inputBytes: 1, pages: 2, usedPages: 3, want: ocrBudgetFull},
		{name: "empty document", inputBytes: 1, pages: 0, want: ocrBudgetReject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, reason := checkOCRInputBudget(tt.inputBytes, tt.pages, tt.usedBytes, tt.usedPages, params)
			if decision != tt.want {
				t.Fatalf("decision = %v reason = %q, want %v", decision, reason, tt.want)
			}
		})
	}
}

func TestUniqueOCRScansDeduplicatesOnlyOnePage(t *testing.T) {
	scans := []ocrScan{
		{documentID: 1, sha256: "same"},
		{documentID: 2, sha256: "same"},
		{documentID: 3, sha256: "other"},
	}
	unique := uniqueOCRScans(scans)
	if len(unique) != 2 || unique[0].documentID != 1 || unique[1].documentID != 3 {
		t.Fatalf("unique scans = %+v", unique)
	}
}

func TestUnfinishedOCRScansAccountsEveryUnpersistedDocument(t *testing.T) {
	scans := []ocrScan{
		{documentID: 1, sha256: "same"},
		{documentID: 2, sha256: "same"},
		{documentID: 3, sha256: "other"},
	}
	completed := map[int64]bool{1: true}
	unfinished := unfinishedOCRScans(scans, completed)
	if len(unfinished) != 2 || unfinished[0].documentID != 2 || unfinished[1].documentID != 3 {
		t.Fatalf("unfinished = %+v, want documents 2 and 3", unfinished)
	}
	if !completed[2] || !completed[3] {
		t.Fatalf("completed = %+v, want every selected document accounted", completed)
	}
}

func TestPreflightOCRPageRejectsMissingHashesPerDocument(t *testing.T) {
	activities := Activities{log: slog.New(slog.DiscardHandler)}
	params := OcrAllParams{MaxInputBytes: 10, MaxPages: 2}
	accepted, excluded, cursor, complete := activities.preflightOCRPage(context.Background(), params, 4, []ocrScan{
		{documentID: 5, rawFileID: 50},
		{documentID: 8, rawFileID: 80},
	})
	if len(accepted) != 0 || len(excluded) != 2 || cursor != 8 || !complete {
		t.Fatalf("accepted=%v excluded=%v cursor=%d complete=%t", accepted, excluded, cursor, complete)
	}
	for _, documentID := range []int64{5, 8} {
		failure := excluded[documentID]
		if !failure.rejected {
			t.Fatalf("document %d failure = %+v, want rejected", documentID, failure)
		}
	}
}

func TestProcessOCRPageDefersAggregateBoundaryAfterDuplicate(t *testing.T) {
	activities := Activities{log: slog.New(slog.DiscardHandler)}
	params := OcrAllParams{MaxInputBytes: 10, MaxPages: 10}
	scans := []ocrScan{
		{documentID: 1, sha256: "same"},
		{documentID: 2, sha256: "same"},
		{documentID: 3, sha256: "deferred"},
	}
	result, err := activities.processOCRPageWith(context.Background(), params, extract.DefaultGate(), time.Time{}, 0, scans,
		func(_ context.Context, scan ocrScan) (int64, error) {
			if scan.sha256 == "same" {
				return 6, nil
			}
			return 5, nil
		},
		func(ocrScan) (int, error) { return 1, nil },
		func(_ context.Context, _ OcrAllParams, accepted []ocrScan, onResult func(string, ocrOut) error) error {
			if len(accepted) != 1 || accepted[0].sha256 != "same" {
				t.Fatalf("accepted = %+v, want one deduplicated input", accepted)
			}
			return onResult("same", ocrOut{Err: "provider rejected image"})
		},
		func(context.Context, extract.GateConfig, time.Time, ocrScan, ocrOut) error {
			t.Fatal("write called for failed OCR output")
			return nil
		})
	if err != nil {
		t.Fatalf("processOCRPageWith: %v", err)
	}
	if result.Failed != 2 || result.NextCursor != 2 || result.Complete {
		t.Fatalf("result = %+v, want two accounted failures and deferred document 3", result)
	}
}

func TestProcessOCRPageMissingHashCannotReachProviderOrWriter(t *testing.T) {
	activities := Activities{log: slog.New(slog.DiscardHandler)}
	params := OcrAllParams{MaxInputBytes: 10, MaxPages: 10}
	runCalls, writeCalls, sizeCalls := 0, 0, 0
	result, err := activities.processOCRPageWith(context.Background(), params, extract.DefaultGate(), time.Time{}, 4,
		[]ocrScan{{documentID: 5}, {documentID: 8}},
		func(context.Context, ocrScan) (int64, error) { sizeCalls++; return 1, nil },
		func(ocrScan) (int, error) { return 1, nil },
		func(context.Context, OcrAllParams, []ocrScan, func(string, ocrOut) error) error {
			runCalls++
			return nil
		},
		func(context.Context, extract.GateConfig, time.Time, ocrScan, ocrOut) error { writeCalls++; return nil })
	if err != nil {
		t.Fatalf("processOCRPageWith: %v", err)
	}
	if result.Rejected != 2 || result.NextCursor != 8 || runCalls != 0 || writeCalls != 0 || sizeCalls != 0 {
		t.Fatalf("result=%+v run=%d write=%d size=%d", result, runCalls, writeCalls, sizeCalls)
	}
}

func TestProcessOCRPageAccountsAllDocumentsAfterProviderAbort(t *testing.T) {
	activities := Activities{log: slog.New(slog.DiscardHandler)}
	params := OcrAllParams{MaxInputBytes: 10, MaxPages: 10}
	wantErr := errors.New("provider aborted")
	scans := []ocrScan{{documentID: 1, sha256: "a"}, {documentID: 2, sha256: "b"}, {documentID: 3, sha256: "c"}}
	result, err := activities.processOCRPageWith(context.Background(), params, extract.DefaultGate(), time.Time{}, 0, scans,
		func(context.Context, ocrScan) (int64, error) { return 1, nil },
		func(ocrScan) (int, error) { return 1, nil },
		func(_ context.Context, _ OcrAllParams, _ []ocrScan, onResult func(string, ocrOut) error) error {
			if err := onResult("a", ocrOut{Err: "bad image"}); err != nil {
				return err
			}
			return wantErr
		},
		func(context.Context, extract.GateConfig, time.Time, ocrScan, ocrOut) error { return nil })
	if !errors.Is(err, wantErr) || result.Failed != 3 {
		t.Fatalf("result=%+v error=%v, want three failures and provider cause", result, err)
	}
}

func TestProcessOCRPageAccountsAllDocumentsAfterPersistAbort(t *testing.T) {
	activities := Activities{log: slog.New(slog.DiscardHandler)}
	params := OcrAllParams{MaxInputBytes: 10, MaxPages: 10}
	wantErr := errors.New("persist failed")
	scans := []ocrScan{{documentID: 1, sha256: "a"}, {documentID: 2, sha256: "b"}, {documentID: 3, sha256: "c"}}
	result, err := activities.processOCRPageWith(context.Background(), params, extract.DefaultGate(), time.Time{}, 0, scans,
		func(context.Context, ocrScan) (int64, error) { return 1, nil },
		func(ocrScan) (int, error) { return 1, nil },
		func(_ context.Context, _ OcrAllParams, _ []ocrScan, onResult func(string, ocrOut) error) error {
			return onResult("a", ocrOut{Text: "text"})
		},
		func(context.Context, extract.GateConfig, time.Time, ocrScan, ocrOut) error { return wantErr })
	if !errors.Is(err, wantErr) || result.Failed != 3 || result.Processed != 0 {
		t.Fatalf("result=%+v error=%v, want three failures and persist cause", result, err)
	}
}

func TestConsumeOCRKaggleStreamDoesNotCallResultAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	emit := make(chan struct{})
	streamDone := make(chan error, 1)
	stream := func(ctx context.Context, _ []ocrbatch.Input, onResult func(ocrbatch.Result) error) error {
		close(started)
		<-emit
		err := onResult(ocrbatch.Result{Sha256: "late", Text: "late result"})
		streamDone <- err
		return err
	}
	returned := make(chan error, 1)
	calls := 0
	go func() {
		returned <- consumeOCRKaggleStream(ctx, nil, stream, func(string, ocrOut) error {
			calls++
			return nil
		}, func() {})
	}()
	<-started
	cancel()
	if err := <-returned; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	close(emit)
	if err := <-streamDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("provider callback error = %v, want cancellation", err)
	}
	if calls != 0 {
		t.Fatalf("onResult calls = %d, want 0 after return", calls)
	}
}

func TestConsumeOCRKaggleStreamReturnsResultCallbackErrorToProvider(t *testing.T) {
	wantErr := errors.New("persist failed")
	providerSaw := make(chan error, 1)
	stream := func(_ context.Context, _ []ocrbatch.Input, onResult func(ocrbatch.Result) error) error {
		err := onResult(ocrbatch.Result{Sha256: "one", Text: "text"})
		providerSaw <- err
		return err
	}
	err := consumeOCRKaggleStream(context.Background(), nil, stream, func(string, ocrOut) error {
		return wantErr
	}, func() {})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want persistence error", err)
	}
	if err := <-providerSaw; !errors.Is(err, wantErr) {
		t.Fatalf("provider callback error = %v, want persistence error", err)
	}
}

func TestOCRScanQueryForceHandling(t *testing.T) {
	normal := ocrScanQuery(false)
	forced := ocrScanQuery(true)
	if !strings.Contains(normal, "t.authority = 'ocr_extractive'") {
		t.Fatalf("normal query does not exclude existing OCR text")
	}
	if strings.Contains(forced, "t.authority = 'ocr_extractive'") {
		t.Fatalf("forced query still excludes existing OCR text")
	}
	for _, query := range []string{normal, forced} {
		if !strings.Contains(query, "d.id > $1") || !strings.Contains(query, "LIMIT $2") {
			t.Fatalf("query is not keyset paged: %s", query)
		}
	}
}
