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

const patchFormatExample = `*** Begin Patch
*** Update File: src/main.go
@@ func main() {
-old := 1
+new := 2
*** Add File: notes.txt
+hello world
*** Delete File: tmp/scratch.txt
*** Move to: notes2.txt
*** Update File: config.yaml
-# old comment
+# new comment
*** End Patch`

// Patch applies a line-based patch to files (create, update, delete, move)
// instead of rewriting whole files. The format follows the OpenAI Codex
// apply_patch grammar; see Description for an example.
type Patch struct{}

func (p *Patch) Name() string { return "patch" }

func (p *Patch) Description() string {
	return fmt.Sprintf(`Apply a line-based patch to files instead of rewriting whole files. Hunks are applied in the given order; context lines after "@@" locate the change position, and matching is fuzzy: exact first, then ignoring trailing whitespace, then trimming both sides.

Notes:
- "*** Move to:" must directly follow an Update File marker; it renames that file (a pure rename uses no content chunks).
- Context lines only anchor the position — do not repeat in them the line you are deleting or replacing; use the preceding line as the usual anchor.
- Hunks apply sequentially: each later chunk is searched in the file after earlier hunks, so anchors must match the already-updated surroundings when editing nearby content.
- The patch is not atomic: if a later hunk fails, earlier hunks are already written to disk. Verify the result or split high-risk patches into separate calls.
- If a hunk does not match, the error shows the expected lines and nearby file contents to make fixing easy.

Format:
%s`, patchFormatExample)
}

func (p *Patch) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("patch", api.ToolProperty{
		Type: api.PropertyType{"string"},
		Description: fmt.Sprintf(`Full patch text. File paths inside hunks are relative to the working directory, absolute, or with ~. Hunks: "*** Add File: <path>" followed by "+" lines; "*** Delete File: <path>"; "*** Update File: <path>" (optionally followed by "*** Move to: <new-path>"), then one or more chunks of "@@" / "@@ <context line>" plus lines prefixed with "+", "-", or " ". A chunk may end with "*** End of File". To insert at the end of a file, use an update chunk without context and only "+" lines. Example:
%s`, patchFormatExample),
	})
	return api.ToolFunction{
		Name:        p.Name(),
		Description: p.Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"patch"},
		},
	}
}

func (p *Patch) RequiresApproval(map[string]any) bool { return true }

// --- parsing -------------------------------------------------------------

type patchHunk struct {
	kind     string // "add", "delete", "update"
	path     string
	moveTo   string
	chunks   []patchChunk
	addLines []string
}

type patchChunk struct {
	contextLine *string
	oldLines    []string
	newLines    []string
	isEOF       bool
}

const (
	beginPatchMarker = "*** Begin Patch"
	endPatchMarker   = "*** End Patch"
	addFileMarker    = "*** Add File: "
	deleteFileMarker = "*** Delete File: "
	updateFileMarker = "*** Update File: "
	moveToMarker     = "*** Move to: "
	eofMarker        = "*** End of File"
)

func (p *Patch) Execute(ctx context.Context, toolCtx agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	patchText, ok := args["patch"].(string)
	if !ok || strings.TrimSpace(patchText) == "" {
		return agent.ToolResult{}, fmt.Errorf("patch parameter is required")
	}

	hunks, err := parsePatch(lenientStripHeredoc(patchText))
	if err != nil {
		return agent.ToolResult{}, err
	}

	select {
	case <-ctx.Done():
		return agent.ToolResult{}, ctx.Err()
	default:
	}

	var out []string
	for _, hunk := range hunks {
		switch hunk.kind {
		case "add":
			res, err := applyAdd(toolCtx.WorkingDir, hunk.path, strings.Join(hunk.addLines, "\n"))
			if err != nil {
				return agent.ToolResult{}, err
			}
			out = append(out, res)
		case "delete":
			res, err := applyDelete(toolCtx.WorkingDir, hunk.path)
			if err != nil {
				return agent.ToolResult{}, err
			}
			out = append(out, res)
		default: // update
			res, err := applyUpdate(toolCtx.WorkingDir, hunk)
			if err != nil {
				return agent.ToolResult{}, err
			}
			out = append(out, res)
		}
	}

	return agent.ToolResult{Content: strings.Join(out, "\n")}, nil
}

// lenientStripHeredoc removes a wrapping heredoc (<<'EOF' ... EOF) if the model
// passed one instead of raw patch text.
func lenientStripHeredoc(s string) string {
	t := strings.TrimSpace(s)
	body, ok := strings.CutPrefix(t, "<<'EOF'\n")
	if !ok {
		return t
	}
	if body, ok = cutSuffix(body, "\nEOF"); ok {
		return strings.TrimSpace(body)
	}
	return t
}

func cutSuffix(s string, suffix string) (string, bool) {
	if len(s) < len(suffix) || !strings.HasSuffix(s, suffix) {
		return s, false
	}
	return s[:len(s)-len(suffix)], true
}

// parsePatch follows the apply_patch grammar. It is deliberately a bit more
// lenient than the spec: leading/trailing whitespace around markers is allowed,
// and change lines ("+"/"-"/" ") before the first "@@" start an implicit chunk
// without context (mirroring codex's streaming parser).
func parsePatch(text string) ([]patchHunk, error) {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 || !isMarkerLine(lines[0], beginPatchMarker) {
		return nil, fmt.Errorf("invalid patch: must start with %q", beginPatchMarker)
	}

	var hunks []patchHunk
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if isMarkerLine(line, endPatchMarker) {
			return hunks, nil
		}
		switch {
		case strings.HasPrefix(line, addFileMarker):
			hunks = append(hunks, patchHunk{kind: "add", path: strings.TrimSpace(strings.TrimPrefix(line, addFileMarker))})
			for i++; i < len(lines); i++ {
				l := lines[i]
				trimmed := strings.TrimSpace(l)
				if isMarkerLine(trimmed, endPatchMarker) || strings.HasPrefix(trimmed, "***") { // next hunk or end of patch
					i--
					break
				}
				if len(l) > 0 && l[0] == '+' {
					hunks[len(hunks)-1].addLines = append(hunks[len(hunks)-1].addLines, l[1:])
				} else if trimmed != "" {
					hunks[len(hunks)-1].addLines = append(hunks[len(hunks)-1].addLines, trimmed)
				}
			}
		case strings.HasPrefix(line, deleteFileMarker):
			hunks = append(hunks, patchHunk{kind: "delete", path: strings.TrimSpace(strings.TrimPrefix(line, deleteFileMarker))})
		case strings.HasPrefix(line, updateFileMarker):
			path := strings.TrimSpace(strings.TrimPrefix(line, updateFileMarker))
			hunks = append(hunks, patchHunk{kind: "update", path: path})
			for i++; i < len(lines); i++ {
				l := lines[i]
				trimmed := strings.TrimSpace(l)
				if strings.HasPrefix(trimmed, moveToMarker) {
					hunks[len(hunks)-1].moveTo = strings.TrimSpace(strings.TrimPrefix(trimmed, moveToMarker))
					continue
				}
				if isMarkerLine(trimmed, eofMarker) {
					if len(hunks[len(hunks)-1].chunks) == 0 {
						return nil, fmt.Errorf("invalid hunk at line %d: update hunk does not contain any lines", i+1)
					}
					hunks[len(hunks)-1].chunks[len(hunks[len(hunks)-1].chunks)-1].isEOF = true
					continue
				}
				if strings.HasPrefix(trimmed, "***") { // next hunk or end of patch
					i--
					break
				}
				h := &hunks[len(hunks)-1]
				switch {
				case trimmed == "@@" || (len(trimmed) > 2 && strings.HasPrefix(trimmed, "@@ ")):
					lastIdx := len(h.chunks) - 1
					if lastIdx >= 0 && len(h.chunks[lastIdx].oldLines) == 0 && len(h.chunks[lastIdx].newLines) == 0 {
						return nil, fmt.Errorf("invalid hunk at line %d: unexpected \"@@\" marker; every line should start with ' ', '+' or '-'", i+1)
					}
					var ctx *string
					if trimmed != "@@" {
						c := strings.TrimSpace(strings.TrimPrefix(trimmed, "@@ "))
						ctx = &c
					}
					h.chunks = append(h.chunks, patchChunk{contextLine: ctx})
				case l == "":
					if len(h.chunks) == 0 {
						h.chunks = append(h.chunks, patchChunk{})
					}
					ch := &h.chunks[len(h.chunks)-1]
					ch.oldLines = append(ch.oldLines, "")
					ch.newLines = append(ch.newLines, "")
				case len(l) > 0 && l[0] == '+':
					if len(h.chunks) == 0 {
						h.chunks = append(h.chunks, patchChunk{})
					}
					h.chunks[len(h.chunks)-1].newLines = append(h.chunks[len(h.chunks)-1].newLines, l[1:])
				case len(l) > 0 && l[0] == '-':
					if len(h.chunks) == 0 {
						h.chunks = append(h.chunks, patchChunk{})
					}
					h.chunks[len(h.chunks)-1].oldLines = append(h.chunks[len(h.chunks)-1].oldLines, l[1:])
				case len(l) > 0 && l[0] == ' ':
					if len(h.chunks) == 0 {
						h.chunks = append(h.chunks, patchChunk{})
					}
					ch := &h.chunks[len(h.chunks)-1]
					ch.oldLines = append(ch.oldLines, l[1:])
					ch.newLines = append(ch.newLines, l[1:])
				default:
					return nil, fmt.Errorf("invalid hunk at line %d: expected a line starting with '@@ ', '+', '-', or ' '", i+1)
				}
			}
		case strings.HasPrefix(line, moveToMarker):
			return nil, fmt.Errorf("invalid hunk at line %d: \"*** Move to:\" is not a standalone marker; place it directly after an Update File marker to rename that file (a pure rename uses no content chunks)", i+1)

		default:

			return nil, fmt.Errorf("invalid hunk at line %d: unexpected marker %q", i+1, line)
		}
	}

	if len(hunks) == 0 {
		return nil, fmt.Errorf("invalid patch: no hunks")
	}
	for _, h := range hunks {
		if strings.TrimSpace(h.path) == "" {
			return nil, fmt.Errorf("hunk %q has an empty file path", h.kind)
		}
	}
	return hunks, nil
}

// isMarkerLine compares the trimmed line to a marker.
func isMarkerLine(line, marker string) bool {
	return strings.TrimSpace(line) == marker
}

// --- fuzzy seek ----------------------------------------------------------

// seekSequence finds pattern in lines starting at or after start with decreasing
// strictness: exact match, then ignoring trailing whitespace, then trimming both
// sides. When eof is true the search starts from the last possible position.
func seekSequence(lines []string, pattern []string, start int, eof bool) (int, bool) {
	if len(pattern) == 0 {
		return start, true
	}
	if len(pattern) > len(lines) {
		return 0, false
	}
	searchStart := start
	if eof && len(lines)-len(pattern) >= searchStart {
		searchStart = len(lines) - len(pattern)
	}

	matchLevel := func(a, b string, level int) bool {
		switch level {
		case 0:
			return a == b
		case 1:
			return strings.TrimRight(a, " \t\r") == strings.TrimRight(b, " \t\r")
		default:
			return strings.TrimSpace(a) == strings.TrimSpace(b)
		}
	}

	for level := 0; level < 3; level++ {
		for i := searchStart; i+len(pattern) <= len(lines); i++ {
			ok := true
			for j := range pattern {
				if !matchLevel(lines[i+j], pattern[j], level) {
					ok = false
					break
				}
			}
			if ok {
				return i, true
			}
		}
	}
	return 0, false
}

// --- apply ---------------------------------------------------------------

type replacement struct {
	start    int
	oldLen   int
	newLines []string
}

func linesOf(content string) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // drop the trailing empty element from the final newline
	}
	return lines
}

func applyAdd(workingDir, relPath string, contents string) (string, error) {
	abs, err := normalizeToolPath(workingDir, relPath)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(abs); statErr == nil {
		return "", fmt.Errorf("cannot add %s: file already exists", relPath)
	}
	if dir := filepath.Dir(abs); dir != "." && !isDir(dir) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("failed to create directory for %s: %w", relPath, err)
		}
	}
	data := strings.TrimSuffix(contents, "\n") + "\n"
	if len(data) > maxReadBytes {
		return "", fmt.Errorf("%s is too large to create (%d bytes)", relPath, len(data))
	}
	if err := writeFileAtomic(abs, []byte(data), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Created %s.", relPath), nil
}

func applyDelete(workingDir, relPath string) (string, error) {
	abs, err := normalizeToolPath(workingDir, relPath)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Lstat(abs); os.IsNotExist(statErr) {
		return "", fmt.Errorf("cannot delete %s: file does not exist", relPath)
	}
	if err := os.Remove(abs); err != nil {
		return "", fmt.Errorf("failed to delete %s: %w", relPath, err)
	}
	return fmt.Sprintf("Deleted %s.", relPath), nil
}

func applyUpdate(workingDir string, hunk patchHunk) (string, error) {
	if len(hunk.chunks) == 0 {
		if hunk.moveTo == "" {
			return "", fmt.Errorf("*** Update File: %s has no changes", hunk.path)
		}
		// Pure rename: no content chunks, only "*** Move to:".
		src, err := normalizeToolPath(workingDir, hunk.path)
		if err != nil {
			return "", err
		}
		if _, statErr := os.Lstat(src); os.IsNotExist(statErr) {
			return "", fmt.Errorf("cannot move %s: file does not exist", hunk.path)
		}
		dst, err := normalizeToolPath(workingDir, hunk.moveTo)
		if err != nil {
			return "", err
		}
		if _, statErr := os.Lstat(dst); statErr == nil {
			return "", fmt.Errorf("cannot move %s to %s: destination already exists", hunk.path, hunk.moveTo)
		}
		if dir := filepath.Dir(dst); dir != "." && !isDir(dir) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("failed to create directory for %s: %w", hunk.moveTo, err)
			}
		}
		if err := os.Rename(src, dst); err != nil {
			return "", fmt.Errorf("failed to move %s to %s: %w", hunk.path, hunk.moveTo, err)
		}
		return fmt.Sprintf("Moved %s to %s.", hunk.path, hunk.moveTo), nil
	}
	src, err := normalizeToolPath(workingDir, hunk.path)
	if err != nil {
		return "", err
	}
	file, info, err := openRegularFile(src)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", hunk.path, err)
	}
	defer file.Close()

	contentBytes, err := readAllWithinLimit(file, maxReadBytes)
	file.Close()
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", hunk.path, err)
	}

	lines := linesOf(string(contentBytes))
	repl, err := computeReplacements(lines, hunk.path, hunk.chunks)
	if err != nil {
		return "", err
	}
	newLines := applyReplacements(lines, repl)
	content := strings.Join(newLines, "\n") + "\n" // normalize to LF and guarantee a trailing newline

	if len(content) > maxReadBytes {
		return "", fmt.Errorf("edited %s is too large (%d bytes)", hunk.path, len(content))
	}
	if err := writeFileAtomic(src, []byte(content), info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("failed to write %s: %w", hunk.path, err)
	}

	res := fmt.Sprintf("Updated %s (%d chunk%s).", hunk.path, len(hunk.chunks), plural(len(hunk.chunks)))
	if hunk.moveTo != "" {
		dst, err := normalizeToolPath(workingDir, hunk.moveTo)
		if err != nil {
			return "", err
		}
		if _, statErr := os.Lstat(dst); statErr == nil {
			return "", fmt.Errorf("cannot move %s to %s: destination already exists", hunk.path, hunk.moveTo)
		}
		if dir := filepath.Dir(dst); dir != "." && !isDir(dir) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("failed to create directory for %s: %w", hunk.moveTo, err)
			}
		}
		if err := os.Rename(src, dst); err != nil {
			return "", fmt.Errorf("failed to move %s to %s: %w", hunk.path, hunk.moveTo, err)
		}
		res = strings.Replace(res, "Updated "+hunk.path+" (", "Moved "+hunk.path+" to "+hunk.moveTo+" (", -1)
	}
	return res, nil
}

func computeReplacements(originalLines []string, path string, chunks []patchChunk) ([]replacement, error) {
	var repls []replacement
	lineIndex := 0

	for ci, chunk := range chunks {
		if chunk.contextLine != nil {
			idx, ok := seekSequence(originalLines, []string{*chunk.contextLine}, lineIndex, false)
			if !ok {
				snip := nearbySnippet(originalLines, lineIndex, 10)
				return nil, fmt.Errorf("failed to find context %q in %s (chunk %d); file content near where the search started:\n%s", *chunk.contextLine, path, ci+1, snip)
			}
			lineIndex = idx + 1
		}

		if len(chunk.oldLines) == 0 {
			repls = append(repls, replacement{start: len(originalLines), oldLen: 0, newLines: chunk.newLines})
			continue
		}

		pattern := chunk.oldLines
		newSlice := chunk.newLines
		found, ok := seekSequence(originalLines, pattern, lineIndex, chunk.isEOF)
		if !ok && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			// Retry without the trailing empty sentinel (the terminating newline).
			pattern = pattern[:len(pattern)-1]
			if len(newSlice) > 0 && newSlice[len(newSlice)-1] == "" {
				newSlice = newSlice[:len(newSlice)-1]
			}
			found, ok = seekSequence(originalLines, pattern, lineIndex, chunk.isEOF)
		}
		if !ok {
			snip := nearbySnippet(originalLines, lineIndex, 10)
			return nil, fmt.Errorf("failed to find expected lines in %s (chunk %d):\n%s\nfile content near where the search started:\n%s", path, ci+1, strings.Join(chunk.oldLines, "\n"), snip)
		}

		repls = append(repls, replacement{start: found, oldLen: len(pattern), newLines: newSlice})
		lineIndex = found + len(pattern)
	}

	for i := 1; i < len(repls); i++ {
		if repls[i-1].start+repls[i-1].oldLen > repls[i].start {
			return nil, fmt.Errorf("patch changes in %s overlap", path)
		}
	}
	return repls, nil
}

// nearbySnippet returns up to maxLines file lines starting at idx (for error messages).
func nearbySnippet(lines []string, idx int, maxLines int) string {
	if len(lines) == 0 || idx >= len(lines) {
		return "(end of file)"
	}
	n := min(maxLines, len(lines)-idx)
	return strings.Join(lines[idx:idx+n], "\n")
}

func applyReplacements(lines []string, repls []replacement) []string {
	for i := len(repls) - 1; i >= 0; i-- {
		r := &repls[i]
		tail := lines[min(r.start+r.oldLen, len(lines)):]
		next := make([]string, 0, len(lines)-r.oldLen+len(r.newLines))
		next = append(next, lines[:min(r.start, len(lines))]...)
		next = append(next, r.newLines...)
		next = append(next, tail...)
		lines = next
	}
	return lines
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
