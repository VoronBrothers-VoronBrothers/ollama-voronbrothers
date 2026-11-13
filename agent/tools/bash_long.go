package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

// bashLongName returns the platform-appropriate tool name:
// "bash_long" on Unix, "powershell_long" on Windows.
func bashLongName() string { return shellToolName() + "_long" }

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
	mu        sync.Mutex
	lastRunAt time.Time // zero value means "never ran"
}

// NewBashLong builds a BashLong with the default long timeout configured.
func NewBashLong() *BashLong {
	return &BashLong{Bash: &Bash{Timeout: bashLongTimeout}}
}

func (b *BashLong) Name() string {
	return bashLongName()
}

// Execute runs the embedded Bash with a per-call timeout taken from the optional
// `timeout` argument in seconds (60-400; default 120 when absent or non-positive).
// The deadline is only an upper bound: the result returns as soon as the command
// finishes. A value copy of the shared *Bash is used so concurrent calls do not
// race on state.
//
// On completion the result content is prefixed with a timing line containing:
//   - current UTC wall-clock time
//   - how long this command took to execute
//   - elapsed time since the previous bash_long call (if any)
func (b *BashLong) Execute(ctx context.Context, toolCtx agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	t := b.callTimeout(args)
	lb := *b.Bash // value copy (holds only Timeout): safe under concurrent calls
	lb.Timeout = t

	start := time.Now()
	runResult, err := lb.Execute(ctx, toolCtx, args)
	end := time.Now()

	// Build the timing prefix.
	var prevDelta string
	b.mu.Lock()
	if !b.lastRunAt.IsZero() {
		d := b.lastRunAt.Sub(start) // time between previous call's start and this one's start
		if d < 0 {
			d = 0
		}
		prevDelta = fmt.Sprintf(" | Since last bash_long: %s", formatDuration(d))
	}
	b.lastRunAt = end
	b.mu.Unlock()

	elapsed := end.Sub(start)
	prefix := fmt.Sprintf("[Time: %s | Duration: %s%s]\n",
		end.UTC().Format(time.RFC3339),
		formatDuration(elapsed),
		prevDelta,
	)

	runResult.Content = prefix + runResult.Content
	return runResult, err
}

// callTimeout resolves the effective deadline for one call: the `timeout`
// argument (seconds) clamped to [bashLongMinTimeout, bashLongMaxTimeout], or the
// configured default (bashLongTimeout) when the argument is absent, non-numeric
// or non-positive. Kept separate from Execute so the bounds are testable without
// spawning a shell.
func (b *BashLong) callTimeout(args map[string]any) time.Duration {
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
	return t
}

// formatDuration renders a duration in a compact human-friendly form for the
// model (e.g. "3.2s", "1m 15s"). It avoids sub-second noise (< 10 ms → "0s")
// and keeps values under an hour readable.
func formatDuration(d time.Duration) string {
	if d < 10*time.Millisecond {
		return "<10ms"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", int64(d/time.Microsecond)/1000)
	}
	m := int64(d / time.Minute)
	s := float64((d % time.Minute) / time.Millisecond) / 1000.0
	if m == 0 {
		return fmt.Sprintf("%.1fs", s)
	}
	return fmt.Sprintf("%dm %ds", m, int(s))
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
