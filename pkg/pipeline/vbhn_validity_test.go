package pipeline

import (
	"testing"
	"time"
)

func TestDecideVBHNValidity(t *testing.T) {
	mk := func(y int) time.Time { return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC) }

	tests := []struct {
		name string
		in   []vbhnConsolidation
		want map[int64]vbhnDecision // keyed by documentID
	}{
		{
			name: "newest mirrors in_force base",
			in: []vbhnConsolidation{
				{documentID: 10, docKey: "VBHN|10", baseDocumentID: 1, baseDocKey: "THÔNG TƯ|1", issuedAt: mk(2024), baseStatusCode: "CHL", baseStatusClass: "in_force"},
			},
			want: map[int64]vbhnDecision{
				10: {documentID: 10, statusCode: "CHL", statusClass: "in_force", reason: "consolidates_base_status:THÔNG TƯ|1"},
			},
		},
		{
			name: "newest mirrors expired base",
			in: []vbhnConsolidation{
				{documentID: 11, docKey: "VBHN|11", baseDocumentID: 2, baseDocKey: "THÔNG TƯ|2", issuedAt: mk(2023), baseStatusCode: "HHL", baseStatusClass: "expired"},
			},
			want: map[int64]vbhnDecision{
				11: {documentID: 11, statusCode: "HHL", statusClass: "expired", reason: "consolidates_base_status:THÔNG TƯ|2"},
			},
		},
		{
			name: "older consolidation expired, newest mirrors base",
			in: []vbhnConsolidation{
				{documentID: 20, docKey: "VBHN|20", baseDocumentID: 3, baseDocKey: "THÔNG TƯ|3", issuedAt: mk(2020), baseStatusCode: "CHL", baseStatusClass: "in_force"},
				{documentID: 21, docKey: "VBHN|21", baseDocumentID: 3, baseDocKey: "THÔNG TƯ|3", issuedAt: mk(2024), baseStatusCode: "CHL", baseStatusClass: "in_force"},
			},
			want: map[int64]vbhnDecision{
				21: {documentID: 21, statusCode: "CHL", statusClass: "in_force", reason: "consolidates_base_status:THÔNG TƯ|3"},
				20: {documentID: 20, statusCode: "SUPERSEDED", statusClass: "expired", reason: "superseded_by_newer_consolidation"},
			},
		},
		{
			name: "unresolved base is unknown",
			in: []vbhnConsolidation{
				{documentID: 30, docKey: "VBHN|30", baseDocumentID: 0, issuedAt: mk(2024)},
			},
			want: map[int64]vbhnDecision{
				30: {documentID: 30, statusCode: "", statusClass: "unknown", reason: "consolidates_base_unresolved"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decideVBHNValidity(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("decisions = %d, want %d (%+v)", len(got), len(tt.want), got)
			}
			// decideVBHNValidity returns decisions ordered by documentID.
			for i := 1; i < len(got); i++ {
				if got[i-1].documentID > got[i].documentID {
					t.Fatalf("decisions not ordered by documentID: %+v", got)
				}
			}
			for _, d := range got {
				w, ok := tt.want[d.documentID]
				if !ok {
					t.Fatalf("unexpected decision for doc %d: %+v", d.documentID, d)
				}
				if d != w {
					t.Fatalf("doc %d = %+v, want %+v", d.documentID, d, w)
				}
			}
		})
	}
}
