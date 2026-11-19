package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Файл состояния под-агента.
//
// Зачем: control-plane (инструмент `delegate` в основной модели) должен ЯВНО
// видеть, что под-агент жив и на каком шаге, а также однозначно понимать
// «закончил работу». Разбор текста пейна (tmux capture-pane) для этого слабый:
// любой многоточие/эхо ломает эвристику. Поэтому bee пишет свой статус в
// небольшой JSON-файл (атомарная замена через rename) и обновляет в нём
// heartbeat каждые 5 секунд. Свежесть UpdatedAt = «процесс жив и думает».
//
// Путь задаёт delegate через env BEE_STATE; при ручном запуске bee файл
// кладётся в $TMPDIR/bee-state-<pid>.json.
const (
	statusStarting  = "starting"   // процесс стартовал, задача ещё не взята
	statusWorking   = "working"    // крутит tool-loop по задаче
	statusIdle      = "idle"       // задача ВЫПОЛНЕНА, ждёт новую (/exit)
	statusError     = "error"      // ошибка модели/фатальная ошибка
	statusMaxRounds = "max-rounds" // кончился бюджет раундов
	statusExited    = "exited"     // bee завершился (файл остаётся для истории)
)

// stateHeartbeatEvery — как часто обновляем UpdatedAt, пока процесс жив.
// delegate считает статус «завис», если UpdatedAt старше beeStaleAfter.
const stateHeartbeatEvery = 5 * time.Second

// beeState — содержимое state-файла. Все поля экспортируются: файл читают
// другие процессы (delegate) по JSON-именам.
type beeState struct {
	PID        int    `json:"pid"`
	Session    string `json:"session,omitempty"`
	Model      string `json:"model,omitempty"`
	Task       string `json:"task,omitempty"`
	Status     string `json:"status"`
	Phase      string `json:"phase,omitempty"` // человекочитаемо: "model call", "run_command: make -j8"
	Tool       string `json:"tool,omitempty"`
	Round      int    `json:"round"`
	RoundsLeft int    `json:"rounds_left"`
	// Waiting — истина, когда bee закончил задачу и ждёт следующую в REPL.
	// Отдельный флаг (а не status), чтобы не потерять РЕЗУЛЬТАТ задачи:
	// «упал на http 500 и ждёт» — это error+waiting, а не «успешно закончил».
	Waiting    bool      `json:"waiting"`
	Reason     string    `json:"reason,omitempty"` // для error / max-rounds
	StartedAt  time.Time `json:"started_at"`
	PhaseSince time.Time `json:"phase_since,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// stateFile — горутинобезопасная обёртка над файлом состояния.
type stateFile struct {
	mu   sync.Mutex
	path string
	st   beeState
}

var beeStateFile = &stateFile{}

// initStateFile подготавливает файл состояния: путь из BEE_STATE (или
// $TMPDIR/bee-state-<pid>.json) и начальный статус «starting».
func initStateFile(model string) {
	path := os.Getenv("BEE_STATE")
	if path == "" {
		path = filepath.Join(os.TempDir(), fmt.Sprintf("bee-state-%d.json", os.Getpid()))
	}
	now := time.Now()
	beeStateFile.mu.Lock()
	beeStateFile.path = path
	beeStateFile.st = beeState{
		PID:        os.Getpid(),
		Session:    os.Getenv("BEE_SESSION"),
		Model:      model,
		Status:     statusStarting,
		Phase:      "startup",
		StartedAt:  now,
		PhaseSince: now,
		UpdatedAt:  now,
	}
	beeStateFile.mu.Unlock()
	beeStateFile.save()
}

// statePath — путь state-файла ("" если не инициализирован).
func statePath() string {
	beeStateFile.mu.Lock()
	defer beeStateFile.mu.Unlock()
	return beeStateFile.path
}

// updateState применяет мутацию к состоянию и сохраняет файл.
// UpdatedAt обновляется всегда; PhaseSince — только при смене Phase
// (delegate показывает «сколько висит текущий шаг»).
func updateState(mutate func(*beeState)) {
	beeStateFile.mu.Lock()
	prevPhase := beeStateFile.st.Phase
	mutate(&beeStateFile.st)
	beeStateFile.st.UpdatedAt = time.Now()
	if beeStateFile.st.Phase != prevPhase {
		beeStateFile.st.PhaseSince = beeStateFile.st.UpdatedAt
	}
	beeStateFile.mu.Unlock()
	beeStateFile.save()
}

// touchState — только heartbeat: обновляет UpdatedAt (статус/фазу не трогает).
func touchState() {
	beeStateFile.mu.Lock()
	beeStateFile.st.UpdatedAt = time.Now()
	beeStateFile.mu.Unlock()
	beeStateFile.save()
}

// save пишет JSON атомарно: во временный файл рядом и rename поверх.
// Так читатель никогда не увидит полу-записанный JSON.
func (s *stateFile) save() {
	s.mu.Lock()
	if s.path == "" {
		s.mu.Unlock()
		return
	}
	data, err := json.Marshal(s.st)
	path := s.path
	s.mu.Unlock()
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// startHeartbeat запускает фоновое обновление UpdatedAt. Возвращает функцию
// остановки (для тестов и аккуратного выхода).
func startHeartbeat() func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(stateHeartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				touchState()
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}
