package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
	id          string
	session     string // tmux session name (уникальный, prefix bee-)
	panePID     int    // pid верхнего процесса в пейне (для pause/resume)
	task        string
	model       string
	paused      bool
	stoppedPids []int // все pids замороженные на паузе (для resume)
	spawnedAt   time.Time
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
	// ОДНА строка без `--`: tmux оборачивает её в собственный sh -c — это
	// важно, см. agentCommand (setsid не может быть прямым ребёнком пейна).
	out, err := exec.CommandContext(context.Background(), "tmux",
		"new-session", "-d", "-s", session, "-c", cwd,
		strings.Join(cmdArgs, " ")).CombinedOutput()
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
		status := "▶ working"
		if a.paused {
			frozen := 0
			for _, pid := range a.stoppedPids {
				if procState(pid) == "T" {
					frozen++
				}
			}
			if frozen > 0 {
				status = fmt.Sprintf("⏸ PAUSED (frozen %d pid)", frozen)
			} else if tmuxSessionAlive(a.session) {
				// Пауза «пометка» есть, но никто не застыл — честно пишем,
				// что freeze потерян (например, всё дерево завершилось само).
				status = "⚠️ PAUSE LOST"
			} else {
				status = "⏸ paused (session gone)"
			}
		} else if !tmuxSessionAlive(a.session) {
			status = "💀 dead"
		} else if lastPaneLineIsDone(a.session) {
			status = "✅ done"
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
	status := ""
	if !lastPaneLineIsDone(a.session) {
		status = "\n⏳ AGENT IS STILL WORKING / АГЕНТ ЕЩЁ РАБОТАЕТ"
	}
	return agent.ToolResult{Content: fmt.Sprintf("[agent %s] (last %d lines)\n%s%s", id, n, text, status)}, nil
}

func (d *Delegate) stop(id string) (agent.ToolResult, error) {
	a := d.getAgent(id)
	if a == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: no such agent %q", id)}, nil
	}
	// Снимаем дерево ДО убийства сессии: tmux kill-session шлёт сигналы только
	// своим прямым детям, а setsid-мозг (и его дети) в чужой session — они бы
	// выжили как сироты. Убиваем явно.
	root := a.panePID
	if p := panePIDOf(a.session); p > 0 {
		root = p
	}
	tree := processTree(root)
	out, _ := exec.Command("tmux", "kill-session", "-t", a.session).CombinedOutput()
	killed := 0
	for _, pid := range tree {
		if pid <= 0 || procState(pid) == "" {
			continue // уже не жив (tmux убил вместе с пейном)
		}
		_ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run()
		killed++
	}
	d.mu.Lock()
	delete(d.agents, id)
	d.mu.Unlock()
	extra := ""
	if killed > 0 {
		extra = fmt.Sprintf(", force-killed %d leftover process(es)", killed)
	}
	return agent.ToolResult{Content: fmt.Sprintf("Stopped agent %q (killed tmux session %s%s)%s", id, a.session, extra, suffix(out))}, nil
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

// pause — жёсткая пауза: SIGSTOP всему процессному дереву пейна сессии.
//
// Важно: останавливать ОДИН pane_pid недостаточно. tmux запускает команду
// через shell-обёртку, а «мозг» (bee) и его дети (persistent bash, висящие
// run_command) — отдельные процессы; SIGSTOP не наследуется. Если exec-
// оптимизация шелла не сработала (спецсимволы в task), pane_pid ≠ pid мозга —
// и агент продолжал работать после «паузы». Поэтому замораживаем всё дерево:
// root первым (замороженный родитель больше никого не породит), затем все
// потомки.
//
// Второй нюанс: tmux-сервер сам шлёт SIGCONT процессам пейна, почти сразу
// после STOP. Обёртка шелла в любом случае оттаивает — это безвредно (она
// просто ждёт), а мозг запускается через setsid (см. agentCommand) и потому
// остаётся замороженным. После остановки даём tmux «отстреляться» SIGCONT
// (~150мс) и честно отчитываемся, сколько процессов реально удержалось в T.
func (d *Delegate) pause(id string) (agent.ToolResult, error) {
	a := d.getAgent(id)
	if a == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: no such agent %q", id)}, nil
	}
	// Пейн мог сменить root-процесс — берём актуальный pid.
	root := a.panePID
	if p := panePIDOf(a.session); p > 0 {
		root = p
		d.mu.Lock()
		a.panePID = p
		d.mu.Unlock()
	}
	// Сначала замораживаем root, И ТОЛЬ ПОТОМ снимаем /proc: замороженный
	// родитель новых детей породить не может → снапшот дерева стабильный,
	// «проскочивший» child (mid-fork на момент скана) не потеряется.
	_ = exec.Command("kill", "-STOP", strconv.Itoa(root)).Run()
	tree := processTree(root)
	stopped, failed := 1, 0 // root уже остановлен (или мёртв — не важно)
	for _, pid := range tree[1:] {
		if pid <= 0 {
			continue // kill -STOP 0 — сигнал всей группе вызывающего!
		}
		if err := exec.Command("kill", "-STOP", strconv.Itoa(pid)).Run(); err == nil {
			stopped++
		} else {
			failed++ // процесс мог уже умереть — не страшно
		}
	}
	// Ждём, пока tmux-сервер отправит свои SIGCONT, и считаем, кто реально застыл.
	time.Sleep(150 * time.Millisecond)
	holding := 0
	for _, pid := range tree {
		if procState(pid) == "T" {
			holding++
		}
	}
	d.mu.Lock()
	a.paused = true
	a.stoppedPids = tree
	d.mu.Unlock()
	msg := fmt.Sprintf("Paused agent %q: SIGSTOP to %d process(es) (tree of pid %d), %d already gone; %d holding freeze", id, stopped, root, failed, holding)
	if holding == 0 {
		msg += " — WARNING: nothing stayed frozen (brain not setsid-detached?)"
	}
	return agent.ToolResult{Content: msg}, nil
}

// procState — состояние процесса из /proc/<pid>/stat ('R','S','T',...).
// Пусто, если процесс мёртв.
func procState(pid int) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	idx := strings.LastIndex(string(data), ")")
	if idx < 0 || len(data) <= idx+2 {
		return ""
	}
	fields := strings.Fields(string(data[idx+2:]))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// resume — SIGCONT всем pids, замороженным на паузе. Мертвые процессы не ошибка.
func (d *Delegate) resume(id string) (agent.ToolResult, error) {
	a := d.getAgent(id)
	if a == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: no such agent %q", id)}, nil
	}
	d.mu.Lock()
	pids := a.stoppedPids
	if len(pids) == 0 {
		pids = processTree(a.panePID) // пауза не записала список (старая сессия)
	}
	a.paused = false
	a.stoppedPids = nil
	d.mu.Unlock()
	continued, failed := 0, 0
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if err := exec.Command("kill", "-CONT", strconv.Itoa(pid)).Run(); err == nil {
			continued++
		} else {
			failed++ // процесс завершился во время паузы — нормально
		}
	}
	return agent.ToolResult{Content: fmt.Sprintf("Resumed agent %q: unfroze %d process(es), %d already exited", id, continued, failed)}, nil
}

// processTree возвращает root и всех его потомков (BFS: родитель раньше детей,
// так что при последовательном SIGSTOP новые дети во время обхода не появятся).
func processTree(root int) []int {
	children := map[int][]int{}
	entries, err := os.ReadDir("/proc")
	if err != nil || root <= 0 {
		return []int{root}
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue // процесс исчез между ReadDir и ReadFile — ок
		}
		children[parsePPID(string(data))] = append(children[parsePPID(string(data))], pid)
	}
	tree := []int{root}
	queue := []int{root}
	// seen — защита от циклов в ppid-графе (гонки /proc, pid-reuse):
	// без него BFS может зациклиться и бесконечно раздувать память.
	seen := map[int]bool{root: true}
	// ВАЖНО: не `cur, queue := ...` — это бы создало новую локальную
	// переменную queue (тенирование), appends летели бы в неё,
	// а внешний queue остался бы {root} → бесконечный цикл.
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, k := range children[cur] {
			if !seen[k] {
				seen[k] = true
				tree = append(tree, k)
				queue = append(queue, k)
			}
		}
	}
	return tree
}

// parsePPID — ppid (поле 4) из /proc/<pid>/stat. comm-поле может содержать
// пробелы и скобки, поэтому режем после последней ')': дальше идёт "state ppid ...".
func parsePPID(stat string) int {
	idx := strings.LastIndex(stat, ")")
	if idx < 0 || len(stat) <= idx+2 {
		return 0
	}
	fields := strings.Fields(stat[idx+2:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
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
//
// Команда оборачивается в `bash -c 'exec setsid -- ...'`: мозг попадает в
// собственную process session. Это критично для pause(): tmux-сервер шлёт
// SIGCONT процессам пейна почти сразу после STOP, размораживая их;
// setsid-детаченный процесс от этих SIGCONT невосприимчив и остаётся
// замороженным.
//
// НЮАНС: `setsid` НЕ может быть прямым ребёнком tmux-пейна. Пейн — group
// leader, setsid() падает с EPERM, util-linux форкает себя, родитель
// завершается → tmux видит «процесс пейна умер» и убивает пейн, а мозг
// остаётся сиротой (работает, но вне дерева/управления). Поэтому setsid
// запускаем из шелла: child шелла не group leader — setsid() проходит in-place.
func agentCommand(task, model string) []string {
	brain := brainArgs(task, model)
	var b strings.Builder
	for i, a := range brain {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellQuote(a))
	}
	// ОДНА строка: /bin/bash -c 'setsid -- <args>; exit $?'.
	// setsid БЕЗ exec, как ребёнок нашего bash: с `exec` tmux-пейн умирал
	// (EPERM-fork fallback убивал родителя), а обычный child проходит
	// setsid() in-place. bash не group leader → fork не нужен.
	return []string{"/bin/bash -c " + shellQuote("setsid -- " + b.String() + "; exit $?")}
}

// brainArgs — «голая» команда мозга без setsid-обёртки.
func brainArgs(task, model string) []string {
	if cmd := strings.TrimSpace(os.Getenv("BEE_AGENT_CMD")); cmd != "" {
		// Обёртка bash -c: строка с кавычками/спецсимволами не ломается
		// от простого space-split (strings.Fields). Это test-only override.
		return []string{"/bin/bash", "-c", cmd}
	}
	beePath := os.Getenv("BEE_BIN")
	if beePath == "" {
		beePath = "/usr/local/bin/bee"
	}
	if _, err := os.Stat(beePath); err == nil {
		return []string{beePath, "-model", model, "-task", task}
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
