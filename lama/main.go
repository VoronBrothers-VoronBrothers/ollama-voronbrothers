// Command lama — быстрый запуск полноценного TUI (bubbletea) с моделью.
//
// Использование:
//
//	lama              → TUI с моделью из LAMA_MODEL (обязательная переменная)
//	lama mymodel      → TUI с моделью "mymodel"
package main

import (
	"os"

	"github.com/ollama/ollama/cmd"
	"github.com/ollama/ollama/envconfig"
)

func main() {
	// LAMA_MODEL — обязательная (process env / ~/.ollama/env / /etc/ollama/env),
	// встроенного fallback'а нет.
	model := envconfig.Var("LAMA_MODEL")
	if model == "" {
		os.Stderr.WriteString("lama: LAMA_MODEL is not set (process env / ~/.ollama/env / /etc/ollama/env) — specify the TUI model\n")
		os.Exit(2)
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
