package country

import (
	"errors"
	"fmt"
)

var (
	ErrCountryAbsent   = errors.New("country is absent")
	ErrCountryDisabled = errors.New("country is disabled")
	ErrEntryIncomplete = errors.New("country entry is incomplete")
)

// CountryError retains the rejected code and supports errors.Is and errors.As.
type CountryError struct {
	Code  Code
	Cause error
}

func (e *CountryError) Error() string {
	return fmt.Sprintf("country %q: %v", e.Code, e.Cause)
}

func (e *CountryError) Unwrap() error {
	return e.Cause
}
