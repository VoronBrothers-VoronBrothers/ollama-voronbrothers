# План: внешний API провайдера как виртуальная модель в TUI

Цель: позволить использовать OpenAI-совместимый endpoint + API-ключ купленного
провайдера (Groq/OpenAI/Mistral/Together и т.п.) прямо в ollama TUI —
выбор из `/model`, стриминг в чате, счётчики токенов — без `ollama run`
и без загрузки весов локально.

## Текущее состояние (разведка 2026-09-19)
- Чужих API-ключей нигде нет: ни `/api`, ни env для внешних провайдеров.
- Единственный remote-прокси — Ollama Cloud (`name:cloud` → `server/cloud_proxy.go`,
  подпись SSH-ключом `~/.ollama/id_ed25519`, база `OLLAMA_CLOUD_BASE_URL`).
- Его паттерн (middleware на пути `/api/chat`, стриминг jsonl) — образец для повторения.

## Этап 0 — Разведка (30 мин)
- [ ] Прочитать `server/cloud_proxy.go` целиком: точки входа middleware,
      как проксируется body и SSE→jsonl ответ, где висит в `server/routes.go`.
- [ ] Посмотреть, как TUI-пикер резолвит имя модели (`cmd/tui/chat/*`,
      internal/modelref) — где можно подцепить «внешняя модель».
- [ ] Проверить формат стрима ollama `/api/chat` (jsonl) vs SSE `chat/completions`.

## Этап 1 — Конфигурация (MVP: env, без файлов)
- [ ] `envconfig/config.go`: новые переменные
      - `OLLAMA_EXTERNAL_BASE_URL` (OpenAI-совместимый корень, напр. https://api.groq.com/openai/v1)
      - `OLLAMA_EXTERNAL_API_KEY`
      - (опц.) `OLLAMA_EXTERNAL_MODELS="name-a,name-b"` — имена виртуальных моделей;
        без списка — любое непарсится-локально имя шлём на внешний endpoint.
- [ ] Идентификация «внешней модели»: суффикс в имени, напр. `ext:<имя>`
      (или отдельный источник в `internal/modelref`: ModelSourceExternal).

## Этап 2 — Серверный прокси (основной)
- [ ] Новый `server/external_proxy.go` по образцу cloud_proxy:
      - middleware перехватывает `/api/chat`, где model = внешняя;
      - body конвертируется в OpenAI `chat/completions` (stream:true);
      - заголовок `Authorization: Bearer <key>`; без подписи.
- [ ] Ответ провайдера (SSE) → стрим jsonl ollama `/api/chat`:
      mapping delta.content → message, done → stop_reason ("stop"/"length").
- [ ] Путь для pull-like: внешние имена НЕ качаем; `ollama list` их не показывает
      (или показываем отдельной пометкой «external»).

## Этап 3 — TUI
- [ ] `/model` пикер: если включён external-конфиг — в списке появляются виртуальные модели.
- [ ] Ошибки: 401/429 → человекочитаемое сообщение с указанием, чей ключ не сработал.

## Этап 4 — Приёмка
- [ ] `ext:gpt-oss` (или выбранная) в TUI: стриминг идёт, `/token` считает токены,
      stop_reason корректен.
- [ ] Локальные модели не затрагиваются (regression-проверка /api/chat локально).
- [ ] Без ключа/endpoint — поведение как сейчас (ничего не ломается).

## Этап 5 — Универсальный системный промпт для внешних моделей (сделано 2026-10-26)

Промпт применяется **только** к моделям вида `provider/vendor/model`; локальные
модели не затрагиваются ни на одном пути (см. `server/external_proxy.go`,
ветка `tryParseExternalModel`).

Файл (первый найденный побеждает):
1. `$OLLAMA_EXTERNAL_SYSTEM_PROMPT` — если задан, другие пути не смотрятся;
2. `external_system_prompt.md` в каталоге конфигурации провайдеров;
3. `system_prompt.md` там же (алиас).

Каталог конфигурации (`externalPromptConfigDir`, `server/external_prompt.go:61`):
`dirname($OLLAMA_PROVIDERS_CONFIG)` → `$OLLAMA_HOME` → `~/.ollama`.
Т.е. промпт лежит рядом с `providers.json`: `/home/voron/.ollama/external_system_prompt.md`.

Две точки инъекции:
- `buildOpenAIRequest` — промпт вставляется первым сообщением `role: system`
  (`prependExternalSystemPrompt`, `server/external_prompt.go:152`); если клиент уже
  прислал system с этим текстом — не дублируется, если прислал свой system —
  склеивается как `prompt + "\n\n" + свой`;
- `ShowHandler` — внешняя модель отдаёт промпт в `ShowResponse.System`, поэтому TUI
  включает его в запрос и показывает в `/system`.

Кэш: `mtime` + размер файла, правки подхватываются без рестарта демона.
Тесты: `server/external_prompt_test.go`, `cmd/tui/chat/input_test.go`
(`TestChatSystemCommandControlsExternalSystemPrompt`).

- [x] Промпт читается из файла рядом с `providers.json`
- [x] Инъекция в исходящий OpenAI-запрос, без дублей
- [x] `ollama show <external>` печатает блок `System`
- [x] `/system off` в TUI убирает промпт и для внешних моделей
- [ ] Живая проверка `/system off` в TUI на реальном провайдере (проверено кодом + тестом)

## Риски / открытые вопросы
- Конфликт суффиксов с `:cloud`/`:local` в modelref — выбрать синтаксис.
- Разные форматы стрима провайдеров (anthropic-style, responses API) — MVP только OpenAI SSE.
- Логирование ключа: не писать key в логи (`OLLAMA_DEBUG_LOG_REQUESTS` — маскировать).
- Промпт общий для всех внешних провайдеров; per-provider override пока не сделан
  (при необходимости — `external_system_prompt.<provider>.md`).
