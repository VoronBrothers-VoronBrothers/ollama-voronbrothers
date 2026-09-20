// Package mcps connects external MCP servers and registers their tools
// in the agent TUI alongside the built-in tools.
package mcps

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ServerConfig describes one MCP server: either a local command (stdio)
// or an HTTP endpoint (streamable HTTP).
type ServerConfig struct {
	Name    string            `json:"name"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
}

// Config is the content of ~/.ollama/mcp.json.
type Config struct {
	Servers         []ServerConfig `json:"servers"`
	RequireApproval *bool          `json:"require_approval,omitempty"` // default: true
}

// Approval reports whether MCP tools ask for user approval before use.
func (c *Config) Approval() bool {
	if c == nil || c.RequireApproval == nil {
		return true
	}
	return *c.RequireApproval
}

// DefaultPath is the location of the MCP config file.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ollama/mcp.json"
	}
	return filepath.Join(home, ".ollama", "mcp.json")
}

// Load reads and validates the MCP config file. A missing file is not an
// error: it means no external MCP servers are configured.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("read mcp config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse mcp config %s: %w", path, err)
	}
	for i, s := range cfg.Servers {
		if s.Name == "" {
			cfg.Servers[i].Name = fmt.Sprintf("server%d", i+1)
		}
		if (s.Command == "") == (s.URL == "") {
			return nil, fmt.Errorf("mcp server %q must define either \"command\" or \"url\"", s.Name)
		}
	}
	return &cfg, nil
}
