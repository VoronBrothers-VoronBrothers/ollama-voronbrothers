package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/envconfig"
)

// Delegate — инструмент управления под-агентами bumblebee.
//
// Под-агент = отдельный живой процесс в tmux (уникальное имя сессии), явно
// отличный от самого бumblebee. Управляется действиями: spawn | list | stop |
// feed | pause | resume. Это control-plane: запуск, наблюдение, пауза, подача
// сообщений и остановка. «Мозг» агента (какая модель/команда крутит tool-loop)
// задаётся командой запуска (env BEE_AGENT_CMD / аргумент task) — см. slice 2-3.
type Delegate struct {
	mu      sync.Mutex
	agents  map[string]*agentInfo // id -> info
	nextSeq int
}

type agentInfo struct {
	id        string
	session   string // tmux session name (уникальный, prefix bee-)
	panePID   int    // pid верхнего процесса в пейне (для pause/resume)
	task      string
	model     string
	paused    bool
	spawnedAt time.Time
}

// beeDoneMarker — должен совпадать с doneMarker в bee/main.go.
const beeDoneMarker = "[bee] DONE {end}"

// NewDelegate возвращает новый управляющий агентом.
func NewDelegate() *Delegate { return &Delegate{agents: map[string]*agentInfo{}} }

func (d *Delegate) Name() string { return "delegate" }

func (d *Delegate) Description() string {
	return "Spawn and control sub-agents as separate live tmux processes. Actions: " +
		"'spawn' starts an agent with a task; 'list' shows running agents; " +
		"'models' lists available agent brains (cloud providers + local ollama); " +
		"'output' reads the agent's terminal output; " +
		"'stop' kills one; 'feed' sends it a message (at start or mid-run); " +
		"'pause'/'resume' freeze/unfreeze its process. Sub-agents have full freedom; " +
		"you only monitor and steer."
}

func (d *Delegate) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("action", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "One of: spawn, list, models, output, stop, feed, pause, resume.",
	})
	props.Set("task", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Task text for 'spawn'.",
	})
	props.Set("model", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Model name for the agent (default: proxyapi/deepseek/deepseek-v4.1-flash-1017). Optional.",
	})
	props.Set("id", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Agent id. Required for stop/feed/pause/resume; optional for spawn (auto-generated).",
	})
	props.Set("message", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Message to send with 'feed'.",
	})
	props.Set("lines", api.ToolProperty{
		Type:        api.PropertyType{"number"},
		Description: "For 'output': how many trailing lines to return (default 80). Omit for the default.",
	})
	return api.ToolFunction{
		Name:        "delegate",
		Description: d.Description(),
		Parameters: api.ToolFunctionParameters{
			Type:       "object",
			Properties: props,
			Required:   []string{"action"},
		},
	}
}

func (d *Delegate) Execute(_ context.Context, toolCtx agent.ToolContext, args map[string]any) (agent.ToolResult, error) {
	action := strings.TrimSpace(toString(args["action"]))
	switch action {
	case "spawn":
		return d.spawn(toolCtx.WorkingDir, toString(args["task"]), toString(args["model"]), toString(args["id"]))
	case "list":
		return d.list()
	case "models":
		return d.listModels(), nil
	case "output":
		id := strings.TrimSpace(toString(args["id"]))
		if id == "" {
			return agent.ToolResult{Content: "Error: 'id' required for action output"}, nil
		}
		n := 80 // default trailing lines
		if v, ok := args["lines"]; ok && v != nil {
			switch t := v.(type) {
			case int:
				n = t
			case float64:
				n = int(t)
			}
		}
		return d.output(id, n)
	case "stop", "feed", "pause", "resume":
		id := strings.TrimSpace(toString(args["id"]))
		if id == "" {
			return agent.ToolResult{Content: "Error: 'id' required for action " + action}, nil
		}
		switch action {
		case "stop":
			return d.stop(id)
		case "feed":
			return d.feed(id, toString(args["message"]))
		case "pause":
			return d.pause(id)
		case "resume":
			return d.resume(id)
		default:
			return agent.ToolResult{Content: "Error: unhandled action '" + action + "'"}, nil
		}
	default:
		return agent.ToolResult{Content: "Error: unknown action '" + action + "'. Use spawn|list|models|output|stop|feed|pause|resume."}, nil
	}
}

func (d *Delegate) spawn(cwd, task, model, id string) (agent.ToolResult, error) {
	// Fail fast: don't spawn a doomed session if the model doesn't exist.
	if err := validateSpawnModel(model); err != nil {
		return agent.ToolResult{Content: "Error: " + err.Error()}, nil
	}
	if cwd == "" {
		cwd = "."
	}
	d.mu.Lock()
	d.nextSeq++
	seq := d.nextSeq
	d.mu.Unlock()

	if strings.TrimSpace(id) == "" {
		id = fmt.Sprintf("bee-%d-%d", time.Now().UnixMilli(), seq)
	}
	session := "bee-" + id

	// Уникальность имени сессии tmux (требование: не писать «такая панель уже есть»).
	if _, err := exec.Command("tmux", "has-session", "-t", session).CombinedOutput(); err == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: agent id %q already exists (session %s)", id, session)}, nil
	}

	cmdArgs := agentCommand(task, model) // команда «мозга» агента (bee / placeholder)
	out, err := exec.CommandContext(context.Background(), "tmux", append([]string{
		"new-session", "-d", "-s", session, "-c", cwd, "--",
	}, cmdArgs...)...).CombinedOutput()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error spawning agent %q: %v\n%s", id, err, strings.TrimSpace(string(out)))}, nil
	}

	panePID := panePIDOf(session)
	info := &agentInfo{id: id, session: session, panePID: panePID, task: task, model: model, spawnedAt: time.Now()}
	d.mu.Lock()
	d.agents[id] = info
	d.mu.Unlock()

	return agent.ToolResult{Content: fmt.Sprintf(
		"Spawned agent %q (tmux session %s, pid %d). Task: %s. Model: %s.", id, session, panePID, task, displayModel(model))}, nil
}

func (d *Delegate) list() (agent.ToolResult, error) {
	d.mu.Lock()
	infos := make([]*agentInfo, 0, len(d.agents))
	for _, a := range d.agents {
		infos = append(infos, a)
	}
	d.mu.Unlock()

	if len(infos) == 0 {
		return agent.ToolResult{Content: "No agents tracked in this bumblebee session."}, nil
	}
	var b strings.Builder
	for _, a := range infos {
		status := "working"
		if a.paused {
			status = "paused"
		} else if !tmuxSessionAlive(a.session) {
			status = "dead"
		} else if lastPaneLineIsDone(a.session) {
			status = "done"
		}
		fmt.Fprintf(&b, "- %s [%s] session=%s pid=%d age=%s\n    task: %s\n", a.id, status, a.session, a.panePID, time.Since(a.spawnedAt).Round(time.Second), a.task)
	}
	return agent.ToolResult{Content: strings.TrimSpace(b.String())}, nil
}

func (d *Delegate) output(id string, n int) (agent.ToolResult, error) {
	a := d.getAgent(id)
	if a == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: no such agent %q", id)}, nil
	}
	out, err := exec.Command("tmux", "capture-pane", "-t", a.session, "-p").CombinedOutput()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error reading output of %q: %v", id, err)}, nil
	}
	text := tailLines(string(out), n)
	if text == "" {
		text = "(no output yet)"
	}
	return agent.ToolResult{Content: fmt.Sprintf("[agent %s] (last %d lines)\n%s", id, n, text)}, nil
}

func (d *Delegate) stop(id string) (agent.ToolResult, error) {
	a := d.getAgent(id)
	if a == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: no such agent %q", id)}, nil
	}
	out, _ := exec.Command("tmux", "kill-session", "-t", a.session).CombinedOutput()
	d.mu.Lock()
	delete(d.agents, id)
	d.mu.Unlock()
	return agent.ToolResult{Content: fmt.Sprintf("Stopped agent %q (killed tmux session %s)%s", id, a.session, suffix(out))}, nil
}

func (d *Delegate) feed(id, message string) (agent.ToolResult, error) {
	a := d.getAgent(id)
	if a == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: no such agent %q", id)}, nil
	}
	msg := strings.TrimRight(message, "\n") + "\n"
	out, err := exec.Command("tmux", "send-keys", "-t", a.session, "--", message, "Enter").CombinedOutput()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error feeding %q: %v\n%s", id, err, strings.TrimSpace(string(out)))}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("Sent to agent %q: %s", id, strings.ReplaceAll(msg, "\n", " "))}, nil
}

func (d *Delegate) pause(id string) (agent.ToolResult, error) {
	a := d.getAgent(id)
	if a == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: no such agent %q", id)}, nil
	}
	out, err := exec.Command("kill", "-STOP", strconv.Itoa(a.panePID)).CombinedOutput()
	d.mu.Lock()
	a.paused = true
	d.mu.Unlock()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("Paused agent %q (SIGSTOP to pid %d): %v", id, a.panePID, err)}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("Paused agent %q (pid %d stopped)%s", id, a.panePID, suffix(out))}, nil
}

func (d *Delegate) resume(id string) (agent.ToolResult, error) {
	a := d.getAgent(id)
	if a == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: no such agent %q", id)}, nil
	}
	out, err := exec.Command("kill", "-CONT", strconv.Itoa(a.panePID)).CombinedOutput()
	d.mu.Lock()
	a.paused = false
	d.mu.Unlock()
	if err != nil {
		return agent.ToolResult{Content: fmt.Sprintf("Resumed agent %q (SIGCONT to pid %d): %v", id, a.panePID, err)}, nil
	}
	return agent.ToolResult{Content: fmt.Sprintf("Resumed agent %q (pid %d continued)%s", id, a.panePID, suffix(out))}, nil
}

func (d *Delegate) getAgent(id string) *agentInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.agents[id]
}

// agentCommand возвращает аргументы команды «мозга» агента.
//   - env BEE_AGENT_CMD — override (для теста/своей обёртки).
//   - иначе бинарник bee ($BEE_BIN или /usr/local/bin/bee) с model+task;
//   - если bee не найден — placeholder bash, чтобы control-plane работал.
func agentCommand(task, model string) []string {
	if cmd := strings.TrimSpace(os.Getenv("BEE_AGENT_CMD")); cmd != "" {
		return strings.Fields(cmd)
	}
	beePath := os.Getenv("BEE_BIN")
	if beePath == "" {
		beePath = "/usr/local/bin/bee"
	}
	if _, err := os.Stat(beePath); err == nil {
		args := []string{beePath, "-model", model, "-task", task}
		return args
	}
	return []string{"/bin/bash", "-c", `echo "[bee] bee binary not found at ` + beePath + `; placeholder brain"; cat`}
}

func displayModel(model string) string {
	if model == "" {
		return "(default: proxyapi/deepseek/deepseek-v4.1-flash-1017)"
	}
	return model
}

func panePIDOf(session string) int {
	out, err := exec.Command("tmux", "list-panes", "-t", session, "-F", "#{pane_pid}").CombinedOutput()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func tmuxSessionAlive(session string) bool {
	_, err := exec.Command("tmux", "has-session", "-t", session).CombinedOutput()
	return err == nil
}

// lastPaneLineIsDone — true, если последний непустой вывод пейна содержит
// авто стоп-маркер bee (DONE {end}) или REPL-prompt «ready». Это однозначный
// признак, что модель агента перестала работать (не зависит от промпта).
func lastPaneLineIsDone(session string) bool {
	out, err := exec.Command("tmux", "capture-pane", "-t", session, "-p").CombinedOutput()
	if err != nil {
		return false
	}
	lines := strings.Split(string(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l == "" {
			continue
		}
		return strings.Contains(l, beeDoneMarker) || strings.HasSuffix(l, "ready (type /exit to quit): ")
	}
	return false
}

func suffix(b []byte) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	return "\n" + s
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}

// tailLines — последние n строк вывода (с конца), без висящих пустых строков.
// n<=0 → все непустые строки. Пустой ввод → "".
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	last := len(lines) - 1
	for last >= 0 && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	if last < 0 {
		return ""
	}
	start := 0
	if n > 0 && last+1-n > start {
		start = last + 1 - n
	}
	return strings.Join(lines[start:last+1], "\n")
}

// --- guard: существует ли модель для spawn (providers.json ИЛИ локально) ---

// providerEntry — минимальный срез providers.json, нужный только для проверки.
type providerEntry struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

func loadProvidersForGuard() []providerEntry {
	path := os.Getenv("OLLAMA_PROVIDERS_CONFIG")
	if path == "" {
		path = "$HOME/.ollama/providers.json"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var w struct {
		Providers []providerEntry `json:"providers"`
	}
	if json.Unmarshal(data, &w) != nil {
		return nil
	}
	return w.Providers
}

// matchModelGuard — зеркалит семантику bee/main.go matchModel: точное совпадение
// ИЛИ prefix-match по bare-имени модели провайдера (включая каноническую форму
// «provider/model»).
func matchModelGuard(providerName string, list []string, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return false
	}
	pn := strings.ToLower(providerName)
	for _, v := range list {
		lv := strings.ToLower(v)
		withPrefix := pn + "/" + lv // каноническая форма «provider/model»
		if q == lv || q == withPrefix || strings.HasPrefix(lv, q) {
			return true
		}
	}
	return false
}

// localModelCheck — есть ли локально в ollama модель с таким именем (точное имя
// или prefix). Возвращает (exists, conclusive): если сервер недоступен/ответ не
// распарсился → conclusive=false, чтобы guard не блокировал spawn ложно.
func localModelCheck(query string) (bool, bool) {
	host := os.Getenv("OLLAMA_URL")
	if host == "" {
		host = "http://127.0.0.1:11434"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(host, "/") + "/api/tags")
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	var w struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&w) != nil {
		return false, false
	}
	q := strings.ToLower(strings.TrimSpace(query))
	for _, m := range w.Models {
		name := strings.ToLower(m.Name)
		if name == q || strings.HasPrefix(name, q) {
			return true, true
		}
	}
	return false, true // список получен, совпадений нет → точно отсутствует
}

// listModels — доступные «мозги» под-агентов: облачные (providers.json)
// и локальные (ollama /api/tags). Используется action 'models'.
func (d *Delegate) listModels() agent.ToolResult {
	var b strings.Builder
	for _, p := range loadProvidersForGuard() {
		b.WriteString("cloud:" + p.Name + ":\n")
		for _, m := range p.Models {
			b.WriteString("  - " + p.Name + "/" + m + "\n")
		}
	}
	host := os.Getenv("OLLAMA_URL")
	if host == "" {
		host = "http://127.0.0.1:11434"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(strings.TrimSuffix(host, "/") + "/api/tags")
	if err == nil {
		defer resp.Body.Close()
		var w struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.NewDecoder(resp.Body).Decode(&w) == nil {
			b.WriteString("local (ollama):\n")
			for _, m := range w.Models {
				b.WriteString("  - " + m.Name + "\n")
			}
		} else {
			b.WriteString("local: failed to parse /api/tags\n")
		}
	} else {
		b.WriteString("local: ollama server unavailable\n")
	}
	if envconfig.DefaultModel != "" {
		b.WriteString("\ndefault agent brain: " + envconfig.DefaultModel)
	}
	return agent.ToolResult{Content: strings.TrimSpace(b.String())}
}

// validateSpawnModel — guard перед spawn под-агента. Пустая модель → OK (bee
// использует встроенный дефолт). Иначе модель должна существовать в
// providers.json ИЛИ локально; иначе явная ошибка с подсказкой про дефолтную
// модель, чтобы не было тишайшего 404 из HTTP.
func validateSpawnModel(model string) error {
	q := strings.TrimSpace(model)
	if q == "" {
		return nil
	}
	for _, p := range loadProvidersForGuard() {
		if matchModelGuard(p.Name, p.Models, q) {
			return nil
		}
	}
	if exists, conclusive := localModelCheck(q); exists || !conclusive {
		return nil // найдена локально ИЛИ сервер недоступен (bee покажет свою ошибку)
	}
	return fmt.Errorf("model %q not found in providers.json nor locally; use the default %s", q, envconfig.DefaultModel)
}
