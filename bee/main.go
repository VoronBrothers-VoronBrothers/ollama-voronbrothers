// Command bee — «мозг» под-агента bumblebee (slice 2).
//
// ⚠️ Это КОД АГЕНТА, а не Ollama TUI. Бинарник `bee` — отдельный живой
// процесс субагента: он не имеет отношения к TUI со slash-командами
// (/enterwatch, /heartbeat и т.д.) — тот живёт в cmd/tui/chat и компилируется
// в бинарник `ollama`. Не путать при отладке и сборке.
//
// Отдельный живой процесс: получает задачу, крутит agentic tool-loop, где
// LLM (по умолчанию deepseek через облачный провайдер proxyapi; либо локальный
// ollama для теста) решает действия, а рука run_command исполняет их локально
// в персистентном bash (persistent cwd). Шаги стримит в stdout, поправки от
// bumblebee берёт со stdin (delegate feed → tmux send-keys → сюда).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ollama/ollama/envconfig"
)

var (
	flagModel  string
	flagTask   string
	maxRoundsN = 300 // бюджет раундов на один запрос; сбрасывается при correction
)

// doneMarker — автоматический маркер окончания работы модели под-агента
// (печатает код bee, а не LLM). Должен совпадать с beeDoneMarker в
// agent/tools/delegate.go.
const doneMarker = "[bee] DONE {end}"

func emitDone(reason string) {
	// Каждый DONE = сохранение полной истории (final answer / error / max rounds).
	beeSave.writeSnapshot(flagModel, getBeeState().Task)
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

	initStateFile(flagModel)
	stopHeartbeat := startHeartbeat()
	defer stopHeartbeat()
	defer func() {
		// Финальная запись: файл остаётся после выхода, чтобы delegate мог
		// показать историю («закончил 3 минуты назад, причина …»).
		updateState(func(s *beeState) {
			s.Status = statusExited
			s.Phase = "exited"
			s.Waiting = false
		})
		// Финальный снапшот полной истории при выходе процесса.
		beeSave.writeSnapshot(flagModel, getBeeState().Task)
	}()

	ep := resolveEndpoint(flagModel)
	// Если prefix-match нашёл полное имя — обновляем flagModel для API запросов.
	if ep.resolvedModel != "" {
		flagModel = ep.resolvedModel
	}
	fmt.Printf("[bee] model=%s endpoint=%s provider=%s\n", flagModel, ep.baseURL, ep.provider)
	updateState(func(s *beeState) { s.Model = flagModel })

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
		updateState(func(s *beeState) {
			// Статус задачи НЕ меняем: если она упала — остаётся error,
			// если закончилась — остаётся idle. Флаг waiting говорит,
			// что процесс жив и готов принять новую задачу.
			s.Waiting = true
			s.Phase = "waiting for next task"
			s.Tool = ""
		})
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

// defaultModel — модель по умолчанию для bee. Единый (глобальный) источник
// живёт в envconfig.BeeBrain(): читает BEE_AGENT_MODEL, иначе DefaultModel.
// Это отдельная переменная от LAMADefaultModel (бинарник lama).
func defaultModel() string {
	return envconfig.BeeBrain()
}

// runTask — одна итерация agentic tool-loop по задаче. Не выходит в REPL,
// не закрывает shell. Поправки из corrCh обрабатываются между шагами.
func runTask(client *http.Client, ep endpoint, shell *shell, corrCh <-chan string, task string) {
	// Статичная метка: всё от пользователя (стартовая задача, REPL-строки,
	// поправки) — USER. Ответы модели — общее имя AI.
	fmt.Printf("\nUSER: %s\n", strings.TrimSpace(task))
	updateState(func(s *beeState) {
		s.Task = strings.TrimSpace(task)
		s.Waiting = false
		s.Status = statusWorking
		s.Phase = "starting task"
		s.RoundsLeft = maxRoundsN
		s.Round = 0
		s.Reason = ""
	})
	messages := []chatMsg{
		{Role: "system", Content: systemPrompt()},
		{Role: "user", Content: task},
	}
	left := maxRoundsN

	for {
		pending := drainCorrections(corrCh)
		corrections := 0
		for _, p := range pending {
			if p == "" {
				continue
			}
			corrections++
			beeSave.noteCorrection(p)
			fmt.Printf("USER (correction): %s\n", p)
			messages = append(messages, chatMsg{Role: "user", Content: "Correction from supervisor (apply it): " + p})
		}
		// Supervisor correction = агенту даём второй шанс: полный бюджет раундов.
		if corrections > 0 {
			left = maxRoundsN
			fmt.Printf("[bee] rounds reset by supervisor correction (%d left)\n", left)
			updateState(func(s *beeState) {
				s.RoundsLeft = left
				s.Phase = "applying supervisor correction"
			})
		}

		updateState(func(s *beeState) {
			s.Round++
			s.Phase = "model call"
			s.Tool = ""
		})
		resp, err := chatOnce(client, ep, messages)
		if err != nil {
			fmt.Printf("[bee] ERROR model call: %v\n", err)
			updateState(func(s *beeState) {
				s.Status = statusError
				s.Phase = "model call failed"
				s.Reason = err.Error()
			})
			emitDone("error: " + err.Error())
			return
		}
		messages = append(messages, resp.message)

		// Промежуточный ответ модели рядом с tool calls — тоже метка AI.
		if c := strings.TrimSpace(resp.message.Content); c != "" && len(resp.toolCalls) > 0 {
			fmt.Printf("AI: %s\n", c)
		}

		if len(resp.toolCalls) == 0 {
			beeSave.setFinalAnswer(resp.message.Content)
			fmt.Printf("\nAI: %s\n", strings.TrimSpace(resp.message.Content))
			updateState(func(s *beeState) {
				s.Status = statusIdle
				s.Phase = "task finished"
				s.Tool = ""
				s.Reason = "final answer"
			})
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
			updateState(func(s *beeState) {
				s.Phase = "running tool " + name
				s.Tool = name
			})
			res := dispatchTool(shell, name, args)
			fmt.Printf("[bee] tool=%s => %d bytes\n%s\n", name, len(res), tail(res, 2000))
			messages = append(messages, chatMsg{Role: "tool", ToolCallID: tc.ID, Content: res})
		}

		left--
		updateState(func(s *beeState) { s.RoundsLeft = left })
		if left <= 0 {
			fmt.Println("[bee] max rounds reached; stopping.")
			updateState(func(s *beeState) {
				s.Status = statusMaxRounds
				s.Phase = "stopped"
				s.Reason = fmt.Sprintf("max rounds (%d) reached", maxRoundsN)
			})
			emitDone("max-rounds")
			return
		}
	}
}

// dispatchTool — разветвление tool-вызовов: run_command идёт в персистентный
// bash, read/write/edit работают с файлами напрямую (без round-trip через shell).
// Каждый вызов регистрируется в saveState (каждые N → JSON-снапшот).
func dispatchTool(sh *shell, name string, args map[string]any) string {
	var result string
	switch name {
	case "read":
		result = toolRead(sh, args)
	case "write":
		result = toolWrite(sh, args)
	case "edit":
		result = toolEdit(sh, args)
	default:
		// run_command: пишем в историю ДО исполнения.
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			appendHistory(cmd)
		}
		result = runCommand(sh, args)
	}

	// Периодический save: полная история, чекпоинт каждые N вызовов.
	fullArgs, _ := json.Marshal(args)
	if beeSave.recordToolCall(name, string(fullArgs), result) {
		beeSave.writeSnapshot(flagModel, getBeeState().Task)
	}
	return result
}

// lastRunAt — время старта предыдущего run_command (для строки «Since last …»).
// Защищено мьютексом не нужно: bee однопоточен по tool-loop.
var lastRunAt time.Time

// runCommand — семантика как у bash_long основного агента:
//   - необязательный аргумент timeout (секунды, 60-900, дефолт 120);
//   - верхняя граница — не «жди всегда», команда возвращается как только
//     закончилась;
//   - префикс [Time: … | Duration: … | Since last …] — модель видит, сколько
//     реально шла сборка и сколько прошло между запусками;
//   - каждые 10 с в пейн печатается строка прогресса, чтобы было видно,
//     что долгая команда НЕ висит и под-агент жив.
func runCommand(sh *shell, args map[string]any) string {
	cmdStr, _ := args["command"].(string)
	if err := rejectSuicidalCommand(cmdStr); err != nil {
		return "(refused) " + err.Error()
	}
	t := callTimeout(args)
	start := time.Now()
	fmt.Printf("[bee] run_command (timeout %s): %s\n", formatDur(t), firstLine(cmdStr))

	out, rc, rerr := sh.run(cmdStr, t, func(elapsed, limit time.Duration) {
		fmt.Printf("[bee] … still running: %s / %s elapsed\n", formatDur(elapsed), formatDur(limit))
		updateState(func(s *beeState) {
			s.Phase = fmt.Sprintf("run_command (%s elapsed)", formatDur(elapsed))
		})
	})
	end := time.Now()

	var prevDelta string
	if !lastRunAt.IsZero() {
		d := start.Sub(lastRunAt)
		if d < 0 {
			d = 0
		}
		prevDelta = " | Since last run_command: " + formatDur(d)
	}
	lastRunAt = end

	if rerr != nil {
		out = fmt.Sprintf("(%v)\n%s", rerr, out)
	}
	prefix := fmt.Sprintf("[Time: %s | Duration: %s%s]\n",
		end.UTC().Format(time.RFC3339), formatDur(end.Sub(start)), prevDelta)
	return fmt.Sprintf("%sexit=%d\n%s", prefix, rc, out)
}

// firstLine — первая строка команды для лога/фазы (длинные команды режем).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
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
			"description": "Run a shell command on this machine in a persistent bash (cwd is kept between calls). Optional 'timeout' in seconds (60-900, default 120) for long jobs; returns as soon as the command finishes. Returns stdout+stderr and exit code.",
			"parameters": obj(map[string]any{
				"command": map[string]any{"type": "string"},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Optional upper bound in seconds (60-900, default 120). Set it to the expected duration of the command; do not blindly use the maximum.",
				},
			}, "command"),
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
		if cwd, _, err := shell.run("pwd", 0, nil); err == nil && strings.TrimSpace(cwd) != "" {
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
	return "You are a focused sub-agent with tools: run_command (persistent bash, cwd persists; optional timeout 60-900s, default 120s), read (file text; optional start/end lines), write (create/overwrite file), edit (exact old→new replacement). Relative paths use the shell's cwd. Prefer read/write/edit over cat/sed/echo.\n" +
		"Long jobs (builds, big downloads, tests) are fine: pass timeout up to 900 seconds instead of splitting the work or polling. The call returns as soon as the command finishes — the timeout is only an upper bound.\n" +
		"Efficiency protocol — follow strictly:\n" +
		"- Plan in one breath, then act. A typical task takes 3-8 tool calls; any call beyond that must be justified by a concrete missing fact you can name before calling.\n" +
		"- Measure each fact ONCE. Never re-run a command (or a variant of it) just to double-check its result. If two independent measurements disagree, report the most conservative number with a one-line note — do NOT iterate until they match.\n" +
		"- No verification passes: after collecting the required data, go straight to the final answer. Re-scanning 'to be sure' is forbidden; a clean result from a correct command IS verified.\n" +
		"- Batch independent operations into one command where possible; don't re-read files you've already seen. If 2-3 searches clarify the task, stop searching and act.\n" +
		"Keep responses short — no preamble, no restating the task. When done, reply with a concise final summary (<=20 lines) and stop calling tools."
}

// reasoningTools400 — известный казус прокси (gpt-6-luna): /v1/chat/completions с
// function tools отклоняется, если reasoning включён по умолчанию. Лечится явным
// "reasoning_effort":"none" в теле запроса.
const reasoningTools400Marker = "Function tools with reasoning_effort"

func chatOnce(client *http.Client, ep endpoint, messages []chatMsg) (respChatResponse, error) {
	reqBody := map[string]any{
		"model":    flagModel,
		"messages": messages,
		"tools":    beeTools(),
		"stream":   false,
	}

	// до 2 попыток: первая обычная, вторая — с reasoning_effort=none (казус выше)
	for attempt := 0; ; attempt++ {
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
		var raw map[string]any
		decErr := json.NewDecoder(r.Body).Decode(&raw)
		status := r.StatusCode
		r.Body.Close()
		if decErr != nil {
			return respChatResponse{}, fmt.Errorf("decode response (http %d): %w", status, err)
		}
		if status == http.StatusBadRequest && attempt == 0 && strings.Contains(shortJSON(raw), reasoningTools400Marker) {
			reqBody["reasoning_effort"] = "none"
			continue
		}
		if status >= 400 {
			return respChatResponse{}, fmt.Errorf("http %d: %.300s", status, shortJSON(raw))
		}
		return parseResp(raw), nil
	}
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
