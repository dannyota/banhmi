// Package corpus binds Vietnam worker factories without importing implementations.
package corpus

import (
	"fmt"

	"danny.vn/banhmi/internal/corpus/source/api"
	"danny.vn/banhmi/internal/country/vn"
)

// Constructors are supplied by worker composition. M1.8 requires planners;
// M1.9 supplies decoders. Missing decoders keep api.Factory.Validate failing,
// so partial construction never implies runtime readiness.
type Constructors struct {
	NewPlanner func() api.Planner
	NewDecoder func() api.Decoder
}

// Factories binds exactly the four declared sources without invoking callbacks.
// It returns an owned slice in descriptor order. It cannot bind direct SBV.
func Factories(bindings map[string]Constructors) ([]api.Factory, error) {
	declaration := vn.Declare()
	if declaration.Code != "vn" || len(bindings) != len(declaration.Descriptor.Manifest.Sources) {
		return nil, fmt.Errorf("four Vietnam source bindings required")
	}
	factories := make([]api.Factory, 0, len(bindings))
	for _, source := range declaration.Descriptor.Manifest.Sources {
		constructors, ok := bindings[source.ID]
		if !ok || constructors.NewPlanner == nil {
			return nil, fmt.Errorf("missing planner constructor for %s", source.ID)
		}
		factories = append(factories, api.Factory{SourceID: source.ID, NewPlanner: constructors.NewPlanner, NewDecoder: constructors.NewDecoder})
	}
	return factories, nil
}
