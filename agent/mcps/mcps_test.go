package mcps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

func TestSchemaToParams(t *testing.T) {
	schema := map[string]any{
		"type":        "object",
		"description": "Fetch a page.",
		"properties": map[string]any{
			"url":   map[string]any{"type": "string"},
			"depth": map[string]any{"type": []any{"integer", "number"}},
			"opts":  map[string]any{"type": "object", "properties": map[string]any{"tag": map[string]any{"type": "string"}}},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"mode":  map[string]any{"enum": []any{"a", "b"}, "type": "string"},
		},
		"required": []any{"url"},
		"$defs":    map[string]any{"common": "x"},
	}

	p := schemaToParams(schema)
	if p.Type != "object" {
		t.Fatalf("Type = %q, want object", p.Type)
	}
	if len(p.Required) != 1 || p.Required[0] != "url" {
		t.Fatalf("Required = %v, want [url]", p.Required)
	}
	props := p.Properties.ToMap()
	urlP := props["url"]
	if len(urlP.Type) != 1 || urlP.Type[0] != "string" {
		t.Fatalf("url type = %v, want string", urlP.Type)
	}
	depthP := props["depth"]
	if len(depthP.Type) != 2 {
		t.Fatalf("depth types = %v, want [integer number]", depthP.Type)
	}
	optsP := props["opts"]
	if optsP.Properties == nil || optsP.Properties.ToMap()["tag"].Type[0] != "string" {
		t.Fatal("nested properties not mapped")
	}
	tagsP := props["tags"]
	if tagsP.Items == nil {
		t.Fatal("items not preserved")
	}
	modeP := props["mode"]
	if len(modeP.Enum) != 2 {
		t.Fatalf("enum = %v, want [a b]", modeP.Enum)
	}
	if p.Defs == nil {
		t.Fatal("$defs dropped")
	}

	if p2 := schemaToParams("just a string"); p2.Type != "object" {
		t.Fatalf("degraded schema type = %q, want object", p2.Type)
	}
}

func writeConfig(t *testing.T, servers ...map[string]any) string {
	t.Helper()
	cfg := map[string]any{"servers": servers}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mockStdioCommand(t *testing.T) (command string, args []string) {
	t.Helper()
	// The mock server lives in this module; `go run` compiles it on the fly.
	return "go", []string{"run", "./internal/mocksrv"}
}

func TestConnectStdio(t *testing.T) {
	command, args := mockStdioCommand(t)
	path := writeConfig(t,
		map[string]any{"name": "srv", "command": command, "args": args},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := ConnectAll(ctx, path)
	if err != nil {
		t.Fatalf("ConnectAll: %v", err)
	}
	defer pool.Close()

	if !pool.HasServers() {
		t.Fatal("no live MCP servers")
	}

	registry := &agent.Registry{}
	pool.RegisterTo(registry)
	names := registry.Names()
	if len(names) != 2 || !contains(names, "echo") || !contains(names, "boom") {
		t.Fatalf("registered tools = %v, want [echo boom]", names)
	}

	echo, _ := registry.Get("echo")
	res, err := echo.Execute(ctx, agent.ToolContext{}, map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("Execute(echo): %v", err)
	}
	if !strings.Contains(res.Content, "echo: hello") {
		t.Fatalf("echo result = %q, want to contain 'echo: hello'", res.Content)
	}

	boom, _ := registry.Get("boom")
	res, err = boom.Execute(ctx, agent.ToolContext{}, map[string]any{})
	if err != nil {
		t.Fatalf("Execute(boom): %v", err)
	}
	if !strings.HasPrefix(res.Content, "MCP tool returned an error:") || !strings.Contains(res.Content, "boom failed") {
		t.Fatalf("boom result = %q, want error prefix + 'boom failed'", res.Content)
	}

	// Default approval is on (via the ApprovalRequired interface).
	if !agent.ToolRequiresApproval(echo, map[string]any{}) {
		t.Fatal("MCP tools should require approval by default")
	}
}

func TestConnectHTTP(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "mock-http", Version: "v0.1.0"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "add",
		Description: "Add two integers.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"a": map[string]any{"type": "integer"}, "b": map[string]any{"type": "integer"}},
			"required":   []any{"a", "b"},
		},
	}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			A int `json:"a"`
			B int `json:"b"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "bad args"}}}, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strconv.Itoa(in.A + in.B)}}}, nil
	})

	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{JSONResponse: true}))
	defer httpServer.Close()

	path := writeConfig(t,
		map[string]any{"name": "http", "url": httpServer.URL},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := ConnectAll(ctx, path)
	if err != nil {
		t.Fatalf("ConnectAll: %v", err)
	}
	defer pool.Close()

	registry := &agent.Registry{}
	pool.RegisterTo(registry)
	add, ok := registry.Get("add")
	if !ok {
		t.Fatal("tool 'add' not registered")
	}
	res, err := add.Execute(ctx, agent.ToolContext{}, map[string]any{"a": 2, "b": 3})
	if err != nil {
		t.Fatalf("Execute(add): %v", err)
	}
	if res.Content != "5" {
		t.Fatalf("add result = %q, want \"5\"", res.Content)
	}

	schema := add.Schema()
	props := schema.Parameters.Properties.ToMap()
	if len(props["a"].Type) != 1 || props["a"].Type[0] != "integer" {
		t.Fatalf("add schema type = %v, want integer", props["a"].Type)
	}
}

func TestNameCollisionGetsPrefixed(t *testing.T) {
	command, args := mockStdioCommand(t)
	path := writeConfig(t,
		map[string]any{"name": "srvA", "command": command, "args": args},
		map[string]any{"name": "srvB", "command": command, "args": args},
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := ConnectAll(ctx, path)
	if err != nil {
		t.Fatalf("ConnectAll: %v", err)
	}
	defer pool.Close()

	registry := &agent.Registry{}
	// Pre-register a colliding built-in-style name.
	registry.Register(fakeTool{"boom"})
	pool.RegisterTo(registry)

	names := registry.Names()
	if !contains(names, "echo") || !contains(names, "srvB_echo") || !contains(names, "srvA_boom") || !contains(names, "srvB_boom") {
		t.Fatalf("names = %v, want echo + prefixed echoes and booms", names)
	}
}

func TestLoadMissingConfig(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("missing config should not error: %v", err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("servers = %d, want 0", len(cfg.Servers))
	}
}

func TestLoadInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"servers":[{"name":"x"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("server without command or url should be rejected")
	}

	if err := os.WriteFile(path, []byte(`{"servers":[{"name":"x","url":"http://ok"}], "require_approval": false}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if cfg.Approval() != false {
		t.Fatal("require_approval=false should disable approval")
	}
}

type fakeTool struct{ n string }

func (f fakeTool) Name() string             { return f.n }
func (f fakeTool) Description() string      { return "fake" }
func (f fakeTool) Schema() api.ToolFunction { return api.ToolFunction{} }
func (f fakeTool) Execute(context.Context, agent.ToolContext, map[string]any) (agent.ToolResult, error) {
	return agent.ToolResult{}, nil
}

// contains is a small helper over []string.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
