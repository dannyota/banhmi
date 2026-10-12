package vn

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"danny.vn/banhmi/internal/corpus/model"
)

func TestEmbeddedAssetsMatchManifest(t *testing.T) {
	m := AssetManifest()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.Digest == "" || len(m.Assets) != 5 {
		t.Fatalf("manifest digest or asset inventory missing: %+v", m)
	}
	for _, ref := range m.Assets {
		b, err := Asset(ref.Path)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(b)) != ref.ByteSize || model.Digest(fmt.Sprintf("%x", sha256.Sum256(b))) != ref.SHA256 {
			t.Errorf("asset bytes differ: %s", ref.Path)
		}
	}
	for _, path := range []string{"missing.csv", "../data/seeds/scope_term.csv", "/data/seeds/scope_term.csv", "data/manifest.json", "data\\seeds\\scope_term.csv"} {
		if _, err := Asset(path); err == nil {
			t.Errorf("accepted undeclared path %q", path)
		}
	}
	for _, mutate := range []func([]model.AssetRef) []model.AssetRef{
		func(refs []model.AssetRef) []model.AssetRef { return append(refs, refs[0]) },
		func(refs []model.AssetRef) []model.AssetRef {
			refs[0].Path = "missing.csv"
			return refs
		},
		func(refs []model.AssetRef) []model.AssetRef {
			refs[0].Path = "../outside"
			return refs
		},
		func(refs []model.AssetRef) []model.AssetRef {
			refs[0].SHA256 = model.Digest(string(bytes.Repeat([]byte("0"), 64)))
			return refs
		},
	} {
		refs := mutate(AssetManifest().Assets)
		if err := validateAssets(embeddedAssets, refs); err == nil {
			t.Error("accepted invalid asset inventory")
		}
	}
	for _, name := range []string{"grammar", "source-factories", "guide", "locales", "qualification"} {
		found := false
		for _, capability := range m.Capabilities {
			if capability.Name == name {
				found = capability.Version == "" && capability.MissingReason != ""
			}
		}
		if !found {
			t.Errorf("missing pending capability %s", name)
		}
	}
	for _, source := range m.Sources {
		if source.ID == "sbv" {
			t.Error("unqualified direct SBV source declared")
		}
	}
}

func TestAssetReturnsCopy(t *testing.T) {
	m := AssetManifest()
	path := m.Assets[0].Path
	first, err := Asset(path)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Clone(first)
	first[0] ^= 0xff
	second, err := Asset(path)
	if err != nil || !bytes.Equal(second, want) {
		t.Fatalf("caller changed embedded asset: %v", err)
	}
	m.Assets[0].Path = "changed"
	m.OCRHints[0] = "changed"
	m.Sources[0].ID = "changed"
	m.Capabilities[0].Name = "changed"
	if err := AssetManifest().Validate(); err != nil {
		t.Fatalf("caller changed manifest: %v", err)
	}
}

func TestLegacyCopiesUnchanged(t *testing.T) {
	var inventory struct {
		SchemaVersion int `json:"schema_version"`
		Entries       []struct {
			Path         string       `json:"path"`
			OriginalPath string       `json:"original_path"`
			ByteSize     int64        `json:"byte_size"`
			SHA256       model.Digest `json:"sha256"`
			Purpose      string       `json:"purpose"`
		} `json:"entries"`
	}
	b, err := embeddedAssets.ReadFile("data/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.SchemaVersion != 1 || len(inventory.Entries) != 5 {
		t.Fatal("invalid copy inventory")
	}
	seen := map[string]bool{}
	for _, entry := range inventory.Entries {
		if seen[entry.Path] {
			t.Fatalf("duplicate copy provenance %s", entry.Path)
		}
		seen[entry.Path] = true
		original, err := os.ReadFile(filepath.Join("../../..", entry.OriginalPath))
		if err != nil {
			t.Fatal(err)
		}
		copied, err := Asset(entry.Path)
		if err != nil || !bytes.Equal(copied, original) {
			t.Fatalf("legacy copy changed: %s: %v", entry.Path, err)
		}
		if entry.Purpose == "" || entry.ByteSize != int64(len(copied)) || entry.SHA256 != model.Digest(fmt.Sprintf("%x", sha256.Sum256(copied))) {
			t.Errorf("copy provenance differs: %s", entry.Path)
		}
	}
}

type baselineInventory struct {
	SchemaVersion        int              `json:"schema_version"`
	SharedSeedReferences []model.AssetRef `json:"shared_seed_references"`
	Countries            []struct {
		Code         string `json:"code"`
		RegistryPath string `json:"registry_path"`
		ParserPath   string `json:"parser_path"`
		Sources      []struct{ ID, Package string } `json:"sources"`
		ScopeSeed    model.AssetRef                 `json:"scope_seed"`
		Golden       model.AssetRef                 `json:"golden"`
		Floors       map[string]float64             `json:"floors"`
		Snapshot     struct {
			Availability string `json:"availability"`
			Reason       string `json:"reason"`
			Reference    string `json:"reference"`
			Digest       string `json:"digest"`
		} `json:"snapshot"`
	} `json:"countries"`
}

func readBaseline(t *testing.T) baselineInventory {
	t.Helper()
	b, err := os.ReadFile("testdata/baseline-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory baselineInventory
	if err := json.Unmarshal(b, &inventory); err != nil {
		t.Fatal(err)
	}
	return inventory
}

func TestBaselineInventorySixCountries(t *testing.T) {
	baseline := readBaseline(t)
	if baseline.SchemaVersion != 1 || len(baseline.Countries) != 6 {
		t.Fatal("six-country inventory missing")
	}
	seen := map[string]bool{}
	parsers := map[string]string{"vn": "legalparse", "my": "malaysiaparse", "id": "indonesiaparse", "sg": "singaporeparse", "th": "thailandparse", "kh": "cambodiaparse"}
	sources := map[string]string{"vn": "congbao:congbao,sbv_hanoi:sbvhanoi,vanban:vanban,vbpl:vbpl", "my": "agclom:agclom,bnm:bnm,sc:sc", "id": "bi:bi,bpk:bpk,ojk:ojk,ojkweb:ojkweb", "sg": "csa:csa,mas:mas,pdpc:pdpc,sso:sso", "th": "bot:bot,etda:etda,ocs:ocs,sec:sec", "kh": "cdc:cdcgov,nbc:nbc,odc:odc,serc:serc"}
	for _, entry := range baseline.Countries {
		if _, ok := parsers[entry.Code]; !ok {
			t.Fatalf("unknown country %s", entry.Code)
		}
		if seen[entry.Code] {
			t.Fatalf("duplicate country %s", entry.Code)
		}
		seen[entry.Code] = true
		suffix := "_" + entry.Code
		if entry.Code == "vn" {
			suffix = ""
		}
		if entry.ScopeSeed.Path != "deploy/seed/scope_term"+suffix+".csv" || entry.Golden.Path != "deploy/eval/golden"+suffix+".json" {
			t.Errorf("wrong baseline paths for %s", entry.Code)
		}
		if entry.RegistryPath != "pkg/base/jurisdiction/jurisdiction.go" || entry.ParserPath != "pkg/pipeline/"+parsers[entry.Code]+".go" {
			t.Errorf("legacy paths missing for %s", entry.Code)
		}
		if len(entry.Sources) != len(strings.Split(sources[entry.Code], ",")) {
			t.Errorf("source count changed for %s", entry.Code)
		}
		for _, mapping := range strings.Split(sources[entry.Code], ",") {
			parts := strings.Split(mapping, ":")
			found := false
			for _, source := range entry.Sources {
				if source.ID == parts[0] && source.Package == "pkg/ingest/"+parts[1] {
					found = true
				}
			}
			if !found {
				t.Errorf("source mapping missing for %s: %s", entry.Code, mapping)
			}
		}
		for _, ref := range []model.AssetRef{entry.ScopeSeed, entry.Golden} {
			if err := ref.Validate(); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(filepath.Join("../../..", ref.Path))
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(b)) != ref.ByteSize || model.Digest(fmt.Sprintf("%x", sha256.Sum256(b))) != ref.SHA256 {
				t.Errorf("baseline bytes changed: %s", ref.Path)
			}
		}
		if entry.Snapshot.Availability != "missing" || entry.Snapshot.Reason == "" || entry.Snapshot.Digest != "" || entry.Snapshot.Reference != "" {
			t.Errorf("unsupported snapshot claim for %s", entry.Code)
		}
	}
	if len(baseline.SharedSeedReferences) != 10 {
		t.Fatal("shared seed references missing")
	}
	for _, ref := range baseline.SharedSeedReferences {
		b, err := os.ReadFile(filepath.Join("../../..", ref.Path))
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(b)) != ref.ByteSize || model.Digest(fmt.Sprintf("%x", sha256.Sum256(b))) != ref.SHA256 {
			t.Errorf("shared seed bytes changed: %s", ref.Path)
		}
	}
	for _, code := range []string{"vn", "my", "id", "sg", "th", "kh"} {
		if !seen[code] {
			t.Errorf("missing %s", code)
		}
	}
}

func TestLegacyFloorsUnchanged(t *testing.T) {
	want := map[string][]float64{"vn": {.90, .66, .99, .95}, "my": {.92, .77, .99, .95}, "id": {.78, .60, .99, .98}, "sg": {.90, .75, .99, .90}, "th": {.86, .68, .99, .90}, "kh": {.90, .70, .99, .95}}
	metrics := []string{"recall", "mrr", "current_law_precision", "abstention"}
	makefile, err := os.ReadFile("../../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range readBaseline(t).Countries {
		vector, ok := want[entry.Code]
		if !ok {
			t.Fatalf("unknown country %s", entry.Code)
		}
		for i, metric := range metrics {
			if entry.Floors[metric] != vector[i] {
				t.Errorf("changed %s %s floor", entry.Code, metric)
			}
		}
		command := fmt.Sprintf("-min-recall %.2f -min-mrr %.2f -min-inforce %.2f -min-abstain %.2f", vector[0], vector[1], vector[2], vector[3])
		_, target, found := strings.Cut(string(makefile), "eval-"+entry.Code+":")
		target, _, _ = strings.Cut(target, "\n\n")
		if !found || !strings.Contains(target, command) {
			t.Errorf("Makefile floor vector missing for %s", entry.Code)
		}
	}
	b, err := os.ReadFile("testdata/floors.json")
	if err != nil {
		t.Fatal(err)
	}
	var floors struct {
		Metrics    map[string]float64 `json:"metrics"`
		Provenance struct {
			Path, Target string
		} `json:"provenance"`
	}
	if err := json.Unmarshal(b, &floors); err != nil {
		t.Fatal(err)
	}
	if floors.Provenance.Path != "Makefile" || floors.Provenance.Target != "eval-vn" {
		t.Fatal("floor provenance changed")
	}
	for i, metric := range metrics {
		if floors.Metrics[metric] != want["vn"][i] {
			t.Errorf("changed VN %s floor", metric)
		}
	}
}
