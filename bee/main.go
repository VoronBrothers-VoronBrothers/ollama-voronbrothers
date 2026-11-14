// Command bee — «мозг» под-агента bumblebee (slice 2).
//
// Отдельный живой процесс: получает задачу, крутит agentic tool-loop, где
// LLM (по умолчанию deepseek через облачный провайдер proxyapi; либо локальный
// ollama для теста) решает действия, а рука run_command исполняет их локально
// в персистентном bash (persistent cwd). Шаги стримит в stdout, поправки от
// bumblebee берёт со stdin (delegate feed → tmux send-keys → сюда).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ollama/ollama/envconfig"
)

var (
	flagModel  string
	flagTask   string
	maxRoundsN = 24 // защита от бесконечного loop
	// cmdTimeout — дедлайн на ОДНУ команду run_command у под-агента.
	// Жёсткий потолок 30с: иначе висящая команда держит tmux-pane «замороженным»
	// и саб-агент зависает в терминале (основной bumblebee это не трогает).
	cmdTimeout = 30 * time.Second
)

const doneMarker = "[bee] DONE {end}"

func emitDone(reason string) {
	if reason == "" {
		fmt.Println(doneMarker)
	} else {
		fmt.Printf("%s (%s)\n", doneMarker, reason)
	}
}

type endpoint struct {
	baseURL, provider, apiKey string
	resolvedModel             string // полное имя из providers.json (если prefix-match)
}

func main() {
	parseArgs(os.Args[1:])
	if flagModel == "" {
		flagModel = defaultModel()
	}
	if flagTask == "" {
		flagTask = os.Getenv("BEE_TASK")
	}

	ep := resolveEndpoint(flagModel)
	// Если prefix-match нашёл полное имя — обновляем flagModel для API запросов.
	if ep.resolvedModel != "" {
		flagModel = ep.resolvedModel
	}
	fmt.Printf("[bee] model=%s endpoint=%s provider=%s\n", flagModel, ep.baseURL, ep.provider)

	shell := newShell()
	defer shell.close()

	corrCh := make(chan string, 16)
	go readStdin(corrCh)

	client := &http.Client{Timeout: 900 * time.Second}

	// Если есть стартовая задача — выполняем сразу.
	if strings.TrimSpace(flagTask) != "" {
		runTask(client, ep, shell, corrCh, flagTask)
	}

	// REPL: ждём следующую задачу или /exit.
	for {
		fmt.Print("[bee] ready (type /exit to quit): ")
		line := waitStdin(corrCh)
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			fmt.Println("[bee] exiting.")
			return
		}
		// Новая задача — сбрасываем контекст.
		runTask(client, ep, shell, corrCh, line)
	}
}

func parseArgs(args []string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-model":
			if i+1 < len(args) {
				flagModel = args[i+1]
				i++
			}
		case "-task":
			if i+1 < len(args) {
				flagTask = args[i+1]
				i++
			}
		}
	}
}

// DefaultAgentModel — каноническое (полное) имя модели по умолчанию для под-агентов.
// Формат «provider/model» совпадает с тем, как модель отображается в ollama
// (/v1/models). resolveEndpoint умеет резолвить эту форму до имени API.
// Single source of truth for the built-in default model is envconfig.DefaultModel
// (shared with the `lama` launcher). Override at runtime with BEE_AGENT_MODEL.
const DefaultAgentModel = envconfig.DefaultModel

func defaultModel() string {
	if m := os.Getenv("BEE_AGENT_MODEL"); m != "" {
		return m
	}
	return DefaultAgentModel
}

// runTask — одна итерация agentic tool-loop по задаче. Не выходит в REPL,
// не закрывает shell. Поправки из corrCh обрабатываются между шагами.
func runTask(client *http.Client, ep endpoint, shell *shell, corrCh <-chan string, task string) {
	// Статичная метка: всё от пользователя (стартовая задача, REPL-строки,
	// поправки) — USER. Ответы модели — общее имя AI.
	fmt.Printf("\nUSER: %s\n", strings.TrimSpace(task))
	messages := []chatMsg{
		{Role: "system", Content: systemPrompt()},
		{Role: "user", Content: task},
	}
	left := maxRoundsN

	for {
		pending := drainCorrections(corrCh)
		for _, p := range pending {
			if p == "" {
				continue
			}
			fmt.Printf("USER (correction): %s\n", p)
			messages = append(messages, chatMsg{Role: "user", Content: "Correction from supervisor (apply it): " + p})
		}

		resp, err := chatOnce(client, ep, messages)
		if err != nil {
			fmt.Printf("[bee] ERROR model call: %v\n", err)
			emitDone("error")
			return
		}
		messages = append(messages, resp.message)

		// Промежуточный ответ модели рядом с tool calls — тоже метка AI.
		if c := strings.TrimSpace(resp.message.Content); c != "" && len(resp.toolCalls) > 0 {
			fmt.Printf("AI: %s\n", c)
		}

		if len(resp.toolCalls) == 0 {
			fmt.Printf("\nAI: %s\n", strings.TrimSpace(resp.message.Content))
			emitDone("")
			return
		}

		for _, tc := range resp.toolCalls {
			name := "run_command"
			if tc.Function.Name != "" {
				name = tc.Function.Name
			}
			args := map[string]any{}
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			res := dispatchTool(shell, name, args)
			fmt.Printf("[bee] tool=%s => %d bytes\n%s\n", name, len(res), tail(res, 2000))
			messages = append(messages, chatMsg{Role: "tool", ToolCallID: tc.ID, Content: res})
		}

		left--
		if left <= 0 {
			fmt.Println("[bee] max rounds reached; stopping.")
			emitDone("max-rounds")
			return
		}
	}
}

// dispatchTool — разветвление tool-вызовов: run_command идёт в персистентный
// bash, read/write/edit работают с файлами напрямую (без round-trip через shell).
func dispatchTool(shell *shell, name string, args map[string]any) string {
	switch name {
	case "read":
		return toolRead(shell, args)
	case "write":
		return toolWrite(shell, args)
	case "edit":
		return toolEdit(shell, args)
	default:
		cmdStr, _ := args["command"].(string)
		out, rc, rerr := shell.run(cmdStr)
		if rerr != nil {
			out = fmt.Sprintf("(run_command error: %v)", rerr)
		}
		return fmt.Sprintf("exit=%d\n%s", rc, out)
	}
}

// beeTools — схемы инструментов, которые видит модель под-агента.
func beeTools() []map[string]any {
	obj := func(props map[string]any, req ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(req) > 0 {
			m["required"] = req
		}
		return m
	}
	return []map[string]any{
		{"type": "function", "function": map[string]any{
			"name":        "run_command",
			"description": "Run a shell command on this machine in a persistent bash (cwd is kept between calls). Returns stdout+stderr and exit code.",
			"parameters":  obj(map[string]any{"command": map[string]any{"type": "string"}}, "command"),
		}},
		{"type": "function", "function": map[string]any{
			"name":        "read",
			"description": "Read a text file. Optional 1-based line range: start, end (inclusive). Relative paths resolve against the persistent shell cwd.",
			"parameters": obj(map[string]any{
				"path":  map[string]any{"type": "string"},
				"start": map[string]any{"type": "integer"},
				"end":   map[string]any{"type": "integer"},
			}),
		}},
		{"type": "function", "function": map[string]any{
			"name":        "write",
			"description": "Create or overwrite a file with the given content (parent directories are created). Relative paths resolve against the persistent shell cwd.",
			"parameters": obj(map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			}, "path", "content"),
		}},
		{"type": "function", "function": map[string]any{
			"name":        "edit",
			"description": "Exact-text replacement in a file: old_text must appear exactly once (or set replace_all=true). Empty new_text deletes the match.",
			"parameters": obj(map[string]any{
				"path":        map[string]any{"type": "string"},
				"old_text":    map[string]any{"type": "string"},
				"new_text":    map[string]any{"type": "string"},
				"replace_all": map[string]any{"type": "boolean"},
			}),
		}},
	}
}

// --- лёгкие file-инструменты под-агента (read/write/edit) ---

const maxReadResult = 16000 // потолок результата read, чтобы не раздувать контекст модели

func expandPath(shell *shell, p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = home + strings.TrimPrefix(p, "~")
		}
	}
	if !filepath.IsAbs(p) {
		if cwd, _, err := shell.run("pwd"); err == nil && strings.TrimSpace(cwd) != "" {
			cwd = strings.TrimSpace(cwd)
			if cwd == "/" {
				p = "/" + strings.TrimPrefix(p, "/")
			} else {
				p = filepath.Join(cwd, p)
			}
		}
	}
	return p
}

func toolRead(shell *shell, args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "(read error: missing 'path')"
	}
	abs := expandPath(shell, path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("(read error: %v)", err)
	}
	s := string(data)

	startF, _ := args["start"].(float64)
	endF, _ := args["end"].(float64)
	startLine, endLine := 1, -1
	if startF > 0 {
		startLine = int(startF)
	}
	if endF > 0 {
		endLine = int(endF)
	}

	rangeNote := ""
	if startLine > 1 || (endLine > 0 && endLine < len(strings.Split(s, "\n"))) {
		lines := strings.Split(s, "\n")
		n := len(lines)
		a, b := startLine, n
		if endLine > 0 && endLine < b {
			b = endLine
		}
		if a > b || a > n {
			return fmt.Sprintf("(read: empty range; file has %d lines)", n)
		}
		s = strings.Join(lines[a-1:b], "\n")
		rangeNote = fmt.Sprintf(", lines %d-%d of %d", a, b, n)
	}

	total := len(data)
	if len(s) > maxReadResult {
		half := maxReadResult / 2
		cut := s[half:]
		s = s[:half] + fmt.Sprintf("\n…[truncated; %d bytes total — use start/end to page]…\n", total) + cut[len(cut)-half:]
	}
	return fmt.Sprintf("[read %s (%d bytes%s)]\n%s", abs, total, rangeNote, s)
}

func toolWrite(shell *shell, args map[string]any) string {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" || content == "" {
		return "(write error: 'path' and 'content' are required)"
	}
	abs := expandPath(shell, path)
	if dir := filepath.Dir(abs); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Sprintf("(write error: %v)", err)
		}
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return fmt.Sprintf("(write error: %v)", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len([]byte(content)), abs)
}

func toolEdit(shell *shell, args map[string]any) string {
	path, _ := args["path"].(string)
	oldT, _ := args["old_text"].(string)
	newT, _ := args["new_text"].(string)
	replaceAll, _ := args["replace_all"].(bool)
	if path == "" || oldT == "" {
		return "(edit error: 'path' and 'old_text' are required)"
	}
	if oldT == newT {
		return "(edit error: old_text and new_text are identical; nothing to do)"
	}
	abs := expandPath(shell, path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Sprintf("(edit error: %v)", err)
	}
	n := strings.Count(string(data), oldT)
	switch {
	case n == 0:
		return "(edit error: old_text not found in file)"
	case n > 1 && !replaceAll:
		return fmt.Sprintf("(edit error: old_text matches %d places; add more surrounding context or set replace_all=true)", n)
	}
	out := string(data)
	if replaceAll {
		out = strings.ReplaceAll(out, oldT, newT)
	} else {
		out = strings.Replace(out, oldT, newT, 1)
	}
	if err := os.WriteFile(abs, []byte(out), 0o644); err != nil {
		return fmt.Sprintf("(edit error: %v)", err)
	}
	what := "replaced"
	if replaceAll {
		what = fmt.Sprintf("replaced %d occurrences", n)
	}
	return fmt.Sprintf("%s in %s (%d bytes now)", what, abs, len(out))
}

// waitStdin блокирует ожидание одной строки из corrCh (без таймаута).
func waitStdin(ch <-chan string) string {
	for {
		select {
		case m := <-ch:
			return strings.TrimSpace(m)
		}
	}
}

type chatMsg struct {
	Role       string `json:"role"`
	Content    string `json:"content"` // без omitempty: локальный /v1 ollama требует строку, а не nil
	ToolCallID string `json:"tool_call_id,omitempty"`
}

func systemPrompt() string {
	return "You are a focused sub-agent with full freedom to act on this machine. " +
		"You have four tools: run_command (shell command in a PERSISTENT bash, cwd is kept between calls), " +
		"read (file text; optional 1-based start/end line range), write (create or overwrite a file) and edit (exact old_text → new_text replacement). " +
		"Relative paths resolve against the persistent shell's current directory. Prefer read/write/edit over cat/sed for files. " +
		"Work step by step; prefer small verifiable actions. When the task is done, stop calling tools and reply with a short final summary."
}

func chatOnce(client *http.Client, ep endpoint, messages []chatMsg) (respChatResponse, error) {
	reqBody := map[string]any{
		"model":    flagModel,
		"messages": messages,
		"tools":    beeTools(),
		"stream":   false,
	}
	payload, _ := json.Marshal(reqBody)

	req, err := http.NewRequest("POST", ep.baseURL+"/chat/completions", strings.NewReader(string(payload)))
	if err != nil {
		return respChatResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ep.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+ep.apiKey)
	}

	r, err := client.Do(req)
	if err != nil {
		return respChatResponse{}, err
	}
	defer r.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		return respChatResponse{}, fmt.Errorf("decode response (http %d): %w", r.StatusCode, err)
	}
	if r.StatusCode >= 400 {
		return respChatResponse{}, fmt.Errorf("http %d: %.300s", r.StatusCode, shortJSON(raw))
	}
	return parseResp(raw), nil
}

type respChatResponse struct {
	message   chatMsg
	toolCalls []toolCallOut
}

type toolCallOut struct {
	ID       string
	Function struct{ Name, Arguments string }
}

func parseResp(raw map[string]any) respChatResponse {
	var out respChatResponse
	choices, _ := raw["choices"].([]any)
	if len(choices) == 0 {
		return out
	}
	ch, _ := choices[0].(map[string]any)
	msg, _ := ch["message"].(map[string]any)
	out.message = chatMsg{Role: "assistant", Content: getString(msg, "content")}
	if tcs, ok := msg["tool_calls"].([]any); ok {
		for _, tc := range tcs {
			tcm, _ := tc.(map[string]any)
			fn, _ := tcm["function"].(map[string]any)
			out.toolCalls = append(out.toolCalls, toolCallOut{
				ID:       getString(tcm, "id"),
				Function: struct{ Name, Arguments string }{Name: getString(fn, "name"), Arguments: getString(fn, "arguments")},
			})
		}
	}
	return out
}

type providerCfg struct {
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models"`
}

func loadProviders() []providerCfg {
	path := os.Getenv("OLLAMA_PROVIDERS_CONFIG")
	if path == "" {
		path = os.ExpandEnv("$HOME/.ollama/providers.json")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var w struct {
		Providers []providerCfg `json:"providers"`
	}
	if json.Unmarshal(data, &w) != nil {
		return nil
	}
	return w.Providers
}

// resolveEndpoint — точное совпадение ИЛИ prefix-match по первым символам.
// Понимает и каноническую форму «provider/model» (как в /v1/models): она
// резолвится до bare-имени API. Пример: "proxyapi/deepseek/..." →
// "deepseek/deepseek-v4.1-flash-1017" в proxyapi.
func resolveEndpoint(model string) endpoint {
	for _, p := range loadProviders() {
		if full, ok := matchModel(p.Name, p.Models, model); ok {
			e := endpoint{baseURL: strings.TrimSuffix(p.BaseURL, "/"), provider: "cloud:" + p.Name, apiKey: p.APIKey}
			if full != model {
				e.resolvedModel = full
			}
			return e
		}
	}
	host := os.Getenv("OLLAMA_URL")
	if host == "" {
		host = "http://127.0.0.1:11434"
	}
	return endpoint{baseURL: strings.TrimSuffix(host, "/") + "/v1", provider: "local-ollama"}
}

// matchModel: exact OR prefix. Returns the bare model name from list.
// Понимает три формы query:
//   - bare-имя из списка:            "deepseek/deepseek-v4.1-flash-1017"
//   - каноническое «provider/model»:  "proxyapi/deepseek/deepseek-v4.1-flash-1017"
//   - короткий alias (prefix):       "deepseek" / "sonar"
func matchModel(providerName string, list []string, query string) (string, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return "", false
	}
	pn := strings.ToLower(providerName)
	for _, v := range list {
		lv := strings.ToLower(v)
		withPrefix := pn + "/" + lv // каноническая форма «provider/model»
		if q == lv || q == withPrefix {
			return v, true
		}
		// короткий alias: query — префикс имени модели (напр. "deepseek")
		if strings.HasPrefix(lv, q) {
			return v, true
		}
	}
	return "", false
}

// drainCorrections вбирает все накопленные поправки из канала (non-blocking).
func drainCorrections(ch <-chan string) []string {
	var out []string
drain:
	for {
		select {
		case m := <-ch:
			out = append(out, strings.TrimSpace(m))
		default:
			break drain
		}
	}
	return out
}

// --- персистентный bash (руки агента) ---
type shell struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines chan string
}

func newShell() *shell {
	cmd := exec.Command("bash")
	stdin, _ := cmd.StdinPipe()
	pr, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	s := &shell{cmd: cmd, in: stdin, lines: make(chan string, 256)}
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		close(s.lines)
	}()
	return s
}

func (s *shell) run(command string) (string, int, error) {
	n := randN()
	marker := fmt.Sprintf("__BEE_END_%d__", n)
	if _, err := s.in.Write([]byte(fmt.Sprintf("%s\necho %s $?\n", command, marker))); err != nil {
		return "", -1, err
	}
	var b strings.Builder
	deadline := time.After(cmdTimeout)
	for {
		select {
		case <-deadline:
			s.in.Write([]byte("kill 0\n")) // best effort: убить текущую команду в shell-группе
			return "TIMEOUT after " + cmdTimeout.String(), -1, fmt.Errorf("command timed out")
		case line, ok := <-s.lines:
			if !ok {
				return b.String(), -1, fmt.Errorf("shell closed")
			}
			if idx := strings.Index(line, marker); idx >= 0 {
				rc := 0
				fmt.Sscanf(strings.TrimSpace(line[idx+len(marker):]), "%d", &rc)
				return b.String(), rc, nil
			}
			b.WriteString(line + "\n")
		}
	}
}

func (s *shell) close() {
	_ = s.cmd.Process.Kill()
	_ = s.cmd.Wait()
}

func readStdin(ch chan string) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		ch <- sc.Text()
	}
}

// --- helpers ---
var rndSeed int64 = time.Now().UnixNano()

func randN() int {
	rndSeed = rndSeed*636413629784 + 1
	return int(rndSeed%100000) + os.Getpid()%1000
}

func getString(m map[string]any, k string) string {
	if m == nil {
		return ""
	}
	v, _ := m[k].(string)
	return v
}

func shortJSON(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…[truncated]…" + s[len(s)-n:]
}
