package pipeline

import (
	"reflect"
	"testing"

	"danny.vn/banhmi/pkg/ingest"
	"danny.vn/banhmi/pkg/scope"
)

// TestScopeDecision pins the discovery scope policy: keyword slices are
// server-filtered (always in scope), a trusted sweep (ingest.SweepInScoper)
// enqueues every document without vocabulary matching — including consolidated
// (VBHN) texts, now indexed as primary — and untrusted sweeps stay
// vocabulary-gated.
func TestScopeDecision(t *testing.T) {
	matcher := scope.New(
		[]string{"an ninh mạng"},        // strong
		[]string{"tt-nhnn"},             // strong_title
		[]string{"công nghệ thông tin"}, // weak
		[]string{"ngân hàng"},           // signal
	)
	retention := ingest.DiscoveredDoc{
		Number: "04/2025/TT-NHNN",
		Title:  "Thông tư quy định thời hạn lưu trữ hồ sơ, tài liệu ngành Ngân hàng",
	}
	vbhn := ingest.DiscoveredDoc{
		Number:         "15/VBHN-NHNN",
		Title:          "Văn bản hợp nhất Thông tư quy định về an toàn kho quỹ",
		IsConsolidated: true,
	}
	offTopic := ingest.DiscoveredDoc{
		Number: "50/2025/NĐ-CP",
		Title:  "Nghị định về quản lý, sử dụng tài sản công",
	}

	tests := []struct {
		name           string
		doc            ingest.DiscoveredDoc
		keyword        string
		matcher        *scope.Matcher
		sweepTrusted   bool
		wantProvenance string
		wantMatched    []string
		wantInScope    bool
	}{
		{
			name: "keyword slice is server-filtered", doc: offTopic,
			keyword: "an ninh mạng", matcher: nil,
			wantProvenance: "keyword", wantMatched: []string{"an ninh mạng"}, wantInScope: true,
		},
		{
			name: "trusted sweep enqueues without vocabulary", doc: retention,
			matcher: matcher, sweepTrusted: true,
			wantProvenance: provenanceSweep, wantMatched: []string{provenanceSweep}, wantInScope: true,
		},
		{
			name: "trusted sweep enqueues consolidated VBHN", doc: vbhn,
			matcher: matcher, sweepTrusted: true,
			wantProvenance: provenanceSweep, wantMatched: []string{provenanceSweep}, wantInScope: true,
		},
		{
			name: "untrusted sweep drops vocabulary miss", doc: offTopic,
			matcher:     matcher,
			wantInScope: false,
		},
		{
			name: "untrusted sweep keeps số-ký-hiệu vocabulary hit", doc: retention,
			matcher:        matcher,
			wantProvenance: "keyword", wantMatched: []string{"tt-nhnn"}, wantInScope: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provenance, matched, inScope := scopeDecision(tt.doc, tt.keyword, tt.matcher, tt.sweepTrusted)
			if inScope != tt.wantInScope {
				t.Fatalf("inScope = %v, want %v", inScope, tt.wantInScope)
			}
			if provenance != tt.wantProvenance {
				t.Fatalf("provenance = %q, want %q", provenance, tt.wantProvenance)
			}
			if !reflect.DeepEqual(matched, tt.wantMatched) {
				t.Fatalf("matched = %v, want %v", matched, tt.wantMatched)
			}
		})
	}
}
