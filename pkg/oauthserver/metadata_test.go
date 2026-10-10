package oauthserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAuthorizationServerMetadata(t *testing.T) {
	server := &Server{cfg: Config{Issuer: "https://evidence.example", Resource: testResource}}
	recorder := httptest.NewRecorder()
	server.handleAuthorizationServerMetadata(recorder, httptest.NewRequest(http.MethodGet, "https://evidence.example/.well-known/oauth-authorization-server", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if body["issuer"] != "https://evidence.example" || body["authorization_response_iss_parameter_supported"] != true {
		t.Fatalf("metadata = %#v", body)
	}
	if _, advertised := body["registration_endpoint"]; advertised {
		t.Fatal("metadata advertised dynamic client registration")
	}
}

func TestProtectedResourceMetadataAliases(t *testing.T) {
	server := &Server{cfg: Config{Issuer: "https://evidence.example", Resource: testResource}}
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		recorder := httptest.NewRecorder()
		server.handleProtectedResourceMetadata(recorder, httptest.NewRequest(http.MethodGet, "https://evidence.example"+path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, recorder.Code)
		}
		var body struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			BearerMethods        []string `json:"bearer_methods_supported"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if body.Resource != testResource || len(body.AuthorizationServers) != 1 || body.AuthorizationServers[0] != "https://evidence.example" || len(body.BearerMethods) != 1 || body.BearerMethods[0] != "header" {
			t.Fatalf("%s metadata = %#v", path, body)
		}
	}
}
