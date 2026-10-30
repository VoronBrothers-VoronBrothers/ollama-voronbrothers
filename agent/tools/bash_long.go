package tools

import (
	"context"
	"time"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

const bashLongToolName = "bash_long"

// Per-call bounds for the optional `timeout` argument of bash_long.
// When the argument is absent (or non-positive) the default bashLongTimeout
// (120 s) is used. BASH-LONG-BOUNDS: менять диапазон здесь.
const (
	bashLongMinTimeout = 60 * time.Second
	bashLongMaxTimeout = 400 * time.Second // ~6-7 minutes
)

// BashLong is a parallel shell tool identical to Bash in behaviour, but with a
// configurable deadline. Without a `timeout` argument it runs for up to 120 s
// (vs. the fixed 40 s of `bash`); the caller may raise that up to 400 s. It exists
// so the model can wait on longer commands (builds, large downloads) without
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
// `timeout` argument in seconds (60-400; default 120 when absent or non-positive).
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
	return "Run a shell command with an optional 'timeout' argument in seconds (60-400; default 120) for long-running jobs. Returns as soon as the command finishes, with combined output and the final working directory."
}

func (b *BashLong) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("command", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "The command to execute.",
	})
	props.Set("timeout", api.ToolProperty{
		Type:        api.PropertyType{"integer"},
		Description: "Optional upper bound in seconds (60-400, default 120). Set it to the expected duration of the command; do not blindly use the maximum.",
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
