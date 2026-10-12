// Package vn declares Vietnam metadata without runtime registration or I/O.
package vn

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"

	"danny.vn/banhmi/internal/core/country"
	"danny.vn/banhmi/internal/corpus/model"
)

// Metadata is separate from declared asset bytes to avoid a self-hash cycle.
//
//go:embed data/manifest.json data/seeds/scope_term.csv data/dictionaries/abbreviation_expand_vn.csv data/dictionaries/diacritic_restore_vn.csv testdata/goldens/golden.json testdata/goldens/workflow-golden-vn.json
var embeddedAssets embed.FS

// AssetManifest returns frozen declaration metadata, not runtime readiness.
func AssetManifest() country.Manifest {
	return country.Manifest{
		Code:            "vn",
		ModuleVersion:   "m0-foundation-1",
		ContractVersion: "1",
		Digest:          "2f43a18ef3710e8216559fe3b89f7aadc836f6f61fd7599681e991e8f6e51e26",
		CorpusLanguage:  "vi",
		BindingLanguage: "vi",
		Timezone:        "Asia/Ho_Chi_Minh",
		OCRHints:        []string{"vi"},
		Sources: []country.SourceDescriptor{
			{ID: "congbao", Package: "pkg/ingest/congbao", CorpusLanguage: "vi", BindingLanguage: "vi"},
			{ID: "sbv_hanoi", Package: "pkg/ingest/sbvhanoi", CorpusLanguage: "vi", BindingLanguage: "vi"},
			{ID: "vanban", Package: "pkg/ingest/vanban", CorpusLanguage: "vi", BindingLanguage: "vi"},
			{ID: "vbpl", Package: "pkg/ingest/vbpl", CorpusLanguage: "vi", BindingLanguage: "vi"},
		},
		Capabilities: []country.Capability{
			{Name: "assets", Version: "m0-foundation-1"},
			{Name: "guide", MissingReason: "No reviewed standalone guide; legacy translation-first copy is not new-system guidance."},
			{Name: "grammar", MissingReason: "Vietnam grammar rules await M2."},
			{Name: "identity", MissingReason: "Vietnam identity rules await M2."},
			{Name: "language", MissingReason: "Vietnam query language rules await M2."},
			{Name: "locales", MissingReason: "No reviewed standalone interface locale data."},
			{Name: "qualification", MissingReason: "Vietnam qualification rules await M2."},
			{Name: "relations", MissingReason: "Vietnam relation rules await M2."},
			{Name: "source-decoders", MissingReason: "Pure source decoders await M1."},
			{Name: "source-factories", MissingReason: "Worker-only source factories await M1."},
			{Name: "source-planners", MissingReason: "Pure source planners await M1."},
			{Name: "structure", MissingReason: "Vietnam structure rules await M2."},
			{Name: "subjects", MissingReason: "Vietnam subject rules await M2."},
			{Name: "validity", MissingReason: "Vietnam validity rules await M2."},
		},
		Assets: []model.AssetRef{
			{Path: "data/dictionaries/abbreviation_expand_vn.csv", SHA256: "4d4e915d44cf03a2f0225f1a00650c92d7e4471d5702c75d5a5e52e21f84f2c6", ByteSize: 407},
			{Path: "data/dictionaries/diacritic_restore_vn.csv", SHA256: "46313cc987da135076224396f48afe75f914191fb9489119999099f5e92c6e67", ByteSize: 851622},
			{Path: "data/seeds/scope_term.csv", SHA256: "e9ce8c072901b30837109b056b4a2a3afafdbc882ddb6d618f47839d1f40c42d", ByteSize: 6842},
			{Path: "testdata/goldens/golden.json", SHA256: "881d09074ed5160e1e3ef97cd9220eb53ee16ccbbd7d7b0d39fb9483bbcb3aae", ByteSize: 52403},
			{Path: "testdata/goldens/workflow-golden-vn.json", SHA256: "878b5ae0d1d723970ed394724ce875c28d25ca2427507f1abacb2c959a3475bd", ByteSize: 7861},
		},
	}
}

// Asset returns independent bytes only for a declared, hash-verified asset.
func Asset(path string) ([]byte, error) {
	manifest := AssetManifest()
	if err := manifest.Validate(); err != nil {
		return nil, fmt.Errorf("validate Vietnam manifest: %w", err)
	}
	if err := validateAssets(embeddedAssets, manifest.Assets); err != nil {
		return nil, err
	}
	for _, ref := range manifest.Assets {
		if ref.Path == path {
			b, err := embeddedAssets.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read Vietnam asset %q: %w", path, err)
			}
			return bytes.Clone(b), nil
		}
	}
	return nil, fmt.Errorf("Vietnam asset %q is undeclared: %w", path, fs.ErrNotExist)
}

func validateAssets(files fs.ReadFileFS, refs []model.AssetRef) error {
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("validate Vietnam asset: %w", err)
		}
		if seen[ref.Path] {
			return fmt.Errorf("duplicate Vietnam asset %q", ref.Path)
		}
		seen[ref.Path] = true
		b, err := files.ReadFile(ref.Path)
		if err != nil {
			return fmt.Errorf("read Vietnam asset %q: %w", ref.Path, err)
		}
		if int64(len(b)) != ref.ByteSize || model.Digest(fmt.Sprintf("%x", sha256.Sum256(b))) != ref.SHA256 {
			return fmt.Errorf("Vietnam asset %q does not match its declared bytes", ref.Path)
		}
	}
	return nil
}
