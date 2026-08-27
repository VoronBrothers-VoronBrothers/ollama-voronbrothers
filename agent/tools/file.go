package tools

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
)

const (
	maxReadBytes = 200000
)

type Read struct{}

func (r *Read) Name() string {
	return "read"
}

func (r *Read) Description() string {
	return "Read a text file from the current working directory."
}

func (r *Read) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("path", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "File path (relative, absolute, or with ~).",
	})
	props.Set("start", api.ToolProperty{
		Type:        api.PropertyType{"integer"},
		Description: "Optional 1-based line to start from.",
	})
	props.Set("end", api.ToolProperty{
		Type:        api.PropertyType{"integer"},
		Description: "Optional inclusive end line.",
	})
	return api.ToolFunction{
		Name:        r.Name(),
		Description: r.Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"path"},
		},
	}
}

func (r *Read) RequiresApproval(map[string]any) bool {
	return true
}

func (r *Read) Execute(ctx context.Context, toolCtx agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	// TODO: use shared agent.RequiredStringArg / agent.OptionalIntArg for args (see agent package cleanup plan).
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return agent.ToolResult{}, fmt.Errorf("path parameter is required")
	}

	absPath, err := normalizeToolPath(toolCtx.WorkingDir, path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	file, info, err := openRegularFile(absPath)
	if err != nil {
		return agent.ToolResult{}, err
	}
	defer file.Close()

	selection, err := readSelectionFromArgs(args)
	if err != nil {
		return agent.ToolResult{}, err
	}
	if !selection.enabled && info.Size() > maxReadBytes {
		return agent.ToolResult{}, fmt.Errorf("%s is too large to read (%d bytes)", path, info.Size())
	}

	select {
	case <-ctx.Done():
		return agent.ToolResult{}, ctx.Err()
	default:
	}

	var content string
	if selection.enabled {
		content, err = readLineSelection(file, selection)
	} else {
		var contentBytes []byte
		contentBytes, err = readAllWithinLimit(file, maxReadBytes)
		content = string(contentBytes)
	}
	if err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: content}, nil
}

type Edit struct{}

func (e *Edit) Name() string {
	return "edit"
}

func (e *Edit) Description() string {
	return "Edit a file by exact-text replacement."
}

func (e *Edit) Schema() api.ToolFunction {
	editProps := api.NewToolPropertiesMap()
	editProps.Set("old_text", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Exact text to replace.",
	})
	editProps.Set("new_text", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Replacement text.",
	})

	props := api.NewToolPropertiesMap()
	props.Set("path", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "File path (relative, absolute, or with ~).",
	})
	props.Set("edits", api.ToolProperty{
		Type: api.PropertyType{"array"},
		Items: api.ToolProperty{
			Type:       api.PropertyType{"object"},
			Properties: editProps,
			Required:   []string{"old_text", "new_text"},
		},
		Description: "Edits matched against the original file. Each old_text must be unique; keep it minimal.",
	})
	props.Set("replace_all", api.ToolProperty{
		Type:        api.PropertyType{"boolean"},
		Description: "Replace every occurrence (single edit only).",
	})
	return api.ToolFunction{
		Name:        e.Name(),
		Description: e.Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"path", "edits"},
		},
	}
}

func (e *Edit) RequiresApproval(map[string]any) bool {
	return true
}

func (e *Edit) Execute(ctx context.Context, toolCtx agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	// TODO: use shared agent.RequiredStringArg / agent.OptionalBoolArg for args (see agent package cleanup plan).
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return agent.ToolResult{}, fmt.Errorf("path parameter is required")
	}

	edits, replaceAll, err := parseEditArgs(args)
	if err != nil {
		return agent.ToolResult{}, err
	}

	absPath, err := normalizeToolPath(toolCtx.WorkingDir, path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	if err := rejectFinalSymlink(absPath); err != nil {
		return agent.ToolResult{}, err
	}

	file, info, err := openRegularFile(absPath)
	if err != nil {
		return agent.ToolResult{}, err
	}
	if info.Size() > maxReadBytes {
		file.Close()
		return agent.ToolResult{}, fmt.Errorf("%s is too large to edit (%d bytes)", path, info.Size())
	}

	select {
	case <-ctx.Done():
		file.Close()
		return agent.ToolResult{}, ctx.Err()
	default:
	}

	contentBytes, err := readAllWithinLimit(file, maxReadBytes)
	if closeErr := file.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return agent.ToolResult{}, err
	}
	content := string(contentBytes)

	var updated string
	replacements := 0
	if replaceAll {
		matches := strings.Count(content, edits[0].OldText)
		if matches == 0 {
			return agent.ToolResult{}, fmt.Errorf("old_text was not found in %s", path)
		}
		updated = strings.ReplaceAll(content, edits[0].OldText, edits[0].NewText)
		replacements = matches
	} else {
		// Every edit is matched against the original file content rather
		// than the output of earlier edits, so each edit must match exactly
		// once and edits must target disjoint regions.
		matched := make([]editMatch, 0, len(edits))
		for i, edit := range edits {
			count := strings.Count(content, edit.OldText)
			if count == 0 {
				return agent.ToolResult{}, editNotFoundError(path, i, len(edits))
			}
			if count > 1 {
				return agent.ToolResult{}, editAmbiguousError(path, i, len(edits), count)
			}
			matched = append(matched, editMatch{
				editIndex: i,
				offset:    strings.Index(content, edit.OldText),
				length:    len(edit.OldText),
				newText:   edit.NewText,
			})
			replacements++
		}

		slices.SortFunc(matched, func(a, b editMatch) int { return cmp.Compare(a.offset, b.offset) })
		for i := 1; i < len(matched); i++ {
			prev, cur := matched[i-1], matched[i]
			if prev.offset+prev.length > cur.offset {
				return agent.ToolResult{}, fmt.Errorf("edits[%d] and edits[%d] overlap in %s; merge them into one edit or target disjoint text", prev.editIndex, cur.editIndex, path)
			}
		}

		// Apply from the end of the file backwards so earlier offsets stay valid.
		updated = content
		for i := len(matched) - 1; i >= 0; i-- {
			m := matched[i]
			updated = updated[:m.offset] + m.newText + updated[m.offset+m.length:]
		}
	}

	if updated == content {
		return agent.ToolResult{}, fmt.Errorf("edit produced no changes in %s; replacement text is identical to the original", path)
	}
	if len(updated) > maxReadBytes {
		return agent.ToolResult{}, fmt.Errorf("edited content is too large (%d bytes)", len(updated))
	}

	if err := writeFileAtomic(absPath, []byte(updated), info.Mode().Perm()); err != nil {
		return agent.ToolResult{}, err
	}

	return agent.ToolResult{Content: fmt.Sprintf("Updated %s (%d edit%s, %d replacement%s).", path, len(edits), plural(len(edits)), replacements, plural(replacements))}, nil
}

type Write struct{}

func (w *Write) Name() string {
	return "write"
}

func (w *Write) Description() string {
	return "Create or overwrite a file with the given content."
}

func (w *Write) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("path", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "File path (relative, absolute, or with ~).",
	})
	props.Set("content", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Full file content.",
	})
	return api.ToolFunction{
		Name:        w.Name(),
		Description: w.Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"path", "content"},
		},
	}
}

func (w *Write) RequiresApproval(map[string]any) bool {
	return true
}

func (w *Write) Execute(ctx context.Context, toolCtx agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	path, ok := args["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return agent.ToolResult{}, fmt.Errorf("path parameter is required")
	}
	content, ok := args["content"].(string)
	if !ok {
		return agent.ToolResult{}, fmt.Errorf("content parameter is required")
	}

	absPath, err := normalizeToolPath(toolCtx.WorkingDir, path)
	if err != nil {
		return agent.ToolResult{}, err
	}

	select {
	case <-ctx.Done():
		return agent.ToolResult{}, ctx.Err()
	default:
	}

	perm := os.FileMode(0o644)
	if info, err := os.Lstat(absPath); err == nil {
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return agent.ToolResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return agent.ToolResult{}, err
	}
	if err := writeFileAtomic(absPath, []byte(content), perm); err != nil {
		return agent.ToolResult{}, err
	}
	return agent.ToolResult{Content: fmt.Sprintf("Wrote %s (%d bytes).", path, len(content))}, nil
}

// editReplacement is one targeted replacement within an edit call.
type editReplacement struct {
	OldText string
	NewText string
}

// editMatch locates one editReplacement within the original file content.
type editMatch struct {
	editIndex int
	offset    int
	length    int
	newText   string
}

// parseEditArgs normalizes edit arguments from a tool call into a list of
// replacements. It accepts the `edits` array form and tolerates legacy
// top-level old_text/new_text args as well as stringified JSON, mirroring
// the pi coding agent's argument handling.
func parseEditArgs(args map[string]any) ([]editReplacement, bool, error) {
	replaceAll, _ := args["replace_all"].(bool)

	var edits []editReplacement
	if raw, ok := args["edits"]; ok {
		parsed, err := parseEditArray(raw)
		if err != nil {
			return nil, false, err
		}
		edits = parsed
	}

	// Fold a legacy top-level old_text/new_text pair into edits.
	if oldText, ok := args["old_text"].(string); ok {
		newText, ok := args["new_text"].(string)
		if !ok {
			return nil, false, fmt.Errorf("new_text parameter is required")
		}
		edits = append(edits, editReplacement{OldText: oldText, NewText: newText})
	}

	if len(edits) == 0 {
		return nil, false, fmt.Errorf("edits parameter is required")
	}
	for i, edit := range edits {
		if edit.OldText == "" {
			if len(edits) == 1 {
				return nil, false, fmt.Errorf("old_text parameter is required")
			}
			return nil, false, fmt.Errorf("edits[%d].old_text must not be empty", i)
		}
	}
	if replaceAll && len(edits) != 1 {
		return nil, false, fmt.Errorf("replace_all only applies to a single edit")
	}
	return edits, replaceAll, nil
}

func parseEditArray(raw any) ([]editReplacement, error) {
	if s, ok := raw.(string); ok {
		// Some models serialize array arguments as a JSON string.
		if err := json.Unmarshal([]byte(s), &raw); err != nil {
			return nil, fmt.Errorf("edits must be an array of {old_text, new_text} objects")
		}
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("edits must be an array of {old_text, new_text} objects")
	}

	edits := make([]editReplacement, 0, len(items))
	for i, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edits[%d] must be an object with old_text and new_text", i)
		}
		oldText, oldOK := editTextArg(entry, "old_text", "oldText")
		newText, newOK := editTextArg(entry, "new_text", "newText")
		if !oldOK || !newOK {
			return nil, fmt.Errorf("edits[%d] must be an object with old_text and new_text", i)
		}
		edits = append(edits, editReplacement{OldText: oldText, NewText: newText})
	}
	return edits, nil
}

// editTextArg reads the first present string key, tolerating both snake_case
// and camelCase spellings that models emit.
func editTextArg(entry map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := entry[key].(string); ok {
			return value, true
		}
	}
	return "", false
}

func editNotFoundError(path string, editIndex, totalEdits int) error {
	if totalEdits == 1 {
		return fmt.Errorf("old_text was not found in %s", path)
	}
	return fmt.Errorf("edits[%d].old_text was not found in %s", editIndex, path)
}

func editAmbiguousError(path string, editIndex, totalEdits, occurrences int) error {
	if totalEdits == 1 {
		return fmt.Errorf("old_text matched %d times in %s; set replace_all to true to replace every match", occurrences, path)
	}
	return fmt.Errorf("edits[%d].old_text matched %d times in %s; each edit must match exactly once, so provide more surrounding context", editIndex, occurrences, path)
}

// normalizeToolPath resolves a tool-supplied path to a clean absolute path:
// trims whitespace, expands a leading "~" to the home directory, resolves
// relative paths against the working directory, and cleans the result. Any
// absolute path (including ../ that escapes the working directory) is
// accepted as-is.
func normalizeToolPath(workingDir, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path parameter is required")
	}
	if path == "~" || strings.HasPrefix(path, "~"+string(os.PathSeparator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if !filepath.IsAbs(path) {
		base, err := workingDirAbs(workingDir)
		if err != nil {
			return "", err
		}
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path), nil
}

// openRegularFile opens an absolute, cleaned path for reading. Symlinks are
// rejected so callers operate on the real target file directly.
func openRegularFile(abs string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%s is a symlink; read the target file directly", abs)
	}
	if err := rejectNonRegularFile(abs, info); err != nil {
		return nil, nil, err
	}
	file, err := os.Open(abs)
	if err != nil {
		return nil, nil, err
	}
	info, err = file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if err := rejectNonRegularFile(abs, info); err != nil {
		file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func rejectNonRegularFile(path string, info os.FileInfo) error {
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

// writeFileAtomic writes data to the absolute, cleaned path via a temporary
// file in the same directory.
func writeFileAtomic(abs string, data []byte, perm os.FileMode) error {
	info, err := os.Lstat(abs)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; edit the target file directly", abs)
	}

	parent, name := filepath.Split(abs)
	tmpBase := fmt.Sprintf(".%s.ollama-tmp-%d", name, os.Getpid())
	for i := 0; ; i++ {
		candidateName := tmpBase
		if i > 0 {
			candidateName = fmt.Sprintf("%s-%d", tmpBase, i)
		}
		candidate := filepath.Join(parent, candidateName)
		file, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := file.Chmod(perm); err != nil {
			closeErr := file.Close()
			_ = os.Remove(candidate)
			if closeErr != nil {
				return closeErr
			}
			return err
		}
		writeErr := writeAllAndSync(file, data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(candidate)
			if writeErr != nil {
				return writeErr
			}
			return closeErr
		}
		return os.Rename(candidate, abs)
	}
}

func rejectFinalSymlink(abs string) error {
	info, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; edit the target file directly", abs)
	}
	return nil
}

func writeAllAndSync(file *os.File, data []byte) error {
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func readAllWithinLimit(reader io.Reader, limit int) ([]byte, error) {
	if limit < 0 {
		limit = 0
	}
	content, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(content) > limit {
		return nil, fmt.Errorf("content is too large (%d byte limit)", limit)
	}
	return content, nil
}

func workingDirAbs(workingDir string) (string, error) {
	base := workingDir
	if base == "" {
		var err error
		base, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	return canonicalPath(base)
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return resolved, nil
	}
	return abs, nil
}

type readSelection struct {
	enabled bool
	start   int
	end     int
}

func readSelectionFromArgs(args map[string]any) (readSelection, error) {
	selection := readSelection{start: 1}

	if start, ok, err := intReadArg(args, "start"); err != nil {
		return readSelection{}, err
	} else if ok {
		selection.enabled = true
		selection.start = start
	}
	if end, ok, err := intReadArg(args, "end"); err != nil {
		return readSelection{}, err
	} else if ok {
		selection.enabled = true
		selection.end = end
	}

	if !selection.enabled {
		return selection, nil
	}
	if selection.start < 1 {
		return readSelection{}, fmt.Errorf("start must be greater than 0")
	}
	if selection.end > 0 && selection.end < selection.start {
		return readSelection{}, fmt.Errorf("end must be greater than or equal to start")
	}
	return selection, nil
}

func readLineSelection(file *os.File, selection readSelection) (string, error) {
	reader := bufio.NewReader(file)
	var b strings.Builder
	for lineNo := 1; ; {
		line, err := reader.ReadSlice('\n')
		if lineNo >= selection.start && (selection.end == 0 || lineNo <= selection.end) {
			if b.Len()+len(line) > maxReadBytes {
				return "", fmt.Errorf("selected content is too large (%d byte limit)", maxReadBytes)
			}
			b.Write(line)
		}
		if err != nil {
			if err == bufio.ErrBufferFull {
				continue
			}
			if err == io.EOF {
				break
			}
			return "", err
		}
		if selection.end > 0 && lineNo >= selection.end {
			break
		}
		lineNo++
	}
	return b.String(), nil
}

func intReadArg(args map[string]any, key string) (int, bool, error) {
	value, ok := args[key]
	if !ok {
		return 0, false, nil
	}
	switch v := value.(type) {
	case int:
		return v, true, nil
	case int64:
		return int(v), true, nil
	case float64:
		if v != float64(int(v)) {
			return 0, true, fmt.Errorf("%s must be a whole number", key)
		}
		return int(v), true, nil
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return 0, false, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return 0, true, fmt.Errorf("%s must be a whole number", key)
		}
		return n, true, nil
	default:
		return 0, true, fmt.Errorf("%s must be a whole number", key)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
