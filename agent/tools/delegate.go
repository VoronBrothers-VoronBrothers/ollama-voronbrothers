package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ollama/ollama/agent"
	"github.com/ollama/ollama/api"
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
		"'output' reads the agent's terminal output; " +
		"'stop' kills one; 'feed' sends it a message (at start or mid-run); " +
		"'pause'/'resume' freeze/unfreeze its process. Sub-agents have full freedom; " +
		"you only monitor and steer."
}

func (d *Delegate) Schema() api.ToolFunction {
	props := api.NewToolPropertiesMap()
	props.Set("action", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "One of: spawn, list, output, stop, feed, pause, resume.",
	})
	props.Set("task", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Task text for 'spawn'.",
	})
	props.Set("model", api.ToolProperty{
		Type:        api.PropertyType{"string"},
		Description: "Model name for the agent (default: deepseek via cloud provider). Optional.",
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
		return agent.ToolResult{Content: "Error: unknown action '" + action + "'. Use spawn|list|output|stop|feed|pause|resume."}, nil
	}
}

func (d *Delegate) spawn(cwd, task, model, id string) (agent.ToolResult, error) {
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
		return "(default: deepseek via cloud provider)"
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
