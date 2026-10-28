# Сборка/пересборка оллама/атомарная замена бинарника/инструменты/переменные окружения
# инструкция 

> Репозиторий: `/home/voron/Документы/VB-ollama/ollama` (upstream github.com/ollama/ollama, ветка на fb307609 + свои правки).

## 1. Где что лежит

| Компонент | Путь |
|---|---|
| bash | `agent/tools/bash.go` (+ `bash_unix.go`, `bash_windows.go`) |
| read и edit | `agent/tools/file.go` (+ тесты `file_test.go`) |
| skill | `agent/tools/skill.go` |
| web_search / web_fetch | `agent/tools/web.go` (отключены флагом, см. ниже) |
| Регистрация инструментов | `cmd/agent_tui.go` → функция `agentToolsRegistry()` (~строка 253) |
| Петля выполнения инструментов | `agent/session.go` — исполняет LОКАЛЬНО процесс TUI, не сервер |
| CLI-вход | `main.go` → `cmd.NewCLI()` |

### Флаги окружения (регистрация в `agentToolsRegistry`)
- `OLLAMA_AGENT_DISABLE_SHELL` — задать = убрать bash.
- `OLLAMA_AGENT_DISABLE_WEBSEARCH` — задан у нас (`~/.zshrc:125` и префикс в `Скрипты_ИИ/ollama_console.sh`) → web-инструменты не регистрируются. Решение за 23.08, см. `vb-ollama-long-memory/2026-08-24_0430_web-tools-off.md`.
- Skill регистрируется, только если каталог скиллов непуст.

## 2. Патч от 24.08: edit принимает абсолютные пути

`agent/tools/file.go`, бэкап оригинала `/tmp/file.go.bak` (пока /tmp жив), полный откат = `git checkout -- agent/tools/` в репо.

Изменения:
1. Edit.Execute: `openRegularFile(workingDir, path, false)` → `true` (read и так принимал abs).
2. `rejectFinalSymlink` — ветка для абсолютных путей через `os.Lstat` до вызова `cleanRelativePath`.
3. `writeFileAtomic` — развилка: abs → новый `writeFileAtomicAbs(path, data, perm)` (атомарно: OpenFile+Rename, без os.Root); rel — прежний путь.
4. Текст описания параметра `path` в схеме инструмента дополнен про абсолютные пути.
5. Тесты в `file_test.go`: `TestEditAcceptsAbsolutePath`, `TestEditAcceptsAbsoluteSymlinkTarget`, `TestEditAbsolutePathOutsideWorkingDir`.

Проверено: `go vet`, `go test ./agent/tools/ -count=1` → PASS; smoke: edit по abs-пути вне cwd работает.

## 3. Как пересобрать и поставить бинарник (проверенный цикл)

> **Имя версии в ldflags:** `<upstream-версия>-voron-<DDMMYYYY>`.
> Пример: `0.40.2-voron-10102026`.
>
> Правило определения версии:
> 1. Выполнить `ollama --version` → получить текущий номер (например `0.40.2-voron-09102026`).
> 2. **Версия не должна уменьшаться.** Новое число должно быть ≥ того что стояло до пересборки.
> 3. Если работа с upstream (merge, pull) → брать версию из upstream (например `0.40.2`).
> 4. Если просто патчим свой код (без изменения upstream-базы) → держать то же число что было, менять только дату в суффиксе.
> 5. Суффикс: `-voron-` + текущая дата `DDMMYYYY`.
>
> Это позволяет отличить свой билд от чужого, видеть дату сборки и базовый upstream.

```bash
# Шаг 1: узнать текущую версию
ollama --version   # → например "0.40.2-voron-09102026"
# Базовое число (до -voron) = версия upstream, НЕ ниже этого ставить!

# Шаг 2: собрать (версия = та же или выше + свежая дата)
cd /home/voron/Документы/VB-ollama/GitHub Ollama кастомный бинарник/ollama fork our fork
go build -tags sqlite_fts5 -ldflags "-X github.com/ollama/ollama/version.Version=0.40.2-voron-$(date +%d%m%Y)" \
  -o /tmp/ollama-patched .
# -tags sqlite_fts5: нужен для RAG-индекса internal/knowledge (SQLite FTS5).

# Шаг 3: бэкап + установка (mv, не cp!)
sudo cp /usr/local/bin/ollama /usr/local/bin/ollama-<старая-версия>.bak
sudo mv /tmp/ollama-patched /usr/local/bin/ollama && sudo chmod 755 /usr/local/bin/ollama
```

Важно:
- **`mv`, а не `cp`** — на занятом бинарнике cp даёт «Текстовый файл занят». `mv` атомарно меняет inode; текущие процессы (сервер, TUI) продолжают работать со старым inode без перезапуска.
- **ldflags обязателен** — без него клиент ругается «client version is 0.0.0».
- **Бэкап перед заменой** — `sudo cp` старого бинарника с именем версии, чтобы можно было откатиться.
- CGO: нужен (go-sqlite3, x/mlxrunner — mlx-c из хедеров репо, отдельная .so не нужна). `CGO_ENABLED=0` упадёт на `x/mlxrunner`. Go: apt `golang-go` (1.26).
- Сервер (`ollama serve`) и TUI перезапускать не обязательно после замены; следующий старт подхватит новый бинарник. Для TUI — обычный self-restart / новое окно konsole.

## 4. Как добавить НОВОЕ инструмент

1. Новый файл `agent/tools/<tool>.go`: struct, реализующий интерфейс инструмента:
   ```go
   type MyTool struct{}
   func (t *MyTool) Name() string { return "my_tool" }
   func (t *MyTool) Description() string { return "..." }
   func (t *MyTool) Schema() api.ToolFunction {
       props := api.NewToolPropertiesMap()
       props.Set("arg", api.ToolProperty{Type: api.PropertyType{"string"}, Description: "..."})
       ... // как в bash.go / file.go
   }
   func (t *MyTool) Execute(ctx context.Context, tc agent.ToolContext, args map[string]any) (*agent.ToolResult, error) { ... }
   ```
2. Регистрация — `cmd/agent_tui.go` в `agentToolsRegistry()`:
   `registry.Register(&agenttools.MyTool{})` (можно под условием `os.Getenv("OLLAMA_AGENT_DISABLE_X") == ""`).
3. Тесты рядом (`_test.go`), цикл: `go test ./agent/tools/ -count=1` → build из §3 → поставить.

## 5. Быстрая проверка состояния
```bash
cd /home/voron/Документы/VB-ollama/ollama && git status --short   # свои правки
go test ./agent/tools/ -count=1                                     # тесты
ollama list                                                          # сервер живой
```


### Пересборка:
cd "/home/voron/Документы/VB-ollama/GitHub ollama/ollama 0.33.0-rc3"
go build -ldflags "-X github.com/ollama/ollama/version.Version=0.33.0-rc3-voron" -o /tmp/ollama-patched .
sudo mv /tmp/ollama-patched /usr/local/bin/ollama   # mv, не cp!

### Оллала TUI и /usr/local/lib/ollama (llama.cpp):
- ollama tui пользуется только `/usr/local/lib/ollama/` из llama cpp — это готовые
  шары C++-движка (`libggml-base.so`, `libggml-cpu-*.so`, `cuda_v12/`, `ggml-rpc-server`).
  Собирать их из источников этого репозитория (FetchContent/CMake) НЕ нужно.
- Можно тупо скопировать для себя готовые релизы и перенести их в `/usr/local/lib/ollama/`:
  https://github.com/ggml-org/llama.cpp/releases
  (архив с нужной версией, из него — `libggml-*.so`, cuda-библиотеки, `ggml-rpc-server`).
- После копирования бинарник ollama подхватит новые .so при следующем старте процесса.

### Провайдеры (external API) и путь к providers.json:
- Порядок поиска в `server/providers.go` → `providerConfigPaths()`:
  1. `$OLLAMA_PROVIDERS_CONFIG` (явный путь)
  2. `$OLLAMA_HOME/providers.json` (иначе `~/.ollama/providers.json`)
  3. `/etc/ollama/providers.json` (фолбэк)
- Сервис работает под `User=ollama` (uid 997, home `/usr/share/ollama`), поэтому
  путь из `/home/voron` требует ACL (drop-in уже стоит):
  `/etc/systemd/system/ollama.service.d/providers.conf` →
  `Environment="OLLAMA_PROVIDERS_CONFIG=/home/voron/.ollama/providers.json"`
- Формат записи провайдера: `name`, `base_url`, `api_key`, `models`, опционально
  `context_window` / `context_windows` (см. ниже).
```bash
sudo setfacl -m u:ollama:x /home/voron          # проход в каталог
sudo setfacl -m u:ollama:rx /home/voron/.ollama  # листинг + чтение
sudo setfacl -m u:ollama:r /home/voron/.ollama/providers.json
sudo systemctl daemon-reload && sudo systemctl restart ollama
sudo journalctl -u ollama -n 50 | grep -i providers
# ожидаем: providers: config loaded path=/home/voron/.ollama/providers.json
#          providers: loaded external provider name=proxyapi models=2
```
- `~/.ollama/providers.json` — обычный файл (не симлинк), права `640`/`600` + ACL для `ollama`;
  `/etc/ollama/providers.json` остаётся независимой копией-фолбэком.
- Проверка моделей: `curl -s http://127.0.0.1:11434/api/tags | grep proxyapi`.

### Системный промпт для внешних моделей:
- Файл: `/home/voron/.ollama/external_system_prompt.md` (алиас `system_prompt.md` там же);
  явный путь — `$OLLAMA_EXTERNAL_SYSTEM_PROMPT` (если задан, другие не смотрятся).
- Каталог берётся из `dirname($OLLAMA_PROVIDERS_CONFIG)`, т.е. промпт ищется рядом
  с `providers.json` — отдельный drop-in не нужен (`server/external_prompt.go`).
- Применяется только к моделям `provider/vendor/model`; локальные не затрагиваются.
- Правки подхватываются без рестарта (кэш по mtime + размеру).
- Проверка: `ollama show proxyapi/qwen/qwen3.8-27b` → блок `System`.
- `/system off` в TUI отключает промпт и для внешних моделей.

### Окно контекста внешних моделей (`context_window`):
- Внешний API не отдаёт размер окна, поэтому TUI падал на фолбэк 32768 и сжимал
  сессию слишком рано. В `providers.json` добавлены необязательные поля
  (`server/providers.go`):
  - `context_window` — одно значение на провайдера;
  - `context_windows` — `{"vendor/model": N}` per-model (важнее общего).
- Значение отдаётся в `/api/show` внешней модели (`Details.context_length`);
  отрицательные и нулевые значения отбрасываются на загрузке конфига.
- Проверка: `ollama show proxyapi/qwen/qwen3.8-27b | grep -i context`
  и запись в журнале `providers: loaded external provider ... context_window=...`.

### Автосжатие контекста (`/zip`):
- `/zip` (алиас `/зип`) переключает автосжатие в текущей сессии TUI;
  `/zip on|off` (`вкл|выкл`) — задать явно. По умолчанию включено.
- Выключено → остаётся только ручной `/compact` (`agent.RunOptions.DisableAutoCompaction`).
- Флаг сессионный, в файлы не пишется; после перезапуска TUI снова включён.

### Примечания:
- Shift+Delete в bubbletea v1.3.10 неотличим от Delete (нет парсинга CSI modifier) — поэтому очистка через /очисточередь.
- Кириллицу в бинарнике проверять grep, не strings.

## Переменные окружения Ollama / TUI (полный список)
Источник: `envconfig/config.go` → таблица `AsMap()` + grep по репо. Дата сверки: 2026-10-27.
TUI отдельного набора не имеет — использует те же переменные, что сервер; свои добавления ниже в конце списка.

### Сеть и подключение
| Переменная | Описание | По умолчанию |
|---|---|---|
| `OLLAMA_HOST` | Адрес:порт сервера (и куда коннектится TUI/CLI) | 127.0.0.1:11434 |
| `OLLAMA_ORIGINS` | Разрешённые CORS-origins, через запятую | localhost и т.п. |
| `OLLAMA_REMOTES` | Разрешённые хосты для remote-моделей | ollama.com |

### Модели и память
| Переменная | Описание | По умолчанию |
|---|---|---|
| `OLLAMA_MODELS` | Каталог моделей | ~/.ollama/models |
| `OLLAMA_KEEP_ALIVE` | Как долго модель держится в памяти (0 = сразу, -1 = всегда) | 5m |
| `OLLAMA_LOAD_TIMEOUT` | Тайм-аут загрузки модели | 5m |
| `OLLAMA_MAX_LOADED_MODELS` | Макс. моделей в памяти на GPU (0 = без лимита) | 0 |
| `OLLAMA_CONTEXT_LENGTH` | Длина контекста, если не задано иначе | 4k/32k/256k по VRAM |

### Очередь и параллелизм
| Переменная | Описание | По умолчанию |
|---|---|---|
| `OLLAMA_NUM_PARALLEL` | Параллельные запросы | 1 |
| `OLLAMA_MAX_QUEUE` | Размер очереди запросов | 512 |
| `OLLAMA_MAX_TRANSFER_STREAMS` | Параллельные потоки скачивания safetensors | 4 |

### GPU и кэш
| Переменная | Описание | По умолчанию |
|---|---|---|
| `OLLAMA_FLASH_ATTENTION` | Включить flash attention (нужно для KV-квантования) | off |
| `OLLAMA_KV_CACHE_TYPE` | Квантование KV-кэша: f16 / q8_0 / q4_0 | f16 |
| `OLLAMA_GPU_OVERHEAD` | Резерв VRAM на GPU, в байтах | 0 |
| `OLLAMA_SCHED_SPREAD` | Размазать слои по всем GPU | false |
| `OLLAMA_IGPU_ENABLE` | Включить встроенные GPU | — |
| `OLLAMA_VULKAN` | Включить Vulkan | on (не macOS) |
| `CUDA_VISIBLE_DEVICES` | Какие NVIDIA-GPU видеть | все |
| `HIP_VISIBLE_DEVICES` / `ROCR_VISIBLE_DEVICES` / `GPU_DEVICE_ORDINAL` | Выбор AMD-GPU по ID/UUID | все |
| `HSA_OVERRIDE_GFX_VERSION` | Переопределить gfx для AMD GPU | — |
| `GGML_VK_VISIBLE_DEVICES` | Какие Vulkan-устройства видеть (по ID) | все |

### Рантайм и библиотеки
| Переменная | Описание | По умолчанию |
|---|---|---|
| `OLLAMA_LLM_LIBRARY` | Принудительная LLM-библиотека (обходит автоопределение) | авто |
| `LLAMA_ARG_FIT` | Авто-подгонка памяти llama.cpp | on |
| `LLAMA_ARG_FIT_TARGET` | Целевой свободный VRAM для fit, MiB | — |
| `OLLAMA_GO_TEMPLATE` | Рендер через TEMPLATE из Modelfile | on |

### ТUI / CLI поведение
| Переменная | Описание | По умолчанию |
|---|---|---|
| `OLLAMA_EDITOR` | Редактор для Ctrl+G в TUI | $EDITOR/$VISUAL/xdg-open |
| `OLLAMA_NOHISTORY` | Не сохранять readline-историю | off |
| `OLLAMA_NOPRUNE` | Не чистить blob'и моделей при старте | off |
| `OLLAMA_DEBUG` | Дебаг-логирование (1/true) | off |
| `OLLAMA_DEBUG_LOG_REQUESTS` | Логировать тела запросов + curl-реплей в temp | off |
| `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` (+строчные) | Прокси | — |

### Облако и remote
| Переменная | Описание | По умолчанию |
|---|---|---|
| `OLLAMA_NO_CLOUD` | Выключить облачные фичи (remote-инференс, web search) | off |
| `OLLAMA_CREATE_REMOTE` | Создавать модели через серверный API даже при локальном сервере | off |

### Свои (слой VoronBrothers, вне envconfig)
| Переменная | Описание |
|---|---|
| `OLLAMA_ERRDOT_SESSION` | TUI: передать сессию в errdot-отладчик |
| `OLLAMA_AGENT_DISABLE_SHELL` | Agent-TUI: отключить shell-инструмент (задан = off) |
| `OLLAMA_AGENT_DISABLE_WEBSEARCH` | Agent-TUI: отключить web-search инструмент (задан = off) |

### Заметки
- Большинство читается сервером при старте → после изменения нужен рестарт `ollama serve`.
- Тестовые переменные (`OLLAMA_TEST_*`, `OLLAMA_LAUNCH_OMP_*`) — только для CI, игнорировать.
- Проверка актуального списка из бинарника: таблица живёт в `envconfig/config.go` → `AsMap()`.
