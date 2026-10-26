package version

import (
	"fmt"
	"os"
)

// ВНИМАНИЕ / WARNING: эта константа — только FALLBACK-дефолт, НЕ актуальная версия!
//
// Настоящая версия вставляется при сборке через ldflags по VORON-RUNBOOK.md §3:
//
//	go build -ldflags "-X github.com/ollama/ollama/version.Version=<АПСТРИМ>-voron-$(date +%d%m%Y)" .
//
// Правила именования (не выводить версию из этого файла!):
//   - база = версия upstream (на момент 26.10.2026 — 0.40.6);
//   - патчи своего кода без изменения upstream-базы → та же база, меняется только дата суффикса;
//   - версия не должна уменьшаться относительно предыдущей сборки.
//
// Если бинарник собрали без ldflags — Version равен fallbackVersion и init()
// выводит в stderr напоминание посмотреть ранбук.

const fallbackVersion = "0.40.6-voron"

var Version string = fallbackVersion

func init() {
	if Version == fallbackVersion {
		fmt.Fprintln(os.Stderr, "[version] WARNING: стоит встроенная fallback-версия "+Version)
		fmt.Fprintln(os.Stderr, "[version]         похоже, собирали БЕЗ ldflags -X .../version.Version=...")
		fmt.Fprintln(os.Stderr, "[version]         именование и сборка — VORON-RUNBOOK.md §3:")
		fmt.Fprintf(os.Stderr, "[version]         go build -ldflags \"-X github.com/ollama/ollama/version.Version=%s-$(date +%%d%%m%%Y)\" .\n", fallbackVersion[:len(fallbackVersion)-len("-voron")])
	}
}
