package kagglebatch

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStageInputsDedupSharedSha verifies that distinct documents sharing an
// identical PDF (same sha256) are staged and uploaded once. A duplicate dataset
// file path makes Kaggle's CreateDataset fail with "Path must be unique".
func TestStageInputsDedupSharedSha(t *testing.T) {
	srcDir := t.TempDir()
	fileA := filepath.Join(srcDir, "a.pdf")
	fileB := filepath.Join(srcDir, "b.pdf")
	if err := os.WriteFile(fileA, []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("beta"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Two inputs share sha "aaa" (one underlying file); a third is distinct.
	inputs := []Input{
		{Sha256: "aaa", Path: fileA},
		{Sha256: "aaa", Path: fileA},
		{Sha256: "bbb", Path: fileB},
	}

	inputDir := filepath.Join(t.TempDir(), "input")
	files, err := stageInputs(inputDir, inputs, Options{Languages: "vi", DPI: 300, BatchSize: 32})
	if err != nil {
		t.Fatalf("stageInputs: %v", err)
	}

	// Expect one file per unique sha plus params.json — and no duplicate paths.
	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if seen[f] {
			t.Errorf("duplicate staged path %q (would trip Kaggle 'Path must be unique')", f)
		}
		seen[f] = true
	}
	if want := 3; len(files) != want { // aaa.pdf, bbb.pdf, params.json
		t.Errorf("staged %d files, want %d: %v", len(files), want, files)
	}
	if !seen[filepath.Join(inputDir, "aaa.pdf")] || !seen[filepath.Join(inputDir, "bbb.pdf")] {
		t.Errorf("missing expected staged pdf(s): %v", files)
	}
	if !seen[filepath.Join(inputDir, paramsFileName)] {
		t.Errorf("missing %s in staged files: %v", paramsFileName, files)
	}
}
