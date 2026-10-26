package api

import (
	"encoding/json"
	"strings"
)

// ToolCallArgumentsFromJSON decodes tool call arguments the way a chat client
// (or a model) actually produced them.
//
// ToolCallFunctionArguments.UnmarshalJSON stays strict on purpose: parsers and
// repair paths must not silently accept garbage. Conversions at the API
// boundary, however, only replay the call back to the model or hand it to a
// tool runner, and failing the whole request because a model truncated a JSON
// object is much worse than doing a best-effort decode.
//
// The second return value reports whether the payload (or a structural repair
// of it) actually parsed. When it is false the caller keeps the tool call but
// hands empty arguments to the model and logs the raw payload.
func ToolCallArgumentsFromJSON(data []byte) (ToolCallFunctionArguments, bool) {
	if strings.TrimSpace(string(data)) == "" {
		// Absent arguments are normal, not a parse failure.
		return NewToolCallFunctionArguments(), true
	}
	for _, candidate := range toolCallArgumentCandidates(data) {
		var args ToolCallFunctionArguments
		if err := json.Unmarshal([]byte(candidate), &args); err == nil {
			return args, true
		}
	}
	return NewToolCallFunctionArguments(), false
}

// toolCallArgumentCandidates returns the ordered set of interpretations we are
// willing to try, cheapest and most faithful first.
func toolCallArgumentCandidates(data []byte) []string {
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil
	}

	var candidates []string
	seen := make(map[string]bool)
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		candidates = append(candidates, s)
	}

	add(raw)

	// Some models double-encode the arguments as a JSON string.
	base := raw
	if unquoted, ok := unwrapJSONString(raw); ok {
		base = unquoted
		add(base)
	}

	// Truncated or unbalanced object: close the open string and brackets.
	add(closeToolCallArguments(base))

	// Trailing prose after a complete object.
	if i := strings.LastIndex(base, "}"); i >= 0 && i < len(base)-1 {
		add(base[:i+1])
	}

	// Leading prose before the object.
	if i := strings.Index(base, "{"); i > 0 {
		add(closeToolCallArguments(base[i:]))
	}

	// A dangling member ("{"a": 1, "b":") is repaired by dropping it.
	cuts := topLevelSeparators(base, ',')
	for i := len(cuts) - 1; i >= 0; i-- {
		add(closeToolCallArguments(base[:cuts[i]]))
	}

	// Nothing usable. The call itself still survives; the caller decides how
	// to report the raw payload.
	return candidates
}

func unwrapJSONString(s string) (string, bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", false
	}
	var unquoted string
	if err := json.Unmarshal([]byte(s), &unquoted); err != nil {
		return "", false
	}
	unquoted = strings.TrimSpace(unquoted)
	if !strings.HasPrefix(unquoted, "{") && !strings.HasPrefix(unquoted, "[") && unquoted != "null" {
		return "", false
	}
	return unquoted, true
}

// closeToolCallArguments appends the missing closing quote and brackets for a
// truncated JSON document. It is purely structural: it never invents keys.
func closeToolCallArguments(s string) string {
	s = strings.TrimRight(s, " \t\r\n")
	if s == "" {
		return ""
	}

	var stack []byte
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}

	out := s
	if inString {
		if escaped {
			// A trailing backslash would escape our closing quote.
			out += `\`
		}
		out += `"`
	}
	// Drop a trailing comma/colon left dangling by truncation.
	for {
		trimmed := strings.TrimRight(out, " \t\r\n")
		if !strings.HasSuffix(trimmed, ",") && !strings.HasSuffix(trimmed, ":") {
			break
		}
		out = trimmed[:len(trimmed)-1]
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i] == '{' {
			out += "}"
		} else {
			out += "]"
		}
	}
	return out
}

// topLevelSeparators returns the byte offsets of c that are outside strings and
// at the outermost object nesting level.
func topLevelSeparators(s string, target byte) []int {
	var idx []int
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{', '[':
			depth++
		case '}', ']':
			depth--
		case ',':
			if target == ',' && depth <= 1 {
				idx = append(idx, i)
			}
		}
	}
	return idx
}
