package bot

import (
	"regexp"
)

// ASP.NET WebForms hidden fields that must be chained from each response into
// the next POST request. ViewState alone is ~62KB base64; EventValidation ~16KB.
var (
	viewStateRe          = regexp.MustCompile(`<input[^>]+name="__VIEWSTATE"[^>]+value="([^"]*)"`)
	viewStateGenRe       = regexp.MustCompile(`<input[^>]+name="__VIEWSTATEGENERATOR"[^>]+value="([^"]*)"`)
	eventValidationRe    = regexp.MustCompile(`<input[^>]+name="__EVENTVALIDATION"[^>]+value="([^"]*)"`)
	viewStateEncryptedRe = regexp.MustCompile(`<input[^>]+name="__VIEWSTATEENCRYPTED"[^>]+value="([^"]*)"`)
)

// viewState holds the ASP.NET WebForms hidden fields extracted from a response.
type viewState struct {
	ViewState          string
	ViewStateGenerator string
	EventValidation    string
	ViewStateEncrypted string
}

// extractViewState parses ASP.NET hidden fields from an HTML response body.
// Missing fields are left as empty strings (not all pages emit all four fields).
func extractViewState(body string) viewState {
	vs := viewState{}
	if m := viewStateRe.FindStringSubmatch(body); m != nil {
		vs.ViewState = m[1]
	}
	if m := viewStateGenRe.FindStringSubmatch(body); m != nil {
		vs.ViewStateGenerator = m[1]
	}
	if m := eventValidationRe.FindStringSubmatch(body); m != nil {
		vs.EventValidation = m[1]
	}
	if m := viewStateEncryptedRe.FindStringSubmatch(body); m != nil {
		vs.ViewStateEncrypted = m[1]
	}
	return vs
}
