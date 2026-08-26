package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"whatsapp-ai-caller-server/internal/swagger"
)

// swaggerOnlyRouter mounts just the doc.json override and the Swagger UI
// wildcard — the DB- and Clerk-dependent routes are omitted so the docs can be
// exercised over HTTP without external dependencies.
func swaggerOnlyRouter() http.Handler {
	r := chi.NewRouter()
	r.Get("/swagger/doc.json", swagger.DocJSONHandler())
	r.Get("/swagger/*", swagger.UIHandler())
	return r
}

// TestSwaggerDocServedOverHTTP confirms /swagger/doc.json returns the OpenAPI 3
// document (with the Agents docs) and that the override wins over the wildcard.
func TestSwaggerDocServedOverHTTP(t *testing.T) {
	srv := httptest.NewServer(swaggerOnlyRouter())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/swagger/doc.json")
	if err != nil {
		t.Fatalf("GET /swagger/doc.json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("doc.json status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("doc.json content-type = %q, want application/json", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("doc.json is not valid JSON: %v", err)
	}
	if doc["openapi"] != "3.0.3" {
		t.Errorf("served openapi = %v, want 3.0.3 (override did not win)", doc["openapi"])
	}
	paths, _ := doc["paths"].(map[string]any)
	if _, ok := paths["/v1/agents"]; !ok {
		t.Error("served doc missing /v1/agents")
	}
}

// TestSwaggerUIServed confirms the Swagger UI HTML is served and points at the
// overridden spec URL.
func TestSwaggerUIServed(t *testing.T) {
	srv := httptest.NewServer(swaggerOnlyRouter())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/swagger/index.html")
	if err != nil {
		t.Fatalf("GET /swagger/index.html: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index.html status = %d, want 200", resp.StatusCode)
	}
	// http-swagger JS-escapes the configured spec URL in the HTML
	// (`url: "\/swagger\/doc.json"`), so match on the escaped form.
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `\/swagger\/doc.json`) {
		t.Error("Swagger UI HTML does not reference the overridden /swagger/doc.json spec")
	}
	if !strings.Contains(string(body), `swagger-dark-theme`) {
		t.Error("Swagger UI HTML does not include the dark theme")
	}
}
