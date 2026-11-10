// Command lama — быстрый запуск полноценного TUI (bubbletea) с моделью.
//
// Использование:
//
//	lama              → TUI с моделью "ollama-model" (или LAMA_MODEL из env)
//	lama mymodel      → TUI с моделью "mymodel"
package main

import (
	"os"

	"github.com/ollama/ollama/cmd"
	"github.com/ollama/ollama/envconfig"
)

const defaultModel = "ollama-model"

func main() {
	model := envconfig.Var("LAMA_MODEL")
	if model == "" {
		model = defaultModel
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
