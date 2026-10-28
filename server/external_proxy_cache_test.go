package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakeSSEProvider serves an OpenAI-style stream. The usage chunk comes AFTER
// the finish_reason chunk, as the real API does with include_usage.
func fakeSSEProvider(t *testing.T, usageJSON string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// The proxy must ask the provider for usage.
		if req.StreamOptions == nil || !req.StreamOptions.IncludeUsage {
			t.Errorf("request missing stream_options.include_usage")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		if usageJSON != "" {
			fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":%s}\n\n", usageJSON)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// runExternalChat sends a chat through handleExternalChat and returns the
// parsed NDJSON lines.
func runExternalChat(t *testing.T, baseURL string) []ollamaChatResponse {
	t.Helper()
	setupTestProvider(t, baseURL)
	gin.SetMode(gin.TestMode)

	ref, ok := tryParseExternalModel("testprov/anthropic/claude-haiku-5-5")
	if !ok {
		t.Fatal("model ref not parsed")
	}
	body := []byte(`{"model":"testprov/anthropic/claude-haiku-5-5","stream":true,"messages":[{"role":"user","content":"hello"}]}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/chat", strings.NewReader(string(body)))
	c.Request.Header.Set("Content-Type", "application/json")

	handleExternalChat(c, ref, body)

	var lines []ollamaChatResponse
	for _, l := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		if l == "" {
			continue
		}
		var r ollamaChatResponse
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatalf("bad ndjson line %q: %v", l, err)
		}
		lines = append(lines, r)
	}
	return lines
}

func doneLine(t *testing.T, lines []ollamaChatResponse) ollamaChatResponse {
	t.Helper()
	for _, l := range lines {
		if l.Done {
			return l
		}
	}
	t.Fatal("no done line in response")
	return ollamaChatResponse{}
}

func TestExternalChatReportsCachedTokensOpenAI(t *testing.T) {
	srv := fakeSSEProvider(t, `{"prompt_tokens":1200,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":1024}}`)
	defer srv.Close()

	done := doneLine(t, runExternalChat(t, srv.URL))

	if done.PromptEvalCount != 1200 {
		t.Errorf("PromptEvalCount = %d, want 1200", done.PromptEvalCount)
	}
	if done.PromptEvalCachedCount == nil {
		t.Fatal("PromptEvalCachedCount is nil, want 1024")
	}
	if *done.PromptEvalCachedCount != 1024 {
		t.Errorf("PromptEvalCachedCount = %d, want 1024", *done.PromptEvalCachedCount)
	}
}

func TestExternalChatReportsCachedTokensDeepSeek(t *testing.T) {
	srv := fakeSSEProvider(t, `{"prompt_tokens":800,"completion_tokens":3,"prompt_cache_hit_tokens":512,"prompt_cache_miss_tokens":288}`)
	defer srv.Close()

	done := doneLine(t, runExternalChat(t, srv.URL))

	if done.PromptEvalCachedCount == nil || *done.PromptEvalCachedCount != 512 {
		t.Fatalf("PromptEvalCachedCount = %v, want 512", done.PromptEvalCachedCount)
	}
}

func TestExternalChatOmitsCachedTokensWhenProviderSilent(t *testing.T) {
	// Usage without any cache field: the count must stay nil, not become 0,
	// so the benchmark prints "cached: unavailable".
	srv := fakeSSEProvider(t, `{"prompt_tokens":300,"completion_tokens":4}`)
	defer srv.Close()

	lines := runExternalChat(t, srv.URL)
	done := doneLine(t, lines)

	if done.PromptEvalCachedCount != nil {
		t.Errorf("PromptEvalCachedCount = %d, want nil when provider sends no cache data", *done.PromptEvalCachedCount)
	}
	if done.PromptEvalCount != 300 {
		t.Errorf("PromptEvalCount = %d, want 300", done.PromptEvalCount)
	}
}

func TestExternalChatDoneLineAfterUsage(t *testing.T) {
	// Regression: the stream used to stop at finish_reason, so usage that
	// arrives after it was lost. The done line must still carry it.
	srv := fakeSSEProvider(t, `{"prompt_tokens":42,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":10}}`)
	defer srv.Close()

	lines := runExternalChat(t, srv.URL)
	if len(lines) == 0 || !lines[len(lines)-1].Done {
		t.Fatal("last line must be the done line")
	}
	if got := lines[len(lines)-1].PromptEvalCount; got != 42 {
		t.Errorf("PromptEvalCount = %d, want 42 (usage lost after finish_reason)", got)
	}
}
