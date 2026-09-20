package tools

import (
	"context"
	"time"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

const bashLongToolName = "bash_long"

// Per-call bounds for the optional `timeout` argument of bash_long.
// The default (no argument) stays at bashLongTimeout, 10 minutes.
const (
	// Per-call bounds for the optional `timeout` argument of bash_long.
	// The default (no argument) stays at bashLongTimeout, 10 minutes.
	bashLongMinTimeout = 30 * time.Second
	bashLongMaxTimeout = 1800 * time.Second // 30 minutes
)

// BashLong is a parallel shell tool identical to Bash in behaviour, but with an
// extended, configurable deadline (default 10 minutes). It exists so the model
// can wait on long-running commands (GPU training, large downloads) without
// hitting the short 40s timeout of `bash`. The whole execution path is reused
// from the embedded *Bash; only identity and the effective timeout differ.
type BashLong struct {
	*Bash
}

// NewBashLong builds a BashLong with the default long timeout configured.
func NewBashLong() *BashLong {
	return &BashLong{Bash: &Bash{Timeout: bashLongTimeout}}
}

func (b *BashLong) Name() string {
	return bashLongToolName
}

// Execute runs the embedded Bash with a per-call timeout taken from the optional
// `timeout` argument in seconds (30-1800; default 600 when absent or non-positive).
// The deadline is only an upper bound: the result returns as soon as the command
// finishes. A value copy of the shared *Bash is used so concurrent calls do not
// race on state.
func (b *BashLong) Execute(ctx context.Context, toolCtx agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	t := b.effectiveTimeout()
	if v, ok := args["timeout"]; ok {
		var secs int
		switch x := v.(type) {
		case float64:
			secs = int(x)
		case int:
			secs = x
		}
		if secs > 0 {
			t = time.Duration(secs) * time.Second
			if t < bashLongMinTimeout {
				t = bashLongMinTimeout
			} else if t > bashLongMaxTimeout {
				t = bashLongMaxTimeout
			}
		}
	}
	lb := *b.Bash // value copy (holds only Timeout): safe under concurrent calls
	lb.Timeout = t
	return lb.Execute(ctx, toolCtx, args)
}

func (b *BashLong) Description() string {
	return "Run a shell command with a configurable LONG timeout. Identical to the bash tool but accepts an optional 'timeout' argument in seconds (30-1800; default 600). " +
		"Pick a timeout matched to how long the command is expected to take — do NOT default to the maximum: use short values for quick waits, longer only when genuinely needed " +
		"(GPU training, big downloads, full builds). The result returns as soon as the command finishes; the timeout is an upper bound, not a fixed wait. " +
		"Returns combined output and the final working directory."
}

func (b *BashLong) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("command", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "The command to execute.",
	})
	props.Set("timeout", api.ToolProperty{
		Type:        api.PropertyType{"integer"},
		Description: "Optional upper bound in seconds (30-1800, default 600). Set it to the expected duration of the command; do not blindly use the maximum.",
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
