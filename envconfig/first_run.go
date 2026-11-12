package envconfig

import (
	"fmt"
	"os"
	"path/filepath"
)

const envFileTemplate = `# =====================================================================
# Ollama — единая конфигурация (автосоздан при первом запуске).
#
# Читается ДВУМЯ путями:
#   1) systemd: EnvironmentFile=/etc/ollama/env (юнит ollama.service)
#   2) Go-код: envconfig fallback для процессов вне systemd (TUI/lama)
# Приоритет: process-env > ~/.ollama/env > этот файл.
# Формат: KEY=VALUE, комментарии строкой с #. Без inline-комментариев.
# =====================================================================

# -------------------------------------------------------------------
# СЕРВЕР — куда слушать входящие (bind address)
# -------------------------------------------------------------------

# Слушать на всех интерфейсах (доступ с других машин по LAN):
OLLAMA_BIND=0.0.0.0:11434

# Только локально (безопасно, только эта машина):
# OLLAMA_BIND=127.0.0.1:11434

# -------------------------------------------------------------------
# КЛИЕНТ — куда TUI/CLI ходит за моделями
# -------------------------------------------------------------------

# Подключиться к локальному серверу (по умолчанию):
OLLAMA_URL=127.0.0.1:11434

# Подключиться к удалённому серверу (пример):
# OLLAMA_URL=192.168.0.13:14434

# -------------------------------------------------------------------
# СОВМЕСТНОСТЬ (старая переменная, работает если OLLAMA_BIND/URL не заданы)
# -------------------------------------------------------------------
# OLLAMA_HOST=127.0.0.1:11434

# -------------------------------------------------------------------
# МОДЕЛИ И ХРАНИЛИЩЕ
# -------------------------------------------------------------------

# Каталог моделей (по умолчанию ~/.ollama/models):
# OLLAMA_MODELS=/usr/share/ollama/.ollama/models

# Как долго модель остаётся в VRAM после последнего запроса:
OLLAMA_KEEP_ALIVE=20m

# Сколько моделей одновременно загружать:
OLLAMA_MAX_LOADED_MODELS=2

# Параллельных запросов (одновременно генерить):
OLLAMA_NUM_PARALLEL=1

# -------------------------------------------------------------------
# GPU / VRAM НАСТРОЙКИ
# -------------------------------------------------------------------

# Flash Attention (RTX 40+/50+ — вкл, A5000/A4000 — можно выкл):
OLLAMA_FLASH_ATTENTION=1

# Квантизация KV-кэша: f16 (полная), q8_0, q4_0 (меньше VRAM):
OLLAMA_KV_CACHE_TYPE=q4_1

# Ручной лимит VRAM в байтах (по умолчанию автодетект):
# OLLAMA_MAX_VRAM=25769803776

# -------------------------------------------------------------------
# ДЕБАГ И ДИАГНОСТИКА
# -------------------------------------------------------------------

# Уровень дебага: 0=выкл, 1=info (по умолчанию)
# OLLAMA_DEBUG=1

# Логировать тела запросов inference в /tmp:
# OLLAMA_DEBUG_LOG_REQUESTS=1

# Таймаут загрузки модели перед отказом:
# OLLAMA_LOAD_TIMEOUT=5m

# -------------------------------------------------------------------
# TUI / АГЕНТ (lama)
# -------------------------------------------------------------------

# Дефолтная модель для быстрого запуска TUI 'lama':
LAMA_MODEL=lama

# Отключить web_search/web_fetch у агента (поиск через скилл):
OLLAMA_AGENT_DISABLE_WEBSEARCH=1

# Отключить shell-инструменты (bash/powershell) у агента:
# OLLAMA_AGENT_DISABLE_SHELL=1

# -------------------------------------------------------------------
# ПРОВАЙДЕРЫ (external API, providers.json)
# -------------------------------------------------------------------

# Явный путь к файлу провайдеров (иначе ~/.ollama/providers.json):
# OLLAMA_PROVIDERS_CONFIG=/home/voron/.ollama/providers.json

# Разрешённые remote-хосты для pull'а моделей:
# OLLAMA_REMOTES=ollama.com,huggingface.co

# -------------------------------------------------------------------
# СЕТЕВОЕ / ПРОКСИ
# -------------------------------------------------------------------

# HTTP прокси (если за корпоративным firewall):
# HTTP_PROXY=http://proxy.local:3128
# HTTPS_PROXY=http://proxy.local:3128
# NO_PROXY=127.0.0.1,localhost

# CORS origins для web UI:
# OLLAMA_ORIGINS=http://localhost:3000,http://192.168.0.*

# -------------------------------------------------------------------
# РАЗНОЕ
# -------------------------------------------------------------------

# Контекст (если модель не specifies): 4096 / 32768 / 262144
# OLLAMA_CONTEXT_LENGTH=32768

# Запретить облачные фичи (remote inference, web search):
# OLLAMA_NO_CLOUD=1

# Не удалять старые blob'ы при запуске:
# OLLAMA_NOPRUNE=1

# Максимальная очередь запросов:
# OLLAMA_MAX_QUEUE=8

# Параллельные потоки для safetensors pull:
# OLLAMA_MAX_TRANSFER_STREAMS=4
`

// EnsureEnvFile creates /etc/ollama/env with full documentation if it doesn't
// exist yet. Returns true if the file was created, false if it already existed
// or creation failed (non-fatal).
func EnsureEnvFile() bool {
	return ensureSystemEnvFile(false)
}

// RecreateEnvFile overwrites /etc/ollama/env with the full documented template.
// Used from the TUI "recreate environment variables" menu item.
// Returns an error if writing fails.
func RecreateEnvFile() (string, error) {
	return doWriteEnvFile("/etc", true)
}

// RecreateUserEnvFile overwrites ~/.ollama/env with the full documented template.
// Used from the TUI service section to recreate user-level env without sudo.
func RecreateUserEnvFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home dir: %w", err)
	}
	return doWriteEnvFile(home, true)
}

func ensureSystemEnvFile(_ bool) bool {
	if _, err := doWriteEnvFile("/etc", false); err != nil {
		fmt.Fprintf(os.Stderr, "ollama: warning: %v\n", err)
		return false
	}
	return true
}

func doWriteEnvFile(baseDir string, force bool) (string, error) {
	path := filepath.Join(baseDir, "ollama", "env")

	// Not forcing and file exists — don't touch user's config.
	if !force {
		if _, err := os.Stat(path); err == nil {
			return path, nil // already exists, that's fine
		}
	} else if _, err := os.Stat(path); err == nil && force {
		// Force: we'll overwrite below.
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("cannot create %sollama/: %w", baseDir, err)
	}

	if err := os.WriteFile(path, []byte(envFileTemplate), 0644); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", path, err)
	}

	if !force {
		fmt.Fprintf(os.Stderr, "ollama: created default config at %s (edit to customize)\n", path)
	}
	return path, nil
}
