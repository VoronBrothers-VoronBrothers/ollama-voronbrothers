package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

// SearchFiles — быстрый поиск текста внутри файлов с помощью ripgrep.
// Поддерживает Go, Python, JS, MD, YAML и любые другие текстовые файлы.
// Использует rg --json для чистого вывода без мусора.
type SearchFiles struct{}

func (SearchFiles) Name() string { return "search_files" }

func (SearchFiles) Description() string {
	return "Fast text search across files using ripgrep. Returns matching lines with file paths and line numbers. " +
		"Excellent for searching code (Go, Python, etc.), configs and documentation."
}

func (SearchFiles) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("pattern", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Regular expression pattern to search for (required).",
	})
	props.Set("path", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Directory or file path to search in. Defaults to current working directory.",
	})
	props.Set("glob", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Optional glob pattern to filter files (e.g. '*.go', '*.py', '*.md').",
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
	if strings.TrimSpace(pattern) == "" {
		return agent.ToolResult{Content: "Error: 'pattern' cannot be empty"}, nil
	}

	path, _ := args["path"].(string)
	glob, _ := args["glob"].(string)

	return searchFiles(pattern, path, glob)
}

func searchFiles(pattern, path, glob string) (agent.ToolResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	cmdArgs := []string{"--json", "--max-count=40", "-e", pattern}
	if path != "" {
		cmdArgs = append(cmdArgs, path)
	}
	if glob != "" {
		cmdArgs = append(cmdArgs, "--glob", glob)
	}

	cmd := exec.CommandContext(ctx, "rg", cmdArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return agent.ToolResult{Content: "Search timed out (25 seconds)."}, nil
		}
		// ripgrep возвращает код 1, если ничего не найдено — это нормально
		if stdout.Len() == 0 {
			return agent.ToolResult{Content: fmt.Sprintf("No matches found for: %s", pattern)}, nil
		}
	}

	if stdout.Len() == 0 {
		return agent.ToolResult{Content: fmt.Sprintf("No matches found for: %s", pattern)}, nil
	}

	result := parseRgJSON(stdout.Bytes())
	return agent.ToolResult{Content: result}, nil
}

// parseRgJSON превращает JSON-вывод rg в удобный для модели текст.
func parseRgJSON(data []byte) string {
	var buf bytes.Buffer
	buf.WriteString("=== Search results ===\n\n")

	lines := bytes.Split(data, []byte{'\n'})
	count := 0
	maxResults := 25

	for _, line := range lines {
		if len(line) == 0 {
			continue
		}

		var entry struct {
			Type string `json:"type"`
			Data struct {
				Path       struct{ Text string } `json:"path"`
				LineNumber int                    `json:"line_number"`
				Lines      struct{ Text string } `json:"lines"`
			} `json:"data"`
		}

		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		if entry.Type == "match" && entry.Data.Path.Text != "" {
			count++
			fmt.Fprintf(&buf, "%s:%d: %s\n",
				entry.Data.Path.Text,
				entry.Data.LineNumber,
				strings.TrimSpace(entry.Data.Lines.Text))

			if count >= maxResults {
				buf.WriteString("\n... and more results (limited to 25)")
				break
			}
		}
	}

	if count == 0 {
		return "No matches found."
	}

	buf.WriteString(fmt.Sprintf("\n\nFound %d matches.", count))
	return buf.String()
}
