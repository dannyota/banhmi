package oauthserver

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleAuthorizationServerMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                          s.cfg.Issuer,
		"authorization_endpoint":                          s.cfg.Issuer + "/oauth/authorize",
		"token_endpoint":                                  s.cfg.Issuer + "/oauth/token",
		"revocation_endpoint":                             s.cfg.Issuer + "/oauth/revoke",
		"scopes_supported":                                []string{"mcp:read"},
		"response_types_supported":                        []string{"code"},
		"response_modes_supported":                        []string{"query"},
		"grant_types_supported":                           []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported":           []string{"none"},
		"revocation_endpoint_auth_methods_supported":      []string{"none"},
		"code_challenge_methods_supported":                 []string{"S256"},
		"authorization_response_iss_parameter_supported": true,
	})
}

func (s *Server) handleProtectedResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.cfg.Resource,
		"authorization_servers":    []string{s.cfg.Issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":          []string{"mcp:read"},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
