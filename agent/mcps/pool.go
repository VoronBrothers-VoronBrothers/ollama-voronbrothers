package mcps

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ollama/ollama/agent"
)

// Pool holds live MCP server sessions and their tools. A nil or empty pool
// means no external MCP servers are configured.
type Pool struct {
	config  *Config
	servers []*Server
}

type Server struct {
	cfg     ServerConfig
	session *mcp.ClientSession
	tools   []*MCPTool
}

// ConnectAll loads the config from path (DefaultPath by default is passed in
// by callers) and connects to every configured MCP server. Servers that fail
// to connect are skipped with a warning on stderr; they do not abort the TUI.
func ConnectAll(ctx context.Context, path string) (*Pool, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, fmt.Errorf("mcp: %w", err)
	}
	pool := &Pool{config: cfg}
	approve := cfg.Approval()
	for _, sc := range cfg.Servers {
		srv, err := connectServer(ctx, sc, approve)
		if err != nil {
			fmt.Fprintf(cmdStderr(), "warning: mcp server %q failed to start: %v\n", sc.Name, err)
			continue
		}
		pool.servers = append(pool.servers, srv)
	}
	return pool, nil
}

func cmdStderr() *os.File { return os.Stderr } // small indirection for testability

// HasServers reports whether at least one MCP server is live.
func (p *Pool) HasServers() bool {
	if p == nil {
		return false
	}
	for _, s := range p.servers {
		if len(s.tools) > 0 {
			return true
		}
	}
	return false
}

// RegisterTo adds all live MCP tools to the local agent registry. Tool names
// are prefixed with the server name when they collide across servers or with
// already registered tools.
func (p *Pool) RegisterTo(registry *agent.Registry) {
	if p == nil || registry == nil {
		return
	}
	taken := map[string]bool{}
	for _, n := range registry.Names() {
		taken[n] = true
	}
	for _, s := range p.servers {
		for _, t := range s.tools {
			name := t.remoteName
			if taken[name] {
				name = s.cfg.Name + "_" + t.remoteName
			}
			if taken[name] { // pathological double collision; still unique-ify
				name = fmt.Sprintf("%s_%d", s.cfg.Name, len(taken))
			}
			t.name = name
			taken[name] = true
			registry.Register(t)
		}
	}
}

// Close terminates all live MCP sessions. Errors are combined but not fatal;
// the caller may log them.
func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	var first error
	for _, s := range p.servers {
		if err := s.session.Close(); err != nil && first == nil {
			first = fmt.Errorf("close mcp server %q: %w", s.cfg.Name, err)
		}
	}
	return first
}

func keyValues(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func connectServer(ctx context.Context, cfg ServerConfig, requireApproval bool) (*Server, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "ollama-mcps", Version: "1.0.0"}, nil)

	var transport mcp.Transport
	switch {
	case cfg.Command != "":
		cmd := exec.Command(cfg.Command, cfg.Args...)
		if len(cfg.Env) > 0 {
			cmd.Env = append(cmd.Environ(), keyValues(cfg.Env)...)
		}
		transport = &mcp.CommandTransport{Command: cmd}
	default:
		transport = &mcp.StreamableClientTransport{Endpoint: cfg.URL}
	}

	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %q: %w", cfg.Name, err)
	}

	list, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		session.Close()
		return nil, fmt.Errorf("list tools from %q: %w", cfg.Name, err)
	}

	srv := &Server{cfg: cfg, session: session}
	for _, t := range list.Tools {
		mt := newMCPTool(cfg, t, requireApproval)
		mt.session = session
		srv.tools = append(srv.tools, mt)
	}
	return srv, nil
}
