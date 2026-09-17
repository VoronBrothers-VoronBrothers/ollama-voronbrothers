package tools

import (
	"github.com/ollama/ollama/api"
)

const bashLongToolName = "bash_long"

// BashLong is a parallel shell tool identical to Bash in behaviour, but with an
// extended deadline (10 minutes). It exists so the model can wait on long-running
// commands (GPU training, large downloads) without hitting the short 40s timeout.
// The whole execution path is reused from the embedded *Bash; only the identity
// (Name/Description/Schema) and the effective timeout differ.
type BashLong struct {
	*Bash
}

// NewBashLong builds a BashLong with the long timeout configured.
func NewBashLong() *BashLong {
	return &BashLong{Bash: &Bash{Timeout: bashLongTimeout}}
}

func (b *BashLong) Name() string {
	return bashLongToolName
}

func (b *BashLong) Description() string {
	return "Run a shell command with a LONG timeout (10 minutes). Identical to the bash tool " +
		"but for long-running commands such as GPU training, large downloads or other tasks that " +
		"exceed 40 seconds. Returns combined output and the final working directory."
}

func (b *BashLong) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("command", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "The command to execute.",
	})
	return api.ToolFunction{
		Name:        b.Name(),
		Description: b.Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"command"},
		},
	}
}
