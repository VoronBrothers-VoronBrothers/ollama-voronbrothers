## Custom Build (VoronBrothers)

<p align="center">
  <img src="docs/ollama-logo.svg" alt="ollama llama logo" width="120"/>
</p>

Форк Ollama с кастомными функциями для tmux-оркестрации. База — upstream `v0.34.0 ` где произошло разделение философии оллама.
Мы подхватываем новые фичи из новых версий оллама, держим руку на пульсе.
Наша цель - многочасовая эффективная работа клиентов с полным доступом к системе, чтобы агент чувствовал себя в системе как у себя дома (все риски на пользователе или ограничивайте доступ через shift + tab).

> **Сборка и установка.** Форк собирается из исходников, бинарники с ollama.com его не включают.
> Сборка всегда обоих бинарников (`ollama` + `lama`) — скриптом **[`scripts/voron-build.sh`](scripts/voron-build.sh)**:
> сам определяет версию `<upstream>-voron-<DDMMYYYY>`, ставит `-tags sqlite_fts5` и ldflags,
> делает бэкапы и атомарную подмену (`sudo mv`, не `cp`).
> Правила версионирования, systemd drop-in'ы и ручной вариант — в [VORON-RUNBOOK.md](VORON-RUNBOOK.md).

### Поведение по умолчанию

- **Низкий effort размышлений** — если уровень thinking не задан явно, TUI использует `medium` (при medium пустой промт по стандарту llama.cpp ничего не придумали). Уровни: auto / on / off / low / medium / high / max (`/think`).
- **Полный доступ к инструментам по умолчанию** — модель исполняет инструменты без подтверждения («full access»); переключение на режим проверки — `Shift+Tab`.
- **Очередь промптов** — можно слать несколько сообщений подряд во время генерации; они выстраиваются в очередь (`queued (N)`) и обрабатываются по одному.

### Команды CLI: `run` устарел → `chat`

- **`ollama run …`** — депрекация: печатает «вызов устарел» и не стартует чат (сервер не нужен).
- **`ollama chat MODEL [PROMPT]`** — полноценный интерактивный чат на оригинальной логике v0.34 (без кастомного буфера фоновых сообщений). Те же флаги, что раньше у `run`: `--think`, `--format`, `--insecure`, `--keepalive`, `--runner` и др.
- **One-shot (pipe)**: `echo "question" | ollama chat MODEL` — одноразовый запрос без TUI и памяти. Для proxy-моделей (`proxyapi/...`) автоматически используется `/api/chat` (а не `/api/generate`). Пример: `echo "2+2?" | ollama chat proxyapi/perplexity/sonar`
- В интерактиве `chat` работает `/add <пути...>` — прикрепить текстовые файлы (`.txt`/`.md`/`.py`/`.json`/`.jsonl`/`.yaml`/`.sh`/…) или **PDF с копируемым текстом** (извлечение через `pdftotext`, нужен `poppler-utils`) или папки (только верхний уровень); полное содержимое вставляется в контекст при отправке. Изображения не через `/add` — путь вставь текстом для vision-моделей.

### Быстрый запуск TUI: `lama`

Отдельный бинарник для мгновенного запуска полноценного TUI (bubbletea) без приветственного меню:

```bash
lama              # → TUI с моделью из LAMA_MODEL
lama llama3       # → TUI с моделью "llama3"
```

Модель по умолчанию: env var `LAMA_MODEL` (в systemd-сервисе задан `orchestrator`),
fallback — хардкод `"ollama-model"`. Внутренне вызывает `cmd.LaunchModelTUI`, минуя welcome/menu.

**Собирается и ставится только парой с `ollama`** — тем же `scripts/voron-build.sh`
(свой сборкой lama быстро устаревает; прецедент 08.11: PDF в `/add` был только в одном бинарнике).
Вручную (только для отладки): `go build -tags sqlite_fts5 -ldflags "-X github.com/ollama/ollama/version.Version=..." -o lama ./lama/`

### Инструменты агента по умолчанию

`bash`, `bash_long`, `read`, `edit`, `write`; для мультимодальных моделей — `view_image`.
Опционально: `skill` (если есть каталог скиллов), `web_search` / `web_fetch` и MCP-серверы.
Отключение через переменные окружения, напр. `OLLAMA_AGENT_DISABLE_SHELL`, `OLLAMA_AGENT_DISABLE_WEBSEARCH`.

### Внешние провайдеры (external API)

Форк умеет проксировать модели внешних API-провайдеров через тот же `/api/chat`,
что и локальные. Конфиг — обычный JSON рядом с остальными настройками Ollama:
`~/.ollama/providers.json`.

```json
{
  "providers": [
    {
      "name": "proxyapi",
      "base_url": "https://api.proxyapi.ru/v1",
      "api_key": "sk-...",
      "models": ["qwen/qwen3.8-27b", "deepseek/deepseek-v4.1-flash-1017"],
      "context_window": 131072,
      "context_windows": {"qwen/qwen3.8-27b": 262144}
    }
  ]
}
```

- `name` — первый сегмент имени модели в API (`proxyapi/qwen/qwen3.8-27b`);
  сравнивается без учёта регистра;
- `base_url` — база OpenAI-совместимого API (хвостовой `/` отбрасывается);
- `models` — список `vendor/model`, как их ждёт провайдер; именно эти имена
  видны в `/api/tags` и в пикере `/model` с префиксом `<name>/`;
- `context_window` (необязательно) — окно контекста в токенах по умолчанию для
  всех моделей провайдера;
- `context_windows` (необязательно) — per-model переопределение:
  `{"qwen/qwen3.8-27b": 262144}`; регистр не важен, значения ≤ 0 отбрасываются.
- модели с префиксом провайдера уходят во внешний API, локальные — нет
  (ветка `tryParseExternalModel` в `server/providers.go`).

Зачем `context_window`: внешний API не отдаёт размер окна (в ответе
`/v1/models` только `id`/`owned_by`). Значение из конфига попадает в
`details.context_length` ответа `/api/show`, оттуда — в бюджет компактора.

Для **внешних API-моделей** TUI-агент по умолчанию поднимает окно до ~10 млн
токенов (даже без `context_window`), поэтому история почти не сжимается и
`/zip off` для них, как правило, не нужен. Это влияет только на внешние
провайдеры; локальные и ollama.com cloud модели используют своё реальное окно
(running num_ctx / рекомендация) и уплотняются по-прежнему. Точное (обычно
меньшее) значение для API задаётся `context_window`.

Автосжатие контекста можно выключить на время сессии TUI: `/zip` (алиас `/зип`)
переключает его, `/zip on|off` задаёт явно. По умолчанию — включено. При
выключенном автосжатии компактор остаётся доступен вручную (`/compact`), а
бюджеты обрезки вывода инструментов по размеру окна продолжают работать.
Состояние живёт в рамках запущенного TUI и переживает переключение модели.

Порядок поиска файла (`providerConfigPaths()`, `server/providers.go`):
1. `$OLLAMA_PROVIDERS_CONFIG` — если задан, **только** он (без фолбэков);
2. `$OLLAMA_HOME/providers.json`, иначе `~/.ollama/providers.json`;
3. `/etc/ollama/providers.json` — системный фолбэк.

Демон работает под `User=ollama` с чужим `$HOME`, поэтому путь в `/home/<user>`
требует drop-in с явным путём и ACL на каталог:

```bash
# /etc/systemd/system/ollama.service.d/providers.conf
# [Service]
# Environment="OLLAMA_PROVIDERS_CONFIG=/home/voron/.ollama/providers.json"

sudo setfacl -m u:ollama:x  /home/voron
sudo setfacl -m u:ollama:rx /home/voron/.ollama
sudo setfacl -m u:ollama:r  /home/voron/.ollama/providers.json
sudo systemctl daemon-reload && sudo systemctl restart ollama

sudo journalctl -u ollama -n 50 | grep -i providers
# providers: config loaded path=/home/voron/.ollama/providers.json
# providers: loaded external provider name=proxyapi models=2
curl -s http://127.0.0.1:11434/api/tags | grep proxyapi
```

Файл держим обычным файлом (не симлинком) с правами `600`/`640`: ключи не должны
быть доступны другим пользователям.

### Системный промпт для внешних моделей

Универсальный промпт применяется **только** к моделям внешних провайдеров;
локальные модели не затрагиваются ни на одном пути.

Файл (первый найденный побеждает):
1. `$OLLAMA_EXTERNAL_SYSTEM_PROMPT` — если задан, другие пути не смотрятся;
2. `external_system_prompt.md` в каталоге конфига провайдеров;
3. `system_prompt.md` там же (алиас).

Каталог конфига (`externalPromptConfigDir()`, `server/external_prompt.go`):
`dirname($OLLAMA_PROVIDERS_CONFIG)` → `$OLLAMA_HOME` → `~/.ollama`.
То есть промпт лежит рядом с `providers.json`: `/home/voron/.ollama/external_system_prompt.md`.

```bash
# правки подхватываются без рестарта демона (кэш по mtime + размеру)
# но можно и релоад демона чтобы быстрее подхватились модели апи и промт апи
nano ~/.ollama/external_system_prompt.md
ollama show proxyapi/qwen/qwen3.8-27b   # блок System
```

Куда попадает промпт:
- `buildOpenAIRequest` — первым сообщением `role: system` в исходящем запросе;
  если клиент уже прислал system с этим текстом — не дублируется, если прислал
  свой system — склеивается как `промпт + "\n\n" + свой`;
- `ShowHandler` — внешняя модель отдаёт промпт в `ShowResponse.System`, поэтому
  TUI включает его в запрос и показывает в `/system`;
- `/system off` в TUI отключает его и для внешних моделей.

Промпт общий для всех провайдеров; per-provider override пока нет.

### Слэш-команды

| Команда | Алиасы | Назначение |
|---------|--------|------------|
| `/model` | — | Переключить модель |
| `/new` | `/новый`, `/нов` | Новый чат |
| `/think` | — | Уровень размышлений (auto/on/off/low/medium/high/max) |
| `/tools` | — | Вкл/выкл инструменты |
| `/system [on\|off]` | — | Показать / вкл. / выкл. системный промпт |
| `/skills` | — | Скиллы: список, `import codex\|claude\|pi`, `remove <имя>` (горячая перезагрузка) |
| `/mcp [reload]` | — | MCP-серверы: список / переподключение без рестарта |
| `/compact` | — | Сжать старую часть контекста вручную |
| `/zip [on\|off]` | `/зип` | Автосжатие контекста (по умолчанию вкл.; выкл. = только ручной `/compact`) |
| `/help` | `/?` | Список команд |
| `/bye` | `/exit` | Выход |
| `/prompt` | `/промт`, `/промпт` | Показать полный промпт, инструменты и сообщения |
| `/очисточередь` | `/clear` | Очистить очередь ожидающих промптов |
| `/время <сек>` | — | Автоотправка: интервал бездействия (сек) |
| `/heartbeat "текст"` | `/авторитм` | Периодически слать сообщение ИИ |
| `/heartbeat-time <сек>` | `/авторитм-тайм` | Интервал heartbeat (0 = выкл) |
| `/token` | `/токен` | Показывать причину остановки и токены под ответами (вкл/выкл) |
| `/fullthink` | `/фуллмысль` | Показать / скрыть полные детали размышлений (вкл/выкл) |
| `/save [файл\|путь]` | — | Сохранить запрос в JSON. Без аргумента → `~/.ollama/chat-<дата>.json`; имя → `~/.ollama/<имя>.json`; путь → от рабочей папки |
| `/add <пути...>` | `/прикрепить` | Загрузить текстовые файлы (.txt/.md/.py/.json/…), PDF (с копируемым текстом, через pdftotext) или папки (только верхний уровень) в контекст. Изображения не принимаются — путь вставь текстом для vision. Аналог есть и в CLI `ollama chat`. |

### Spillover (экономия токенов)

Большие выводы инструментов (>~15k символов) автоматически сохраняются в файл
`$TMPDIR/ollama-spill/{hash}.txt` (на Linux: `/tmp/ollama-spill/`), а в контексте остаётся preview + путь.
Модель решает, читать ли полный вывод через file tool.

### Служебное (Service tools)

TUI: пункт меню «Служебное» — раздел сервисных операций.
Подпункты:
- **Recreate env vars (~/.ollama/env)** — пересоздаёт пользовательский файл окружения с задокументированными значениями по умолчанию. Подтверждение y/n перед перезаписью. Не требует sudo (пишет в `$HOME/.ollama/env`).

### База знаний (RAG)

Каталог `~/.ollama/knowledge/` для документов (`.txt`, `.md`, **`.pdf`** — извлечение через `pdftotext`).
Инструмент `knowledge_search`: BM25-поиск по чанкам (~500 слов, overlap 10%), хранится в SQLite FTS5.
TUI: пункт меню «Knowledge base / RAG» → `i` = индексация now, `o` = открыть папку.

Короткие клавиши: `Alt+Enter` — перенос строки; `Shift+Tab` — режим доступа; `Ctrl+O` — детали транскрипта; `↑/↓` — предыдущие промпты.


