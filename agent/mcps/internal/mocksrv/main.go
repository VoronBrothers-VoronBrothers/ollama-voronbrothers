// Package main is a minimal MCP server over stdio used by mcps tests.
package main

import (
	"context"
	"encoding/json"
	"os"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	srv := mcp.NewServer(&mcp.Implementation{Name: "mock-stdio", Version: "v0.1.0"}, nil)

	echoSchema := map[string]any{
		"type":        "object",
		"description": "Echo a string.",
		"properties":  map[string]any{"text": map[string]any{"type": "string"}},
		"required":    []any{"text"},
	}

	srv.AddTool(&mcp.Tool{Name: "echo", Description: "Echo the input text.", InputSchema: echoSchema},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var in struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(req.Params.Arguments, &in)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "echo: " + in.Text}}}, nil
		})

	srv.AddTool(&mcp.Tool{Name: "boom", Description: "Always fails.", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "boom failed"}}, IsError: true}, nil
		})

	if err := srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(1)
	}
}
