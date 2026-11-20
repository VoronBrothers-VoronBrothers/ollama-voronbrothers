package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/cmd/config"
)

func TestDialogLogger_AppendDedup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dl, err := newDialogLogger()
	if err != nil {
		t.Fatalf("newDialogLogger: %v", err)
	}
	defer dl.Close()
	if !strings.HasSuffix(dl.Path(), "-dialog.jsonl") {
		t.Fatalf("ожидалось имя с суффиксом -dialog.jsonl, получен %s", dl.Path())
	}

	msgs := []api.Message{{Role: "user", Content: "привет"}, {Role: "assistant", Content: "здравствуй"}}
	if n := dl.Append(msgs); n != 2 {
		t.Fatalf("ожидалось 2 новых записи, получено %d", n)
	}
	// Повторная отправка той же истории (следующий Run) — дублей быть не должно.
	if n := dl.Append(msgs); n != 0 {
		t.Fatalf("повторный Append должен вернуть 0, получен %d", n)
	}
	// Новое сообщение записывается, старое — нет.
	msgs = append(append([]api.Message{}, msgs...), api.Message{Role: "user", Content: "ещё"})
	if n := dl.Append(msgs); n != 1 {
		t.Fatalf("ожидалось 1 новая запись, получено %d", n)
	}

	data, err := os.ReadFile(dl.Path())
	if err != nil {
		t.Fatalf("читать лог: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("ожидалось 3 строки в логе, получено %d", len(lines))
	}
	var last dialogLogLine
	if err := json.Unmarshal([]byte(lines[2]), &last); err != nil {
		t.Fatalf("парсинг JSONL: %v", err)
	}
	if last.Msg.Content != "ещё" || last.TS == "" {
		t.Errorf("последняя строка некорректна: %+v", last)
	}
}

func TestDialogLogger_CloseThenAppend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dl, err := newDialogLogger()
	if err != nil {
		t.Fatalf("newDialogLogger: %v", err)
	}
	if n := dl.Append([]api.Message{{Role: "user", Content: "x"}}); n != 1 {
		t.Fatalf("ожидалось 1 запись, получено %d", n)
	}
	if err := dl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := dl.Append([]api.Message{{Role: "user", Content: "y"}}); n != 0 {
		t.Fatalf("после Close Append должен вернуть 0, получен %d", n)
	}
}

func TestEnsureDialogLog_GatedByToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.SetPredsaveCompaction(false); err != nil {
		t.Fatalf("SetPredsaveCompaction: %v", err)
	}
	s := &Session{}
	if dl := s.ensureDialogLog(); dl != nil {
		t.Fatal("выключенный пресейв не должен создавать лог диалога")
	}
	if err := config.SetPredsaveCompaction(true); err != nil {
		t.Fatalf("SetPredsaveCompaction: %v", err)
	}
	s = &Session{}
	dl := s.ensureDialogLog()
	if dl == nil {
		t.Fatal("включённый пресейв должен создать лог диалога")
	}
	defer dl.Close()
	// Повторный вызов возвращает тот же логер.
	if s.ensureDialogLog() != dl {
		t.Fatal("ensureDialogLog не идемпотентен")
	}
}
