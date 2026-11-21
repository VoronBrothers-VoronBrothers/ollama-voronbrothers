package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Периодическое сохранение ПОЛНОЙ истории субагента.
//
// bee пишет JSON-снапшоты в:
//   ~/.ollama/subagent/chats_subagent/<YYYY-MM-DD-HH-MM-SS>-delegate.json
//
// Каждый снапшот содержит ВСЮ историю сессии целиком (не «окно»):
// все tool calls от начала, поправки супервизора и финальный ответ —
// чтобы ни один кусок диалога не терялся.
//
// Когда пишется файл:
//   1) каждые saveEveryN tool-вызовов (чекпоинт во время долгой работы);
//   2) при каждом DONE (final answer / error / max rounds — через emitDone);
//   3) при выходе процесса (defer в main).
//
// Формат пригоден для post-mortem анализа: задача, модель, раунды, полная
// история инструментов с аргументами и результатами.

const (
	saveEveryN     = 10   // чекпоинт каждые N tool-вызовов во время работы
	maxSavedArgs   = 4096 // потолок аргументов одного tool call в сохранении
	maxSavedResult = 8192 // потолка результата одного tool call в сохранении
)

// saveEntry — один tool call в полной истории.
type saveEntry struct {
	Name      string `json:"name"`
	Args      string `json:"args,omitempty"`   // полные аргументы (с потолком maxSavedArgs)
	ResultLen int    `json:"result_bytes"`     // полный размер результата, байт
	Result    string `json:"result,omitempty"` // результат целиком (с потолком maxSavedResult)
	Timestamp string `json:"timestamp"`
}

// saveSnapshot — содержимое JSON-файла: полная история на момент записи.
type saveSnapshot struct {
	Timestamp      string      `json:"timestamp"`
	Session        string      `json:"session,omitempty"`
	Model          string      `json:"model,omitempty"`
	Task           string      `json:"task,omitempty"`
	Round          int         `json:"round"`
	RoundsLeft     int         `json:"rounds_left"`
	ToolCallsTotal int         `json:"tool_calls_total"`
	Status         string      `json:"status"`
	Phase          string      `json:"phase,omitempty"`
	History        []saveEntry `json:"history"` // ВСЕ tool calls с начала сессии
	Corrections    []string    `json:"corrections,omitempty"`
	FinalAnswer    string      `json:"final_answer,omitempty"`
}

// saveState — горутинобезопасный счётчик + полная история (без отбрасывания).
type saveState struct {
	mu          sync.Mutex
	count       int
	all         []saveEntry // неограниченная полная история tool calls
	corrections []string    // поправки супервизора (feed)
	finalAnswer string      // ответ модели на последнюю завершённую задачу
}

var beeSave = &saveState{}

// chatsDir — папка для сохранений.
func chatsDir() string {
	dir := filepath.Join(subAgentDir(), "chats_subagent")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// capStr — режет строку до n символов с маркером и полным размером.
func capStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("…[truncated; %d bytes total]", len(s))
}

// recordToolCall вызывается после каждого tool-вызова в dispatchTool.
// Добавляет ПОЛНЫЙ вызов в историю и возвращает true, если пора писать чекпоинт.
func (s *saveState) recordToolCall(name string, argsFull, result string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.count++
	s.all = append(s.all, saveEntry{
		Name:      name,
		Args:      capStr(argsFull, maxSavedArgs),
		ResultLen: len(result),
		Result:    capStr(result, maxSavedResult),
		Timestamp: time.Now().Format("15:04:05"),
	})
	return s.count%saveEveryN == 0
}

// noteCorrection — поправка супервизора (delegate feed) тоже часть истории.
func (s *saveState) noteCorrection(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.corrections = append(s.corrections, truncate(p, 2048))
}

// setFinalAnswer — ответ модели на завершённую задачу.
func (s *saveState) setFinalAnswer(t string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalAnswer = t
}

// writeSnapshot пишет текущую ПОЛНУЮ историю в файл.
func (s *saveState) writeSnapshot(model, task string) {
	s.mu.Lock()
	count := s.count
	all := make([]saveEntry, len(s.all))
	copy(all, s.all)
	corrections := make([]string, len(s.corrections))
	copy(corrections, s.corrections)
	answer := s.finalAnswer
	s.mu.Unlock()

	// Берём актуальное состояние bee.
	st := getBeeState()

	snap := saveSnapshot{
		Timestamp:      time.Now().Format("2006-01-02T15-04-05"),
		Session:        st.Session,
		Model:          model,
		Task:           truncate(task, 1000),
		Round:          st.Round,
		RoundsLeft:     st.RoundsLeft,
		ToolCallsTotal: count,
		Status:         st.Status,
		Phase:          st.Phase,
		History:        all,
		Corrections:    corrections,
		FinalAnswer:    truncate(answer, 8000),
	}

	ts := time.Now().Format("2006-01-02-15-04-05")
	path := filepath.Join(chatsDir(), fmt.Sprintf("%s-delegate.json", ts))

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
