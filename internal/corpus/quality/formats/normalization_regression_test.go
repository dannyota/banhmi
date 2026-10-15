//go:build linux

package formats

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/conversion"
	"danny.vn/banhmi/internal/corpus/conversion/api"
	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

func TestNormalizationBindingPreservesNoChangeConversion(t *testing.T) {
	original := []byte("<body><p>abc</p></body>")
	recipe := []byte("synthetic-baseline-v1")
	hash := Hash([]byte("synthetic"))
	manifest := conversion.ConversionManifest{
		RequestKey: "convert-" + strings.Repeat("0", 32),
		Request: api.ConvertRequest{
			Artifact: model.CaptureRef{
				Country:          "vn",
				SourceID:         "synthetic",
				CaptureKey:       "capture-" + strings.Repeat("0", 32),
				Asset:            model.AssetRef{Path: "original", SHA256: Hash(original), ByteSize: int64(len(original))},
				MediaType:        "text/html",
				RequestHash:      hash,
				ResponseHash:     hash,
				TransportReceipt: model.AssetRef{Path: "transport", SHA256: hash, ByteSize: 1},
			},
			Recipe: model.AssetRef{Path: "recipe", SHA256: Hash(recipe), ByteSize: int64(len(recipe))},
			Budget: model.ResourceBudget{MaxItems: 1000, MaxInputBytes: 1 << 20, MaxPages: 10, MaxElapsed: time.Second, MaxConcurrent: 1},
		},
		ObservationKey:  "observation-" + strings.Repeat("0", 32),
		ArtifactRole:    "body",
		Language:        "vi",
		HTMLSelector:    "body",
		DetectorHash:    hash,
		CandidateDigest: hash,
		OutputBytes:     1 << 20,
	}
	request, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := json.Marshal(struct {
		Version  string
		Mode     string
		Request  json.RawMessage
		Original string
		Recipe   string
		Outputs  []any
	}{"bundle-v1", "convert", request, base64.StdEncoding.EncodeToString(original), base64.StdEncoding.EncodeToString(recipe), nil})
	if err != nil {
		t.Fatal(err)
	}
	input, output := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(input, "input.json"), bundle, 0600); err != nil {
		t.Fatal(err)
	}
	limits := sandbox.OfficeLimits()
	limits.InputBytes, err = conversion.BundleInputBytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := conversion.ChildHandler(sandbox.ChildRequest{InputDir: input, OutputDir: output, ScratchDir: t.TempDir(), InputType: sandbox.TypeJSON, Limits: limits}); err != nil {
		t.Fatal(err)
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(output, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var document model.ConvertedDocument
	if err := json.Unmarshal(read("results/"+string(Hash(request))+"/document.json"), &document); err != nil {
		t.Fatal(err)
	}
	mapBytes, ledgerBytes := read(document.OffsetMap.Path), read(document.EditLedger.Path)
	if !bytes.Equal(ledgerBytes, []byte("null")) || Hash(ledgerBytes) != document.EditLedger.SHA256 || int64(len(ledgerBytes)) != document.EditLedger.ByteSize {
		t.Fatal("no-change conversion lost its retained null ledger")
	}
	var maps []model.OffsetMapSegment
	var ledger []model.EditLedgerEntry
	if err := json.Unmarshal(mapBytes, &maps); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(ledgerBytes, &ledger); err != nil {
		t.Fatal(err)
	}
	if len(maps) == 0 || ledger != nil {
		t.Fatal("no-change fixture does not exercise a map and nil ledger")
	}
	if err := ValidateNormalizationBinding(BaselineNormalizer(), maps, ledger, DefaultLimits()); err != nil {
		t.Fatal("frozen no-change conversion rejected", err)
	}
	after, err := json.Marshal(ledger)
	if err != nil || !bytes.Equal(after, ledgerBytes) || Hash(after) != document.EditLedger.SHA256 || !bytes.Equal(read(document.EditLedger.Path), ledgerBytes) {
		t.Fatal("normalization binding changed retained ledger bytes or digest", err)
	}
}
