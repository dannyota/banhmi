package country

import "fmt"

// Entry is a runtime header. M0 rejects every supplied header as incomplete.
type Entry struct {
	Code     Code
	State    State
	Manifest Manifest
}

// Declaration records inventory without granting runtime admission.
type Declaration struct {
	Code       Code
	Descriptor Descriptor
	Entry      *Entry
}

// Reader exposes admission without construction operations.
type Reader interface {
	Lookup(Code) (Entry, error)
	Entries() []Entry
	RequireEnabled(Code) (Entry, error)
}

type Registry interface {
	Reader
	Freeze() error
}

type registry struct {
	declarations []Declaration
}

// NewRegistry validates and snapshots inventory without invoking country code.
// Complete rule bundles and source bindings belong to later consumer slices.
func NewRegistry(declarations []Declaration) (Registry, error) {
	snapshot := make([]Declaration, 0, len(declarations))
	seen := make(map[Code]bool, len(declarations))
	for _, declaration := range declarations {
		if declaration.Entry != nil {
			return nil, &CountryError{Code: declaration.Code, Cause: ErrEntryIncomplete}
		}
		switch declaration.Code {
		case "vn", "my", "id", "sg", "th", "kh":
		default:
			return nil, &CountryError{Code: declaration.Code, Cause: fmt.Errorf("unsupported declaration code")}
		}
		if seen[declaration.Code] {
			return nil, &CountryError{Code: declaration.Code, Cause: fmt.Errorf("duplicate declaration")}
		}
		if declaration.Descriptor.Manifest.Code != declaration.Code {
			return nil, &CountryError{Code: declaration.Code, Cause: fmt.Errorf("descriptor code does not match declaration")}
		}
		if err := declaration.Descriptor.Manifest.Validate(); err != nil {
			return nil, &CountryError{Code: declaration.Code, Cause: fmt.Errorf("descriptor manifest: %w", err)}
		}
		declaration.Descriptor.Manifest = declaration.Descriptor.Manifest.Clone()
		snapshot = append(snapshot, declaration)
		seen[declaration.Code] = true
	}
	return &registry{declarations: snapshot}, nil
}

func (r *registry) Lookup(code Code) (Entry, error) {
	if code == "" {
		code = "vn"
	}
	return Entry{}, &CountryError{Code: code, Cause: ErrCountryAbsent}
}

func (r *registry) RequireEnabled(code Code) (Entry, error) {
	return r.Lookup(code)
}

func (r *registry) Entries() []Entry {
	return []Entry{}
}

// Freeze is idempotent. This construction-stage registry is already immutable
// and has no binding or mutation API, so freezing cannot add runtime entries.
func (r *registry) Freeze() error {
	return nil
}
