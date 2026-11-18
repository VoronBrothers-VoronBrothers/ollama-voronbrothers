package parsers

import (
	"regexp"
	"strings"
)

// This file holds shared "second chance" repairs for malformed tool-call text.
// Models occasionally emit bracket/structure typos ("wrong bracket", unclosed
// element, trailing comma). The repair helpers are best-effort: if the repaired
// text still doesn't parse, the caller must soft-fail (emit raw text as content)
// rather than return an error that kills the agent.

// repairJSON attempts to fix common structural mistakes in a JSON string:
// markdown code fences, mismatched closing brackets ("square bracket closing an
// object"), unclosed brackets at end of stream, stray closers and trailing commas.
func repairJSON(s string) string {
	s = strings.TrimSpace(s)

	// Strip markdown code fence if the model wrapped its output in one
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if end := strings.LastIndex(s, "```"); end >= 0 {
			s = s[:end]
		}
		s = strings.TrimSpace(s)
	}

	var out []byte
	stack := make([]byte, 0, 8)
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			out = append(out, c)
		case '{', '[':
			out = append(out, c)
			stack = append(stack, c)
		case '}', ']':
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				// "wrong bracket": model wrote a square bracket to close an object
				// (or vice versa) — emit the closing char that matches what is open
				var closer byte = c
				switch top {
				case '{':
					closer = '}'
				default: // '['
					closer = ']'
				}
				out = append(out, closer)
				stack = stack[:len(stack)-1]
			}
			// stray closer with no open element — drop it
		default:
			out = append(out, c)
		}
	}
	// Close anything left open at end of stream (most recent first)
	for len(stack) > 0 {
		switch stack[len(stack)-1] {
		case '{':
			out = append(out, '}')
		default:
			out = append(out, ']')
		}
		stack = stack[:len(stack)-1]
	}

	result := string(out)

	// Drop trailing commas before closing brackets (safe enough here because we
	// only run this when the original parse already failed)
	result = repairTrailingCommaRegex.ReplaceAllString(result, "$1")

	return result
}

var repairTrailingCommaRegex = regexp.MustCompile(`,\s*([}\]])`)

var qwenRepairTagRegex = regexp.MustCompile(`</?(function|parameter)[^>]*>`)

// repairQwenXML balances function/parameter elements in a transformed Qwen XML
// tool call: it auto-closes unclosed elements, drops stray closers and fixes
// mismatched closing tags (e.g. "</function> was meant). It walks every tag and keeps a stack of open elements.

func repairQwenXML(s string) string {
	var out strings.Builder
	stack := make([]string, 0, 8)
	lastIdx := 0
	for _, loc := range qwenRepairTagRegex.FindAllStringIndex(s, -1) {
		out.WriteString(s[lastIdx:loc[0]])
		tag := s[loc[0]:loc[1]]
		name := qwenRepairTagRegex.FindStringSubmatch(tag)[1]
		switch {
		case strings.HasPrefix(tag, "</"):
			if len(stack) > 0 && stack[len(stack)-1] == name {
				out.WriteString(tag)
				stack = stack[:len(stack)-1]
			} else if containsName(stack, name) {
				// mismatched closer: auto-close the elements above it first
				for len(stack) > 0 && stack[len(stack)-1] != name {
					out.WriteString("</" + stack[len(stack)-1] + ">")
					stack = stack[:len(stack)-1]
				}
				out.WriteString(tag)
				stack = stack[:len(stack)-1]
			}
			// stray closer with no matching opener — drop it
		default:
			out.WriteString(tag)
			stack = append(stack, name)
		}
		lastIdx = loc[1]
	}
	out.WriteString(s[lastIdx:])
	for len(stack) > 0 {
		out.WriteString("</" + stack[len(stack)-1] + ">")
		stack = stack[:len(stack)-1]
	}
	return out.String()
}

func containsName(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}
