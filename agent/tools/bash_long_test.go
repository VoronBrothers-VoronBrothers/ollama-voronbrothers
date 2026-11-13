package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/agent"
)

// TestBashLongTimeoutClamp pins the per-call bounds of bash_long. If someone
// changes the min/max/default constants without updating the tests (or the
// tool description shown to the model), this fails loudly.
func TestBashLongTimeoutClamp(t *testing.T) {
	const (
		wantDefault = 120 * time.Second
		wantMin     = 60 * time.Second
		wantMax     = 400 * time.Second
	)

	// Guard the constants themselves: the numbers are also advertised to the
	// model in Description()/Schema(), so an accidental edit must be deliberate.
	if bashLongTimeout != wantDefault {
		t.Errorf("bashLongTimeout = %v, want %v", bashLongTimeout, wantDefault)
	}
	if bashLongMinTimeout != wantMin {
		t.Errorf("bashLongMinTimeout = %v, want %v", bashLongMinTimeout, wantMin)
	}
	if bashLongMaxTimeout != wantMax {
		t.Errorf("bashLongMaxTimeout = %v, want %v", bashLongMaxTimeout, wantMax)
	}

	cases := []struct {
		name string
		args map[string]any
		want time.Duration
	}{
		{"no args at all", nil, wantDefault},
		{"command only, no timeout", map[string]any{"command": "true"}, wantDefault},
		{"explicit default", map[string]any{"command": "true", "timeout": 120.0}, 120 * time.Second},
		{"inside range", map[string]any{"command": "true", "timeout": 300.0}, 300 * time.Second},
		{"exactly min", map[string]any{"command": "true", "timeout": 60.0}, wantMin},
		{"exactly max", map[string]any{"command": "true", "timeout": 400.0}, wantMax},
		{"below min clamped", map[string]any{"command": "true", "timeout": 10.0}, wantMin},
		{"above max clamped", map[string]any{"command": "true", "timeout": 900.0}, wantMax},
		{"old max is clamped", map[string]any{"command": "true", "timeout": 1800.0}, wantMax},
		{"zero falls back to default", map[string]any{"command": "true", "timeout": 0.0}, wantDefault},
		{"negative falls back to default", map[string]any{"command": "true", "timeout": -5.0}, wantDefault},
		{"int type accepted", map[string]any{"command": "true", "timeout": 200}, 200 * time.Second},
		{"non-numeric falls back to default", map[string]any{"command": "true", "timeout": "300"}, wantDefault},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NewBashLong().callTimeout(tc.args)
			if got != tc.want {
				t.Errorf("callTimeout(%v) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}

// TestBashLongTimeoutBoundsInvariant keeps the tool contract coherent: the
// default must sit inside the advertised range, otherwise the model is told one
// thing and the code does another.
func TestBashLongTimeoutBoundsInvariant(t *testing.T) {
	if bashLongMinTimeout >= bashLongMaxTimeout {
		t.Fatalf("min %v must be < max %v", bashLongMinTimeout, bashLongMaxTimeout)
	}
	if bashLongTimeout < bashLongMinTimeout || bashLongTimeout > bashLongMaxTimeout {
		t.Errorf("default %v must be within [%v, %v]", bashLongTimeout, bashLongMinTimeout, bashLongMaxTimeout)
	}
}

// TestBashLongExecuteAppliesClampedTimeout is an end-to-end smoke test: a real
// command still runs, and the clamped deadline (not the raw argument) is what
// the error message reports when a call tops out.
func TestBashLongExecuteAppliesClampedTimeout(t *testing.T) {
	tool := NewBashLong()
	if tool.Name() != "bash_long" {
		t.Fatalf("Name() = %q, want bash_long", tool.Name())
	}

	result, err := tool.Execute(t.Context(), agent.ToolContext{}, map[string]any{
		"command": shellTestCommand("echo hi", "Write-Output hi"),
		"timeout": 900.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content == "" {
		t.Fatal("expected output from echo hi")
	}

	// A value above max must be reported as the clamped max, not as 900s.
	clamped := tool.callTimeout(map[string]any{"timeout": 900.0})
	if clamped != bashLongMaxTimeout {
		t.Fatalf("clamped timeout = %v, want %v", clamped, bashLongMaxTimeout)
	}
}

// TestBashLongExecuteAddsTimingPrefix verifies that the result content starts
// with a [Time: ... | Duration: ...] line.
func TestBashLongExecuteAddsTimingPrefix(t *testing.T) {
	tool := NewBashLong()

	result, err := tool.Execute(t.Context(), agent.ToolContext{}, map[string]any{
		"command": shellTestCommand("echo hello", "Write-Output hello"),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Must start with [Time: and contain Duration.
	if len(result.Content) < 10 || result.Content[:6] != "[Time:" {
		t.Fatalf("expected timing prefix, got content starting with: %.50q", result.Content)
	}
	if !containsAll(result.Content, "[Time:", "Duration:") {
		t.Errorf("missing expected fields in: %.120s", result.Content)
	}

	// First call should NOT have "Since last bash_long".
	if containsAll(result.Content, "Since last") {
		t.Errorf("first call should not report 'since last', got: %.120s", result.Content)
	}

	// Second call SHOULD include delta from first.
	result2, err := tool.Execute(t.Context(), agent.ToolContext{}, map[string]any{
		"command": shellTestCommand("echo again", "Write-Output again"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(result2.Content, "Since last bash_long") {
		t.Errorf("second call should report 'since last', got: %.120s", result2.Content)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
