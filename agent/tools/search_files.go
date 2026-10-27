package tools

import (
	"context"
	"fmt"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

// SearchFiles is a fast text search tool wrapping ripgrep (rg).
//
// Why dedicated tool instead of bash+grep:
//   - Safety: no shell injection risk via pattern/path arguments.
//   - Reliability: clean JSON output, no ANSI codes, no "permission denied" noise.
//   - Speed: direct rg invocation without fork/exec of a shell process.
//
// Note: models trained on code assistants have strong priors toward `grep`/`find`.
// A dedicated tool doesn't eliminate that prior but makes the call simpler
// (one structured function call vs constructing a bash command with flags).
// Until this is implemented, the system prompt can say "use rg via bash".
type SearchFiles struct{}

func (SearchFiles) Name() string { return "search_files" }

func (SearchFiles) Description() string {
	return "Fast text search across files using ripgrep. Returns matching lines with file paths and line numbers."
}

func (SearchFiles) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("pattern", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Regular expression pattern to search for.",
	})
	props.Set("path", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Directory or file path to search in. Defaults to working directory.",
	})
	props.Set("glob", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Optional glob filter for filenames (e.g. '*.go').",
	})
	return api.ToolFunction{
		Name:        "search_files",
		Description: (SearchFiles{}).Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"pattern"},
		},
	}
}

func (SearchFiles) Execute(_ context.Context, _ agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	pattern, _ := args["pattern"].(string)
	path, _ := args["path"].(string)
	glob, _ := args["glob"].(string)

	// TODO: implement with exec.Command("rg", "--json", "-e", pattern, ...)
	// Parse rg JSON output into [{file, line, text}] and return as formatted string.
	// Add timeout (30s), respect .gitignore by default.

	return agent.ToolResult{Content: fmt.Sprintf(
		"[search_files stub] pattern=%q path=%q glob=%q — implementation pending. "+
			"Use `rg` via bash tool until this is ready.",
		pattern, path, glob,
	)}, nil
}
