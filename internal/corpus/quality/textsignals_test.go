package quality

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func hasCode(codes []string, code string) bool {
	for _, value := range codes {
		if value == code {
			return true
		}
	}
	return false
}

func TestDiagnosticsBoundaries(t *testing.T) {
	for _, family := range []struct {
		id, code, glyph string
		count, total    int
		inclusive       bool
	}{
		{"bad", "TEXT_REPLACEMENT_CHARS", "\ufffd", 2, 200, false},
		{"control", "TEXT_REPLACEMENT_CHARS", "\x00", 2, 200, false},
		{"whitespace", "TEXT_HIGH_WHITESPACE", " ", 80, 200, false},
		{"pua", "TEXT_PUA_RUNES", "\ue000", 2, 400, false},
		{"markers", "TEXT_MOJIBAKE_MARKERS", "√", 8, 1600, false},
		{"cyrillic", "TEXT_CYRILLIC_MOJIBAKE", "Ж", 8, 1600, true},
	} {
		for _, delta := range []int{-1, 0, 1} {
			label := map[int]string{-1: "below", 0: "at", 1: "above"}[delta]
			t.Run(family.id+"-"+label, func(t *testing.T) {
				count := family.count + delta
				text := strings.Repeat(family.glyph, count) + strings.Repeat("á", family.total-count)
				want := delta > 0 || (family.inclusive && delta == 0)
				if hasCode(textSignals(text), family.code) != want {
					t.Fatal("diagnostic boundary mismatch")
				}
			})
		}
	}
	for _, c := range []struct {
		id, text, code string
		want           bool
	}{
		{"letters-below", strings.Repeat("á", 49), "TEXT_TOO_FEW_LETTERS", true},
		{"letters-at", strings.Repeat("á", 50), "TEXT_TOO_FEW_LETTERS", false},
		{"letters-above", strings.Repeat("á", 51), "TEXT_TOO_FEW_LETTERS", false},
		{"markers-count-below", strings.Repeat("√", 7), "TEXT_MOJIBAKE_MARKERS", false},
		{"markers-count-at", strings.Repeat("√", 8), "TEXT_MOJIBAKE_MARKERS", true},
		{"markers-count-above", strings.Repeat("√", 9), "TEXT_MOJIBAKE_MARKERS", true},
		{"cyrillic-count-below", strings.Repeat("Ж", 7), "TEXT_CYRILLIC_MOJIBAKE", false},
		{"cyrillic-count-at", strings.Repeat("Ж", 8), "TEXT_CYRILLIC_MOJIBAKE", true},
		{"cyrillic-count-above", strings.Repeat("Ж", 9), "TEXT_CYRILLIC_MOJIBAKE", true},
		{"density-letters-below", strings.Repeat("a", 199), "TEXT_LOW_DIACRITIC_DENSITY", false},
		{"density-letters-at", strings.Repeat("a", 200), "TEXT_LOW_DIACRITIC_DENSITY", true},
		{"density-letters-above", strings.Repeat("a", 201), "TEXT_LOW_DIACRITIC_DENSITY", true},
		{"density-below", strings.Repeat("a", 197) + "ááá", "TEXT_LOW_DIACRITIC_DENSITY", true},
		{"density-at", strings.Repeat("a", 196) + "áááá", "TEXT_LOW_DIACRITIC_DENSITY", false},
		{"density-above", strings.Repeat("a", 195) + "ááááá", "TEXT_LOW_DIACRITIC_DENSITY", false},
		{"permitted-controls", strings.Repeat("á", 100) + "\n\t\r\f", "TEXT_REPLACEMENT_CHARS", false},
		{"placeholder-fold", "  ĐANG\n CẬP\tNHẬT  tệp ĐÍNH KÈM ", "SOURCE_PLACEHOLDER", true},
		{"placeholder-empty", "", "SOURCE_PLACEHOLDER", false},
		{"placeholder-space", " \t\n", "SOURCE_PLACEHOLDER", false},
		{"placeholder-short", "Đang cập nhật", "SOURCE_PLACEHOLDER", true},
		{"placeholder-79", "dang cap nhat " + strings.Repeat("a", 68), "SOURCE_PLACEHOLDER", true},
		{"placeholder-80", "dang cap nhat " + strings.Repeat("a", 69), "SOURCE_PLACEHOLDER", false},
		{"placeholder-81", "dang cap nhat " + strings.Repeat("a", 70), "SOURCE_PLACEHOLDER", false},
		{"placeholder-199", "dang cap nhat file " + strings.Repeat("a", 184), "SOURCE_PLACEHOLDER", true},
		{"placeholder-200", "dang cap nhat file " + strings.Repeat("a", 185), "SOURCE_PLACEHOLDER", true},
		{"placeholder-201", "dang cap nhat file " + strings.Repeat("a", 186), "SOURCE_PLACEHOLDER", false},
	} {
		t.Run(c.id, func(t *testing.T) {
			if hasCode(textSignals(c.text), c.code) != c.want {
				t.Fatal("diagnostic boundary mismatch")
			}
		})
	}
}

func TestDiagnosticsGoldenFindings(t *testing.T) {
	data, err := os.ReadFile("testdata/diagnostics.json")
	if err != nil {
		t.Fatal("diagnostic fixture unavailable")
	}
	var cases []struct {
		ID   string
		Text string
		Want []string
	}
	if json.Unmarshal(data, &cases) != nil {
		t.Fatal("diagnostic fixture invalid")
	}
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			if !reflect.DeepEqual(textSignals(c.Text), c.Want) {
				t.Fatal("diagnostic golden mismatch")
			}
		})
	}
}

func TestDiagnosticsAggregateShortBlocks(t *testing.T) {
	g, request, _ := fixtureGate(t)
	base := request.Document.Blocks[0]
	request.Document.Blocks = nil
	for i, id := range []string{"one", "two", "three"} {
		b := base
		b.ID = id
		b.Kind = []model.BlockKind{model.BlockHeading, model.BlockParagraph, model.BlockTable}[i]
		b.NormalizedText = strings.Repeat("á", 20)
		b.SourceSpans = copySpans(base.SourceSpans)
		b.SourceSpans[0].BlockID = id
		request.Document.Blocks = append(request.Document.Blocks, b)
	}
	got, err := g.Evaluate(request)
	if err != nil || len(got.Findings) != 2 {
		t.Fatal("short blocks were diagnosed separately")
	}
	request.Document.Blocks = nil
	got, err = g.Evaluate(request)
	if err != nil || len(got.Findings) != 3 || got.Findings[2].ReasonCode != "TEXT_EMPTY" {
		t.Fatal("empty aggregate diagnostic failed")
	}
}

// Each control overlays one changed production trigger in a disposable hosted
// subprocess. Only its named boundary failure counts as successful detection.
func TestDiagnosticsChangedTriggerControls(t *testing.T) {
	data, err := os.ReadFile("textsignals.go")
	if err != nil {
		t.Fatal("control source unavailable")
	}
	original, err := filepath.Abs("textsignals.go")
	if err != nil {
		t.Fatal("control source path unavailable")
	}
	for _, c := range []struct {
		id, before, after, named string
	}{
		{"bad", "greater(bad, total, 1, 100)", "greater(bad, total, 2, 100)", "bad-above"},
		{"letters", "letters < 50", "letters < 49", "letters-below"},
		{"space", "greater(whitespace, total, 2, 5)", "greater(whitespace, total, 3, 5)", "whitespace-above"},
		{"pua", "greater(pua, total, 1, 200)", "greater(pua, total, 2, 200)", "pua-above"},
		{"markers", "markers >= 8", "markers >= 9", "markers-count-at"},
		{"cyrillic", "cyrillic >= 8", "cyrillic >= 9", "cyrillic-at"},
		{"density", "letters >= 200", "letters >= 201", "density-letters-at"},
		{"placeholder", "letters <= 200", "letters < 200", "placeholder-200"},
	} {
		t.Run(c.id, func(t *testing.T) {
			if strings.Count(string(data), c.before) != 1 {
				t.Fatal("control trigger not unique")
			}
			dir := t.TempDir()
			replacement := filepath.Join(dir, "changed.go")
			if os.WriteFile(replacement, []byte(strings.Replace(string(data), c.before, c.after, 1)), 0600) != nil {
				t.Fatal("control source staging failed")
			}
			overlay, err := json.Marshal(struct{ Replace map[string]string }{map[string]string{original: replacement}})
			if err != nil {
				t.Fatal("control overlay encoding failed")
			}
			manifest := filepath.Join(dir, "overlay.json")
			if os.WriteFile(manifest, overlay, 0600) != nil {
				t.Fatal("control overlay staging failed")
			}
			command := exec.Command("go", "test", "-count=1", "-overlay", manifest, "-run", "^TestDiagnosticsBoundaries$/^"+c.named+"$", ".")
			command.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "--- FAIL: TestDiagnosticsBoundaries/"+c.named) || !strings.Contains(string(output), "diagnostic boundary mismatch") {
				t.Fatal("changed-trigger control did not detect named boundary")
			}
		})
	}
}
