package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/cmd/config"
)

func TestPredsaveDialogBeforeCompaction_DefaultOn(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	messages := []api.Message{{Role: "user", Content: "привет"}}
	path := PredsaveDialogBeforeCompaction("test-model", "sys prompt", messages)
	if path == "" {
		t.Fatal("пустой путь: по умолчанию пресейв должен быть вкл.")
	}
	if !strings.HasSuffix(path, "-predsave.json") {
		t.Fatalf("ожидалось имя с суффиксом -predsave.json, получен %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не удалось прочитать сохранённый файл: %v", err)
	}
	for _, want := range []string{`"test-model"`, "sys prompt", "привет"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON не содержит %q:\n%s", want, data)
		}
	}
}

func TestPredsaveDialogBeforeCompaction_Disabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := config.SetPredsaveCompaction(false); err != nil {
		t.Fatalf("SetPredsaveCompaction: %v", err)
	}
	path := PredsaveDialogBeforeCompaction("test-model", "", []api.Message{{Role: "user", Content: "x"}})
	if path != "" {
		t.Fatalf("ожидалось пустого пути при выключенном пресейве, получен %s", path)
	}
}

func TestPredsaveCompactionEnabled_LegacyConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if !config.PredsaveCompactionEnabled() {
		t.Fatal("legacy-конфиг без поля predsave_compaction должен означать ВКЛ")
	}
}
