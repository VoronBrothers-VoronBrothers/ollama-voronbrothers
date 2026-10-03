package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

const maxImageBytes = 20 * 1024 * 1024 // 20 MB

var supportedImageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true,
	".webp": true, ".gif": true, ".bmp": true,
}

// ViewImage lets a multimodal model "see" an image file by path.
type ViewImage struct{}

func (v *ViewImage) Name() string { return "view_image" }

func (v *ViewImage) Description() string {
	return "Load an image from a file path and attach it so you can see its contents. Use this when the user references an image by path."
}

func (v *ViewImage) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("path", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "File path to the image (relative, absolute, or with ~).",
	})
	return api.ToolFunction{
		Name:        v.Name(),
		Description: v.Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"path"},
		},
	}
}

func (v *ViewImage) RequiresApproval(map[string]any) bool { return false }

func (v *ViewImage) Execute(_ context.Context, toolCtx agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return agent.ToolResult{}, fmt.Errorf("path parameter is required")
	}

	absPath, err := normalizeToolPath(toolCtx.WorkingDir, path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("cannot access file: %w", err)
	}
	if info.IsDir() {
		return agent.ToolResult{}, fmt.Errorf("path is a directory, not an image")
	}
	if info.Size() > maxImageBytes {
		return agent.ToolResult{}, fmt.Errorf("image too large (%d bytes, max %d)", info.Size(), maxImageBytes)
	}

	ext := strings.ToLower(filepath.Ext(absPath))
	if !supportedImageExts[ext] {
		// Allow anyway — some models accept any binary; just warn in content.
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return agent.ToolResult{}, fmt.Errorf("cannot read image: %w", err)
	}

	return agent.ToolResult{
		Content:    fmt.Sprintf("Image loaded: %s (%d bytes)", absPath, len(data)),
		WorkingDir: toolCtx.WorkingDir,
		Images:     []api.ImageData{data},
	}, nil
}
