package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

// Memory is a long-term memory tool: append notes to today's stream file,
// read files inside the memory directory, or list it. The directory comes
// from OLLAMA_MEMORY_DIR (default: the VB-ollama long-memory folder).
type Memory struct{}

const defaultMemoryDir = "/home/voron/Документы/VB-ollama/vb-ollama-long-memory"

func (m *Memory) Name() string {
	return "memory"
}

func (m *Memory) Description() string {
	return "Long-term memory: append(text), read(path), list."
}

func (m *Memory) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("action", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "append, read, or list.",
	})
	props.Set("text", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Note to record (action=append).",
	})
	props.Set("path", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "File in the memory dir (action=read).",
	})
	return api.ToolFunction{
		Name:        m.Name(),
		Description: m.Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"action"},
		},
	}
}

func (m *Memory) RequiresApproval(map[string]any) bool {
	return true
}

func (m *Memory) Execute(ctx context.Context, toolCtx agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	action, ok := args["action"].(string)
	if !ok || strings.TrimSpace(action) == "" {
		return agent.ToolResult{}, fmt.Errorf("action parameter is required")
	}

	dir := os.Getenv("OLLAMA_MEMORY_DIR")
	if dir == "" {
		dir = defaultMemoryDir
	}
	dir, err := canonicalPath(dir)
	if err != nil {
		return agent.ToolResult{}, err
	}

	switch action {
	case "append":
		text, ok := args["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return agent.ToolResult{}, fmt.Errorf("text parameter is required for action=append")
		}
		return m.append(dir, text)
	case "read":
		path, ok := args["path"].(string)
		if !ok || strings.TrimSpace(path) == "" {
			return agent.ToolResult{}, fmt.Errorf("path parameter is required for action=read")
		}
		abs := filepath.Join(dir, strings.TrimPrefix(strings.TrimSpace(path), "/"))
		if err := rejectFinalSymlink(abs); err != nil {
			return agent.ToolResult{}, err
		}
		file, _, err := openRegularFile(abs)
		if err != nil {
			return agent.ToolResult{}, err
		}
		defer file.Close()
		content, err := readAllWithinLimit(file, maxReadBytes)
		return agent.ToolResult{Content: string(content)}, err
	case "list":
		return m.list(dir)
	default:
		return agent.ToolResult{}, fmt.Errorf("unknown action %q (expected append, read or list)", action)
	}
}

func (m *Memory) append(dir, text string) (agent.ToolResult, error) {
	now := time.Now()
	dayDir := filepath.Join(dir, "stream")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		return agent.ToolResult{}, err
	}
	path := filepath.Join(dayDir, now.Format("2006-01-02")+".md")

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return agent.ToolResult{}, err
	}
	defer file.Close()

	entry := fmt.Sprintf("\n## %s\n%s\n", now.Format("15:04"), strings.TrimRight(text, "\n"))
	if _, err := file.WriteString(entry); err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: fmt.Sprintf("Recorded to %s (%d chars).", path, len(text))}, file.Sync()
}

func (m *Memory) list(dir string) (agent.ToolResult, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return agent.ToolResult{}, err
	}
	var sb strings.Builder
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		sb.WriteString(name)
		sb.WriteByte('\n')
	}
	lines := strings.Split(strings.TrimSpace(sb.String()), "\n")
	sort.Strings(lines)
	return agent.ToolResult{Content: strings.Join(lines, "\n")}, nil
}
