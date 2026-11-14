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
	"strings"
	"time"
)

var (
	flagModel  string
	flagTask   string
	maxRoundsN = 24 // защита от бесконечного loop
	cmdTimeout = 120 * time.Second
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
		fmt.Printf("[bee] task=%q\n", flagTask)
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

func defaultModel() string {
	if m := os.Getenv("BEE_AGENT_MODEL"); m != "" {
		return m
	}
	return "deepseek/deepseek-v4.1-flash-1017"
}

// runTask — одна итерация agentic tool-loop по задаче. Не выходит в REPL,
// не закрывает shell. Поправки из corrCh обрабатываются между шагами.
func runTask(client *http.Client, ep endpoint, shell *shell, corrCh <-chan string, task string) {
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
			fmt.Printf("[bee] correction received: %s\n", p)
			messages = append(messages, chatMsg{Role: "user", Content: "Correction from supervisor (apply it): " + p})
		}

		resp, err := chatOnce(client, ep, messages)
		if err != nil {
			fmt.Printf("[bee] ERROR model call: %v\n", err)
			emitDone("error")
			return
		}
		messages = append(messages, resp.message)

		if len(resp.toolCalls) == 0 {
			fmt.Printf("\n[bee] FINAL:\n%s\n", strings.TrimSpace(resp.message.Content))
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
			cmdStr, _ := args["command"].(string)
			fmt.Printf("[bee] tool=%s cmd=%q\n", name, cmdStr)

			out, rc, rerr := shell.run(cmdStr)
			if rerr != nil {
				out = fmt.Sprintf("(run_command error: %v)", rerr)
			}
			res := fmt.Sprintf("exit=%d\n%s", rc, out)
			fmt.Printf("[bee]   => exit=%d (%d bytes)\n%s\n", rc, len(out), tail(out, 2000))
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
		"You have one tool, run_command: it runs a shell command in a PERSISTENT bash (cwd is kept between calls). " +
		"Work step by step (e.g. ls, then cd, then inspect/run). Prefer small verifiable commands. " +
		"When the task is done, stop calling tools and reply with a short final summary."
}

func chatOnce(client *http.Client, ep endpoint, messages []chatMsg) (respChatResponse, error) {
	reqBody := map[string]any{
		"model":    flagModel,
		"messages": messages,
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "run_command",
				"description": "Run a shell command on this machine in a persistent bash. Returns stdout+stderr and exit code.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"command": map[string]any{"type": "string"}},
					"required":   []string{"command"},
				},
			},
		}},
		"stream": false,
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
// Пример: "deepseek" → "deepseek/deepseek-v4.1-flash-1017" в proxyapi.
func resolveEndpoint(model string) endpoint {
	for _, p := range loadProviders() {
		if full, ok := matchModel(p.Models, model); ok {
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

// matchModel: exact OR prefix. Returns the full matched name.
func matchModel(list []string, query string) (string, bool) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return "", false
	}
	for _, v := range list {
		lv := strings.ToLower(v)
		if lv == q || strings.HasPrefix(lv, q) {
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
