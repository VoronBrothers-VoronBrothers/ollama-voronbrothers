// Command lama — быстрый запуск полноценного TUI (bubbletea) с моделью.
//
// Использование:
//
//	lama              → TUI с моделью по умолчанию (envconfig.DefaultModel, deepseek; override: LAMA_MODEL)
//	lama mymodel      → TUI с моделью "mymodel"
package main

import (
	"os"

	"github.com/ollama/ollama/cmd"
	"github.com/ollama/ollama/envconfig"
)

func main() {
	// Built-in default (no LAMA_MODEL set) is the single canonical source in
	// envconfig.DefaultModel — deepseek. Override at runtime with LAMA_MODEL.
	model := envconfig.Var("LAMA_MODEL")
	if model == "" {
		model = envconfig.DefaultModel
	}

	args := os.Args[1:]
	if len(args) > 0 && args[0] != "-v" && args[0] != "--version" {
		model = args[0]
	}

	if err := cmd.LaunchModelTUI(model); err != nil {
		os.Stderr.WriteString("lama: " + err.Error() + "\n")
		os.Exit(1)
	}
}
