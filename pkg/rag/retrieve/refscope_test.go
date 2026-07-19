package retrieve

import (
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/base/jurisdiction"
)

func TestDocNumberKey(t *testing.T) {
	cases := map[string]string{
		"09/2020/tt-nhnn": "092020ttnhnn",
		"52/2024/nđ-cp":   "522024ndcp",
		"134/2025/qh15":   "1342025qh15",
		"act 758":         "act758",
	}
	for in, want := range cases {
		if got := docNumberKey(in); got != want {
			t.Errorf("docNumberKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildDocFilterCTERefDocIDs characterizes identifier-scoped retrieval: with
// refDocIDs set and the validity relaxation applied (inForceOnly=false), the CTE
// filters on document ids alone; with strict InForceOnly, both conditions apply.
func TestBuildDocFilterCTERefDocIDs(t *testing.T) {
	res := resolved{refDocIDs: []int64{42, 43}}
	cte, args := buildDocFilterCTE(res, 4)
	if !strings.Contains(cte, "d.id = ANY($4)") {
		t.Fatalf("CTE missing ref doc condition:\n%s", cte)
	}
	if strings.Contains(cte, "status_class IN") {
		t.Fatalf("relaxed ref scope must not filter validity:\n%s", cte)
	}
	if len(args) != 1 {
		t.Fatalf("args = %v, want one ([]int64)", args)
	}

	strict := resolved{refDocIDs: []int64{42}, inForceOnly: true}
	cte, args = buildDocFilterCTE(strict, 4)
	if !strings.Contains(cte, "status_class IN ('in_force', 'partial')") || !strings.Contains(cte, "d.id = ANY($4)") {
		t.Fatalf("strict ref scope must filter validity AND doc ids:\n%s", cte)
	}
	if len(args) != 1 {
		t.Fatalf("strict args = %v, want one", args)
	}
}

func TestHasDocFilterIncludesRefDocIDs(t *testing.T) {
	if (resolved{}).hasDocFilter() {
		t.Fatal("empty resolved must have no doc filter")
	}
	if !(resolved{refDocIDs: []int64{1}}).hasDocFilter() {
		t.Fatal("refDocIDs must count as a doc filter")
	}
}

// TestIdentifierScopeFollowsJurisdiction guards the live-jurisdiction boundary:
// identifier-scoped retrieval is validated on VN only; MY (and any other
// jurisdiction) must keep the unscoped path until proven on its corpus.
func TestIdentifierScopeFollowsJurisdiction(t *testing.T) {
	for _, tc := range []struct {
		code string
		want bool
	}{{"vn", true}, {"my", false}, {"id", false}} {
		r := &hybridRetriever{identifierScope: true}
		WithJurisdiction(jurisdiction.For(tc.code))(r)
		if r.identifierScope != tc.want {
			t.Errorf("jurisdiction %s: identifierScope = %v, want %v", tc.code, r.identifierScope, tc.want)
		}
	}
}
