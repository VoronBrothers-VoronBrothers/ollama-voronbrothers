# VORON-RUNBOOK — сборка/пересборка ollama + atomарная замена бинарников

## 1. Что должно быть в системе (чек-лист)

| Путь | Назначение |
|---|---|
| `/usr/local/bin/ollama` | Основной бинарник: сервер (`serve`) + CLI/TUI (chat, `/add`, RAG и т.д.) |
| `/usr/local/bin/lama` | Быстрый запуск TUI: `lama [MODEL] [PROMPT...]`. Модель берётся из env `LAMA_MODEL`, иначе первый аргумент, fallback `ollama-model`. Entry point — `./lama/` в репо. |
| `/usr/local/bin/*.bak` | Бэкапы старых бинарников: `ollama-<версия>.bak`, при повторе — `-2.bak`, `-3.bak` (откат) |
| `/etc/systemd/system/ollama.service` | Сервер (User=ollama, Restart=always); env — через `EnvironmentFile=` + LD_LIBRARY_PATH |
| `/etc/ollama/env` | **Единая конфигурация** всех переменных (serve и TUI), см. §6 |
| `/usr/local/lib/ollama/{cuda_v13,cuda_v12,vulkan}` | Библиотеки llama.cpp для 5060 Ti (sm_120 — только в cuda_v13, она ПЕРВАЯ в LD_LIBRARY_PATH) |

**Правило №1: `ollama` и `lama` — всегда пара.** Собираются из одного коммита с одной строкой версии.
Если пересобрал один без второго — второй устарел (прецедент 08.11.2026: PDF в `/add` работал
в `ollama`, но не в `lama`).

## 2. Версионирование (ldflags)

Формат: `<upstream-версия>-voron-<DDMMYYYY>`. Пример: `0.40.12-voron-08112026`.

Правила определения версии:
1. `ollama --version` → текущий номер (например `0.40.11-voron-09102026`).
2. **Версия не должна уменьшаться.** Новое число ≥ предыдущего.
3. Была работа с upstream (merge/pull) → брать версию из upstream.
4. Просто патчим свой код без изменения upstream-базы → то же базовое число, меняем только дату в суффиксе.
5. Суффикс: `-voron-` + текущая дата `DDMMYYYY`.

Это позволяет отличить свой билд от чужого и видеть дату сборки с базовым upstream.

## 3. Пересборка (проверенный цикл)

**Автомат:** `scripts/voron-build.sh [новая-база]` делает всё ниже сам — версию,
сборку обоих бинарников, бэкапы и атомарную замену. Ручной вариант для отладки:

```bash
# Шаг 1: узнать текущую версию (оба бинарника!)
ollama --version && lama --version   # → например "0.40.12-voron-07112026"

cd /home/voron/Документы/VB-ollama/GitHub\ Ollama\ кастомный\ бинарник/ollama\ fork\ our\ fork
V=0.40.12-voron-$(date +%d%m%Y)   # базовое число по §2, НЕ ниже текущего

# Шаг 2: собрать ОБА бинарника (tags sqlite_fts5 обязателен — RAG-индекс internal/knowledge)
go build -tags sqlite_fts5 -ldflags "-X github.com/ollama/ollama/version.Version=$V" -o /tmp/ollama-patched .
go build -tags sqlite_fts5 -ldflags "-X github.com/ollama/ollama/version.Version=$V" -o /tmp/lama-patched ./lama/

# Шаг 3: бэкап + установка (mv, не cp!) — ОБА файла
sudo cp /usr/local/bin/ollama "/usr/local/bin/ollama-$(olla-old-version).bak" && sudo mv /tmp/ollama-patched /usr/local/bin/ollama && sudo chmod 755 /usr/local/bin/ollama
sudo cp /usr/local/bin/lama   "/usr/local/bin/lama-$(lama-old-version).bak"   && sudo mv /tmp/lama-patched   /usr/local/bin/lama   && sudo chmod 755 /usr/local/bin/lama
```

Важно:
- **`mv`, а не `cp`** — на занятом бинарнике cp даёт «Текстовый файл занят». `mv` атомарно меняет inode; текущие процессы (сервер, TUI) продолжают работать со старым inode без перезапуска.
- **ldflags обязателен** — без него клиент ругается «client version is 0.0.0».
- **Бэкап перед заменой каждого файла**, имя = полная версия, **вшитая в сам файл**
  (`strings binary | grep -oE '[0-9]+\.[0-9]+\.[0-9]+-voron-[0-9]{8}'`), а не взятая из имени.
  Если бэкап этой версии уже есть — добавляется номер: `ollama-<версия>-2.bak`, `-3.bak`, …
  (старый вариант `-dup-HHMM` упразднён: имена раздувались до нечитаемых).
- **Починить имена старых бэкапов**: `scripts/voron-build.sh --fix-backup-names`
  (мигрирует `-dup-HHMM`-имена в нумерацию, версию берёт из самого файла).
- **CGO: нужен** (go-sqlite3, x/mlxrunner — mlx-c из хедеров репо). `CGO_ENABLED=0` упадёт на `x/mlxrunner`. Go: apt `golang-go` (1.26).
- Сервер (`ollama serve`) и TUI перезапускать после замены **не обязательно**: следующий старт подхватит новый бинарник. Для TUI — обычный self-restart / новое окно konsole.

## 4. Проверка после замены

```bash
ollama --version   # сервер ответит СТАРОЙ версией (работает со старым inode) — это нормально;
                   # предупреждение "client version is <новая>" подтверждает, что файл на месте
lama --version     # lama не поднят процессом → сразу покажет новую версию
ls -la /usr/local/bin/ollama* /usr/local/bin/lama*   # бэкапы + размеры/время новых файлов
```

Если менялась фича TUI — проверить в живом окне (self-restart TUI): например, `/add file.pdf` →
текст через pdftotext (нужен `poppler-utils`, ставится: `sudo apt install poppler-utils`).

## 5. Откат

```bash
# любой из бинарников обратно на записанный бэкап:
# <версия> — та, что вшита в бэкап; номер суффикса (-2/-3) смотри в ls:
ls -1t /usr/local/bin/ollama-*.bak /usr/local/bin/lama-*.bak
sudo cp /usr/local/bin/ollama-<версия>.bak /usr/local/bin/ollama && sudo chmod 755 /usr/local/bin/ollama
sudo cp /usr/local/bin/lama-<версия>.bak   /usr/local/bin/lama   && sudo chmod 755 /usr/local/bin/lama
```

Живой сервер/ TUI откат не требуют — они держат старый inode; новый (откаты) подхватится при следующем старте.

## 6. Конфигурация: единый файл `/etc/ollama/env`

**Одна точка правды для ВСЕХ переменных** (serve + TUI/lama): `/etc/ollama/env`.
Формат `KEY=VALUE`, комментарии строкой с `#`. Приоритет: process-env >
`~/.ollama/env` (user override) > `/etc/ollama/env`.

Как читается:
- **serve** — через `EnvironmentFile=/etc/ollama/env` в юните;
- **TUI/lama и всё вне systemd** — Go-fallback: `envconfig.Var()` при пустом
  process-env смотрит `/etc/ollama/env` + `~/.ollama/env` (кэш на процесс,
  тесты: `envconfig/config_file_test.go`).

**Исключение:** `LD_LIBRARY_PATH` остаётся в юните (`Environment=`) — его читает
динамический линковщик ДО запуска Go кода, в файл переносить нельзя.
В юните также только `PATH`. Все `.d/*.conf` drop-in'ы удалены 10.11.2026
(бэкап: `env-migration-backup/dropins-20261110.tar`).

Содержимое (по состоянию на 10.11.2026):
| переменная | значение | назначение |
|---|---|---|
| OLLAMA_KEEP_ALIVE | 20m | выгрузка модели из VRAM |
| OLLAMA_KV_CACHE_TYPE / OLLAMA_FLASH_ATTENTION | q4_1 / 1 | KV cache + FA (5060 Ti) |
| OLLAMA_HOST | 0.0.0.0:11434 | слушать все интерфейсы |
| OLLAMA_MAX_LOADED_MODELS / OLLAMA_NUM_PARALLEL | 2 / 1 | лимиты VRAM и параллелизм |
| OLLAMA_PROVIDERS_CONFIG | /home/voron/.ollama/providers.json | внешние провайдеры (нужны ACL на ~/.ollama для юзера ollama) |
| LAMA_MODEL | ollama-model | дефолтная модель TUI `lama` |
| OLLAMA_AGENT_DISABLE_WEBSEARCH | 1 | web-инструменты агента выкл — поиск через скилл webq |

После правки `/etc/ollama/env`: `sudo systemctl daemon-reload && sudo systemctl restart ollama`
(TUI подхватит при следующем старте, self-restart). Для разового override без правки файла:
`LAMA_MODEL=xyz lama`, `OLLAMA_HOST=127.0.0.1:11435 ollama serve` и т.п.

**Не делать:** export OLLAMA_*/LAMA_* в `.zshrc`/`.bashrc` — это второй источник правды
(прецедент 10.11: LAMA_MODEL жила в zshrc, а drop-in'ов TUI не видел).

## 7. Краткая история (чтобы не гадать)

- `0.40.12-voron-06112026` — база перед пересборками 07–08.11
- `0.40.12-voron-07112026` — добавлен бинарник `lama` (см. память `lama-binary-added-2026-11-07.md`)
- `0.40.12-voron-08112026` — `/add` принимает PDF (pdftotext), см. память `2026-11-08_add-pdf-pdftotext.md`. **Первый раз, когда lama собирали отдельно и упустили — с этого дня правило «всегда оба»**
- `0.40.12-voron-10112026` — единый файл конфигурации `/etc/ollama/env`: envconfig-fallback для TUI/lama, все drop-in'ы удалены (см. §6), web-инструменты агента отключаются из файла, а не из systemd. См. план `vb-ollama-long-memory/plans/2026-11-10-single-env-file.md`
