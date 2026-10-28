package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// setupTestProvider writes a providers.json with one external provider whose
// base URL points at the given test server and loads it.
func setupTestProvider(t *testing.T, baseURL string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, providersFileName)
	config := fmt.Sprintf(`{"providers":[{"name":"testprov","base_url":%q,"api_key":"secret-key","models":["anthropic/claude-haiku-5-5"]}]}`, baseURL)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write providers config: %v", err)
	}

	t.Setenv(providersEnvName, path)
	providersMu.Lock()
	loadedProviders = nil
	providersLoaded = false
	providersMu.Unlock()
	t.Cleanup(func() {
		providersMu.Lock()
		loadedProviders = nil
		providersLoaded = false
		providersMu.Unlock()
	})

	loadProviders()
}

// TestExternalMessagesProxyPassthrough verifies that /v1/messages traffic for
// external providers is forwarded raw: only the model field is replaced and
// every other field (notably cache_control) arrives at the provider untouched,
// while the Anthropic-format response streams back as-is.
func TestExternalMessagesProxyPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("upstream path = %q, want /messages", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-key" {
			t.Errorf("Authorization = %q", got)
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"type":"message","usage":{"input_tokens":10,"cache_creation_input_tokens":5}}`)
	}))
	defer upstream.Close()

	setupTestProvider(t, upstream.URL)

	router := gin.New()
	router.POST("/v1/messages", externalMessagesProxyMiddleware())

	reqBody := `{
	  "model": "testprov/anthropic/claude-haiku-5-5",
	  "max_tokens": 10,
	  "system": [{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}],
	  "messages": [{"role":"user","content":[{"type":"text","text":"привет"}]}]
	}`

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(reqBody))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Errorf("response Content-Type = %q, want passthrough of upstream header", got)
	}
	wantResp := `{"type":"message","usage":{"input_tokens":10,"cache_creation_input_tokens":5}}`
	if w.Body.String() != wantResp {
		t.Errorf("response body = %s, want raw upstream response", w.Body.String())
	}

	var sent map[string]any
	if err := json.Unmarshal(gotBody, &sent); err != nil {
		t.Fatalf("upstream got invalid JSON: %v (body: %s)", err, gotBody)
	}
	if model, _ := sent["model"].(string); model != "anthropic/claude-haiku-5-5" {
		t.Errorf("model = %v, want provider prefix stripped", sent["model"])
	}
	sys, ok := sent["system"].([]any)
	if !ok || len(sys) == 0 {
		t.Fatalf("system not forwarded: %s", gotBody)
	}
	block, _ := sys[0].(map[string]any)
	cc, hasCC := block["cache_control"]
	if !hasCC {
		t.Errorf("cache_control was dropped in passthrough: %s", gotBody)
	} else if fmt.Sprintf("%v", cc) != "map[type:ephemeral]" {
		t.Errorf("cache_control = %v, want {type: ephemeral}", cc)
	}
	if !strings.Contains(string(gotBody), `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("raw cache_control lost in transit: %s", gotBody)
	}
}

// TestExternalMessagesProxyIgnoresNonExternalModels verifies that requests for
// local models are left alone (c.Next) and never reach the upstream.
func TestExternalMessagesProxyIgnoresNonExternalModels(t *testing.T) {
	gin.SetMode(gin.TestMode)

	called := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer upstream.Close()

	setupTestProvider(t, upstream.URL)

	router := gin.New()
	var passedThrough bool
	router.POST("/v1/messages", externalMessagesProxyMiddleware(), func(c *gin.Context) {
		passedThrough = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"llama3:8b","messages":[]}`))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if called {
		t.Error("upstream was contacted for a non-external model")
	}
	if !passedThrough {
		t.Error("non-external request should fall through to the next handler")
	}
}
