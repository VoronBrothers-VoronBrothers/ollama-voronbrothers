package api

import (
	"reflect"
	"testing"
)

func TestToolCallArgumentsFromJSON(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   map[string]any
		wantOK bool
	}{
		{
			name:   "valid object",
			in:     `{"city":"Paris","days":3}`,
			want:   map[string]any{"city": "Paris", "days": float64(3)},
			wantOK: true,
		},
		{
			name:   "empty object",
			in:     `{}`,
			want:   map[string]any{},
			wantOK: true,
		},
		{
			// Absent arguments are normal: an empty object, not a failure.
			name:   "empty payload",
			in:     ``,
			want:   map[string]any{},
			wantOK: true,
		},
		{
			name:   "double encoded",
			in:     `"{\"city\":\"Paris\"}"`,
			want:   map[string]any{"city": "Paris"},
			wantOK: true,
		},
		{
			name:   "truncated mid string",
			in:     `{"path":"/tmp/fo`,
			want:   map[string]any{"path": "/tmp/fo"},
			wantOK: true,
		},
		{
			name:   "truncated dangling comma",
			in:     `{"city":"Paris",`,
			want:   map[string]any{"city": "Paris"},
			wantOK: true,
		},
		{
			name:   "truncated nested object",
			in:     `{"opts":{"deep":true`,
			want:   map[string]any{"opts": map[string]any{"deep": true}},
			wantOK: true,
		},
		{
			name:   "trailing prose",
			in:     `{"city":"Paris"} and that should do it`,
			want:   map[string]any{"city": "Paris"},
			wantOK: true,
		},
		{
			name:   "leading prose",
			in:     `Sure, here you go: {"city":"Paris"}`,
			want:   map[string]any{"city": "Paris"},
			wantOK: true,
		},
		{
			name:   "dangling member dropped",
			in:     `{"city":"Paris","days":`,
			want:   map[string]any{"city": "Paris"},
			wantOK: true,
		},
		{
			// A truncated string ending in a backslash: the repair escapes it
			// instead of letting it swallow the closing quote.
			name:   "trailing backslash inside string",
			in:     `{"path":"C:\\temp\`,
			want:   map[string]any{"path": `C:\temp\`},
			wantOK: true,
		},
		{
			name: "nothing salvageable keeps the call without arguments",
			in:   `{"city":`,
		},
		{
			name: "array payload keeps the call without arguments",
			in:   `["not","an","object"]`,
		},
		{
			name: "plain text keeps the call without arguments",
			in:   `I cannot help with that`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ToolCallArgumentsFromJSON([]byte(tt.in))
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (args %#v)", ok, tt.wantOK, got.ToMap())
			}
			if !tt.wantOK {
				if got.Len() != 0 {
					t.Fatalf("expected empty arguments, got %#v", got.ToMap())
				}
				return
			}
			if gotMap := got.ToMap(); !reflect.DeepEqual(gotMap, tt.want) {
				t.Fatalf("got %#v, want %#v", gotMap, tt.want)
			}
		})
	}
}

func TestToolCallArgumentsFromJSONPreservesOrder(t *testing.T) {
	args, ok := ToolCallArgumentsFromJSON([]byte(`{"zeta":1,"alpha":2,"mid":3}`))
	if !ok {
		t.Fatal("expected a decoded result")
	}
	var keys []string
	for k := range args.All() {
		keys = append(keys, k)
	}
	want := []string{"zeta", "alpha", "mid"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("got keys %v, want %v", keys, want)
	}
}

func TestToolCallArgumentsStrictUnmarshalUnchanged(t *testing.T) {
	var args ToolCallFunctionArguments
	if err := args.UnmarshalJSON([]byte(`{"city":`)); err == nil {
		t.Fatal("expected strict unmarshal to reject truncated arguments")
	}
}
