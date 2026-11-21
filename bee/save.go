package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Периодическое сохранение состояния субагента.
//
// Каждые N вызовов любого инструмента bee пишет JSON-снапшот в:
//   ~/.ollama/subagent/chats_subagent/<YYYY-MM-DD-HH-MM-SS>-delegate.json
//
// Это аналог /save основного агента, но автоматический (не по команде).
// Формат файла пригоден для post-mortem анализа и передаёт полный контекст:
// задача, модель, раунды, последние tool calls.

const saveEveryN = 10 // каждые N tool-вызовов — один файл

// saveEntry — одна запись в recent_tools (сжатое описание tool call).
type saveEntry struct {
	Name      string `json:"name"`
	ArgsBrief string `json:"args_brief,omitempty"`
	ResultLen int    `json:"result_bytes"`
	Timestamp string `json:"timestamp"`
}

// saveSnapshot — содержимое JSON-файла.
type saveSnapshot struct {
	Timestamp     string      `json:"timestamp"`
	Session       string      `json:"session,omitempty"`
	Model         string      `json:"model,omitempty"`
	Task          string      `json:"task,omitempty"`
	Round         int         `json:"round"`
	RoundsLeft    int         `json:"rounds_left"`
	ToolCallsTotal int        `json:"tool_calls_total"`
	Status        string      `json:"status"`
	Phase         string      `json:"phase,omitempty"`
	RecentTools   []saveEntry `json:"recent_tools"`
}

// saveState — горутинобезопасный счётчик + буфер последних tool calls.
type saveState struct {
	mu     sync.Mutex
	count  int
	recent []saveEntry // последние N, старые выкидываются
}

var beeSave = &saveState{}

// chatsDir — папка для сохранений.
func chatsDir() string {
	dir := filepath.Join(subAgentDir(), "chats_subagent")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// recordToolCall вызывается после каждого tool-вызова в dispatchTool.
// Возвращает true если нужно сделать save (каждые N).
func (s *saveState) recordToolCall(name string, argsBrief string, resultLen int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	entry := saveEntry{
		Name:      name,
		ArgsBrief: truncate(argsBrief, 200),
		ResultLen: resultLen,
		Timestamp: time.Now().Format("15:04:05"),
	}
	s.recent = append(s.recent, entry)
	if len(s.recent) > saveEveryN {
		s.recent = s.recent[len(s.recent)-saveEveryN:]
	}
	return s.count%saveEveryN == 0
}

// writeSnapshot пишет текущий state-снапшот в файл.
func (s *saveState) writeSnapshot(model, task string) {
	s.mu.Lock()
	count := s.count
	recent := make([]saveEntry, len(s.recent))
	copy(recent, s.recent)
	s.mu.Unlock()

	// Берём актуальное состояние bee.
	st := getBeeState()
	phase := st.Phase
	status := st.Status
	round := st.Round
	roundsLeft := st.RoundsLeft
	session := st.Session

	snap := saveSnapshot{
		Timestamp:     time.Now().Format("2006-01-02T15-04-05"),
		Session:       session,
		Model:         model,
		Task:          truncate(task, 500),
		Round:         round,
		RoundsLeft:    roundsLeft,
		ToolCallsTotal: count,
		Status:        status,
		Phase:         phase,
		RecentTools:   recent,
	}

	ts := time.Now().Format("2006-01-02-15-04-05")
	filename := fmt.Sprintf("%s-delegate.json", ts)
	path := filepath.Join(chatsDir(), filename)

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

// truncate — режет строку до n символов (для компактности в JSON).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
