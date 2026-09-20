package mcps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

// MCPTool wraps a remote tool of one MCP server so it satisfies the local
// agent.Tool interface.
type MCPTool struct {
	serverName      string // MCP server the tool belongs to
	name            string // registry name (prefixed on collisions)
	remoteName      string // original name used in CallTool
	description     string
	params          api.ToolFunctionParameters
	requireApproval bool
	session         *mcp.ClientSession
}

func newMCPTool(server ServerConfig, t *mcp.Tool, requireApproval bool) *MCPTool {
	desc := t.Description
	if desc == "" {
		desc = "No description provided."
	}
	return &MCPTool{
		serverName:      server.Name,
		name:            t.Name,
		remoteName:      t.Name,
		description:     fmt.Sprintf("Tool %q of MCP server %q. %s", t.Name, server.Name, desc),
		params:          schemaToParams(t.InputSchema),
		requireApproval: requireApproval,
		session:         nil, // set by the pool after connection
	}
}

// Name implements agent.Tool.
func (t *MCPTool) Name() string { return t.name }

// Description implements agent.Tool.
func (t *MCPTool) Description() string { return t.description }

// Schema implements agent.Tool.
func (t *MCPTool) Schema() api.ToolFunction {
	return api.ToolFunction{Name: t.name, Description: t.description, Parameters: t.params}
}

// RequiresApproval implements agent.ApprovalRequired.
func (t *MCPTool) RequiresApproval(_ map[string]any) bool { return t.requireApproval }

// Execute calls the remote MCP tool and converts the result to a local ToolResult.
func (t *MCPTool) Execute(ctx context.Context, _ agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	res, err := t.session.CallTool(ctx, &mcp.CallToolParams{Name: t.remoteName, Arguments: args})
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("call mcp tool %q on server %q: %w", t.name, t.serverName, err)
	}

	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		} else if raw, merr := json.Marshal(c); merr == nil && len(raw) > 0 {
			parts = append(parts, string(raw))
		}
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		if b, merr := json.Marshal(res.StructuredContent); merr == nil {
			parts = append(parts, string(b))
		} else if s, ok := res.StructuredContent.(string); ok {
			parts = append(parts, s)
		}
	}
	content := strings.Join(parts, "\n")
	if content == "" {
		content = "(no output)"
	}
	if res.IsError {
		content = "MCP tool returned an error:\n" + content
	}
	return agent.ToolResult{Content: content}, nil
}

// schemaToParams converts the JSON Schema of a remote MCP tool into the local
// api.ToolFunctionParameters representation. Only the commonly used schema
// constructs are mapped (type, description, properties, required, items,
// enum, anyOf, $defs); exotic schemas degrade to a minimal "object" shape.
func schemaToParams(raw any) api.ToolFunctionParameters {
	m := asSchemaMap(raw)

	props := api.NewToolPropertiesMap()
	if v, ok := m["properties"]; ok {
		if pm, ok := v.(map[string]any); ok {
			for k, val := range pm {
				props.Set(k, convertValue(val))
			}
		}
	}

	p := api.ToolFunctionParameters{Type: "object", Properties: props}
	if req, ok := m["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				p.Required = append(p.Required, s)
			}
		}
	}
	if defs, ok := m["$defs"]; ok && defs != nil {
		p.Defs = defs
	}
	return p
}

func asSchemaMap(raw any) map[string]any {
	if v, ok := raw.(map[string]any); ok {
		return v
	}
	// Not an object schema (e.g. a JSON string or array): degrade to "object".
	return map[string]any{}
}

func convertValue(v any) api.ToolProperty {
	p := api.ToolProperty{}
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return p
	}
	switch tval := m["type"].(type) {
	case string:
		p.Type = api.PropertyType{tval}
	case []any:
		for _, item := range tval {
			if s, ok := item.(string); ok {
				p.Type = append(p.Type, s)
			}
		}
	}
	if d, ok := m["description"].(string); ok {
		p.Description = d
	}
	if items, ok := m["items"]; ok && items != nil {
		p.Items = items
	}
	if enum, ok := m["enum"].([]any); ok {
		p.Enum = enum
	}
	if req, ok := m["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				p.Required = append(p.Required, s)
			}
		}
	}
	if subs, ok := m["properties"]; ok && subs != nil {
		if sm, ok := subs.(map[string]any); ok {
			pm := api.NewToolPropertiesMap()
			for k, val := range sm {
				pm.Set(k, convertValue(val))
			}
			p.Properties = pm
		}
	}
	if anyOf, ok := m["anyOf"].([]any); ok {
		for _, item := range anyOf {
			p.AnyOf = append(p.AnyOf, convertValue(item))
		}
	}
	return p
}
