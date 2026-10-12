package country

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCountryErrorPreservesCauseAndCode(t *testing.T) {
	for _, cause := range []error{ErrCountryAbsent, ErrCountryDisabled, ErrEntryIncomplete} {
		err := fmt.Errorf("admission: %w", &CountryError{Code: "unknown", Cause: cause})
		if !errors.Is(err, cause) {
			t.Fatalf("wrapped error lost cause %v", cause)
		}
		var countryError *CountryError
		if !errors.As(err, &countryError) || countryError.Code != "unknown" {
			t.Fatalf("wrapped error lost rejected code: %v", err)
		}
		if !strings.Contains(err.Error(), "unknown") {
			t.Fatalf("error omitted rejected code: %v", err)
		}
		for _, other := range []error{ErrCountryAbsent, ErrCountryDisabled, ErrEntryIncomplete} {
			if cause != other && errors.Is(err, other) {
				t.Fatalf("error collapsed %v into %v", cause, other)
			}
		}
	}
}
