package tools

import "testing"

func TestTailLines(t *testing.T) {
	in := "a\nb\nc\nd\n" // trailing newline, 4 content lines
	if got := tailLines(in, 2); got != "c\nd" {
		t.Errorf("n=2: got %q want %q", got, "c\nd")
	}
	if got := tailLines(in, 10); got != "a\nb\nc\nd" {
		t.Errorf("n>len: got %q want all", got)
	}
	if got := tailLines(in, 0); got != "a\nb\nc\nd" {
		t.Errorf("n=0: got %q want all", got)
	}
	// trailing blanks dropped
	in2 := "x\ny\n\n   \n"
	if got := tailLines(in2, 5); got != "x\ny" {
		t.Errorf("blanks: got %q want %q", got, "x\ny")
	}
	if got := tailLines("\n  \n", 10); got != "" {
		t.Errorf("empty: got %q want empty", got)
	}
}
