// Package country defines pure country metadata and admission contracts.
package country

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/model"
)

type Code string

type State string

const (
	Enabled          State = "enabled"
	MigratedDisabled State = "migrated-disabled"
	Absent           State = "absent"
)

type SourceDescriptor struct {
	ID, Package, CorpusLanguage, BindingLanguage string
}

// Capability records an implementation version or an explicit missing reason.
type Capability struct {
	Name, Version, MissingReason string
}

// Manifest identifies declared metadata and assets, not runtime readiness.
type Manifest struct {
	Code            Code
	ModuleVersion   string
	ContractVersion string
	Digest          model.Digest
	CorpusLanguage  string
	BindingLanguage string
	Timezone        string
	OCRHints        []string
	Sources         []SourceDescriptor
	Capabilities    []Capability
	Assets          []model.AssetRef
}

type Descriptor struct {
	Manifest Manifest
}

// Validate checks structural integrity without I/O or qualification checks.
// An omitted digest permits computing one before freezing the declaration.
func (m Manifest) Validate() error {
	if err := m.validateContent(); err != nil {
		return err
	}
	if m.Digest == "" {
		return nil
	}
	if err := m.Digest.Validate(); err != nil {
		return fmt.Errorf("manifest digest: %w", err)
	}
	want, err := m.ContentDigest()
	if err != nil {
		return err
	}
	if m.Digest != want {
		return fmt.Errorf("manifest digest does not match declared content")
	}
	return nil
}

func (m Manifest) validateContent() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"code", string(m.Code)},
		{"module version", m.ModuleVersion},
		{"contract version", m.ContractVersion},
		{"corpus language", m.CorpusLanguage},
		{"binding language", m.BindingLanguage},
		{"timezone", m.Timezone},
	} {
		if err := requiredText(field.name, field.value); err != nil {
			return err
		}
	}
	hints := make(map[string]bool, len(m.OCRHints))
	for _, hint := range m.OCRHints {
		if err := requiredText("OCR hint", hint); err != nil {
			return err
		}
		if hints[hint] {
			return fmt.Errorf("duplicate OCR hint %q", hint)
		}
		hints[hint] = true
	}
	sources := make(map[string]bool, len(m.Sources))
	for _, source := range m.Sources {
		for _, field := range []struct {
			name  string
			value string
		}{
			{"source ID", source.ID},
			{"source package", source.Package},
			{"source corpus language", source.CorpusLanguage},
			{"source binding language", source.BindingLanguage},
		} {
			if err := requiredText(field.name, field.value); err != nil {
				return err
			}
		}
		if sources[source.ID] {
			return fmt.Errorf("duplicate source ID %q", source.ID)
		}
		sources[source.ID] = true
	}
	capabilities := make(map[string]bool, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		if err := requiredText("capability name", capability.Name); err != nil {
			return err
		}
		if (capability.Version == "") == (capability.MissingReason == "") {
			return fmt.Errorf("capability %q must have a version or a missing reason, exclusively", capability.Name)
		}
		if capability.Version != "" {
			if err := requiredText("capability version", capability.Version); err != nil {
				return err
			}
		} else if err := requiredText("capability missing reason", capability.MissingReason); err != nil {
			return err
		}
		if capabilities[capability.Name] {
			return fmt.Errorf("duplicate capability name %q", capability.Name)
		}
		capabilities[capability.Name] = true
	}
	assets := make(map[string]bool, len(m.Assets))
	for _, asset := range m.Assets {
		if err := asset.Validate(); err != nil {
			return fmt.Errorf("manifest asset: %w", err)
		}
		if assets[asset.Path] {
			return fmt.Errorf("duplicate asset path %q", asset.Path)
		}
		assets[asset.Path] = true
	}
	return nil
}

func requiredText(name, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", name)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", name)
	}
	return nil
}

// Clone owns every slice and preserves its contents and ordering.
func (m Manifest) Clone() Manifest {
	m.OCRHints = slices.Clone(m.OCRHints)
	m.Sources = slices.Clone(m.Sources)
	m.Capabilities = slices.Clone(m.Capabilities)
	m.Assets = slices.Clone(m.Assets)
	return m
}

// ContentDigest hashes encoding v1: compact encoding/json with exported field
// names in declaration order, excluding Digest. Lists use sorted copies and
// encode as [] even when nil. Valid UTF-8 strings retain their exact identity.
// The supplied Digest never enters this computation or its validation.
func (m Manifest) ContentDigest() (model.Digest, error) {
	if err := m.validateContent(); err != nil {
		return "", err
	}
	m = m.Clone()
	if m.OCRHints == nil {
		m.OCRHints = []string{}
	}
	if m.Sources == nil {
		m.Sources = []SourceDescriptor{}
	}
	if m.Capabilities == nil {
		m.Capabilities = []Capability{}
	}
	if m.Assets == nil {
		m.Assets = []model.AssetRef{}
	}
	slices.Sort(m.OCRHints)
	slices.SortFunc(m.Sources, func(a, b SourceDescriptor) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(m.Capabilities, func(a, b Capability) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(m.Assets, func(a, b model.AssetRef) int { return strings.Compare(a.Path, b.Path) })
	// Keep this v1 wire shape fixed when later manifests gain fields.
	content := struct {
		Code            Code
		ModuleVersion   string
		ContractVersion string
		CorpusLanguage  string
		BindingLanguage string
		Timezone        string
		OCRHints        []string
		Sources         []SourceDescriptor
		Capabilities    []Capability
		Assets          []model.AssetRef
	}{
		Code:            m.Code,
		ModuleVersion:   m.ModuleVersion,
		ContractVersion: m.ContractVersion,
		CorpusLanguage:  m.CorpusLanguage,
		BindingLanguage: m.BindingLanguage,
		Timezone:        m.Timezone,
		OCRHints:        m.OCRHints,
		Sources:         m.Sources,
		Capabilities:    m.Capabilities,
		Assets:          m.Assets,
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", fmt.Errorf("encode manifest content: %w", err)
	}
	return model.Digest(fmt.Sprintf("%x", sha256.Sum256(encoded))), nil
}
