package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Ghost sweep: живые «мозги» bee, которые не зарегистрированы в этом
// delegate. Источник истины — процесс в памяти (/proc), а НЕ tmux list и не
// state-файлы: сессия может умереть (tmux kill-session, крах tmux-сервера,
// перезагрузка), state-файл стёрт, а мозг продолжать работать «невидимо».
//
// Как отличить «мозг агента» от любого другого процесса: при spawn мы
// через withStateEnv экспортируем BEE_STATE и BEE_SESSION — их наличие в
// /proc/<pid>/environ означает процесс запущенный delegate (любой бumblebee),
// а не интерактивное окно.

// ghostBrain — найденный вне реестра живой мозг.
type ghostBrain struct {
	pid       int
	session   string // BEE_SESSION либо имя state-файла без .json
	statePath string // BEE_STATE
}

// findGhosts сканирует /proc и возвращает живые мозги с env BEE_STATE/
// BEE_SESSION, не считая собственный процесс.
func findGhosts() []ghostBrain {
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var ghosts []ghostBrain
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue // self — мы сами (интерактивная сессия, не «мозг»)
		}
		if _, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat")); err != nil {
			continue // процесс исчез во время скана — ок
		}
		envs, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		statePath, session := parseBeeEnv(string(envs))
		if statePath == "" && session == "" {
			continue
		}
		ghosts = append(ghosts, ghostBrain{pid: pid, session: session, statePath: statePath})
	}
	return ghosts
}

// parseBeeEnv вытаскивает BEE_STATE/BEE_SESSION из /proc/<pid>/environ
// (NUL-разделённые KEY=VALUE).
func parseBeeEnv(environ string) (statePath, session string) {
	for _, kv := range strings.Split(environ, "\x00") {
		switch {
		case strings.HasPrefix(kv, "BEE_STATE="):
			statePath = strings.TrimPrefix(kv, "BEE_STATE=")
		case strings.HasPrefix(kv, "BEE_SESSION="):
			session = strings.TrimPrefix(kv, "BEE_SESSION=")
		}
	}
	return statePath, session
}

// sweepGhosts adopt'ит найденные мозги в d.agents. После этого они видны в
// list() со статусной строкой (⚠️ ORPHANED / 👻 GHOST), а stop/feed работают по id.
// Возвращает число новых.
func (d *Delegate) sweepGhosts() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	adopted := 0
	for _, g := range findGhosts() {
		if g.session == "" && g.statePath != "" {
			g.session = strings.TrimSuffix(filepath.Base(g.statePath), ".json")
		}
		id := strings.TrimPrefix(g.session, "bee-")
		if id == "" || id == g.session { // нет префикса bee- — соберём id из pid
			id = fmt.Sprintf("ghost-%d", g.pid)
		}
		if a, ok := d.agents[id]; ok {
			// Агент уже в реестре (spawn из этой сессии), но spawn не знает
			// brain-pid — backfill'им его: иначе ghost без state-файла будет
			// виден как DEAD и stop() не найдёт что убить.
			if a.brainPid == 0 {
				a.brainPid = g.pid
			}
			continue
		}
		spawnedAt, task, model := time.Now(), "", ""
		if st, ok := readBeeState(g.statePath); ok && !st.StartedAt.IsZero() {
			spawnedAt, task, model = st.StartedAt, st.Task, st.Model
		}
		d.agents[id] = &agentInfo{
			id: id, session: g.session, panePID: 0, brainPid: g.pid,
			task: task, model: model, spawnedAt: spawnedAt, statePath: g.statePath,
		}
		adopted++
	}
	return adopted
}

// liveBrainPID — живой pid мозга (из adopt'а или state-файла), 0 если мёртв/нет.
func (d *Delegate) liveBrainPID(a *agentInfo) int {
	candidates := []int{a.brainPid}
	if st, ok := readBeeState(a.statePath); ok {
		candidates = append(candidates, st.PID)
	}
	for _, p := range candidates {
		if p > 0 && procAlive(p) {
			return p
		}
	}
	return 0
}
