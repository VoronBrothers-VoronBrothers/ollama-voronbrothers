# VORON-RUNBOOK — сборка/пересборка ollama + atomарная замена бинарников

## 1. Что должно быть в системе (чек-лист)

| Путь | Назначение |
|---|---|
| `/usr/local/bin/ollama` | Основной бинарник: сервер (`serve`) + CLI/TUI (chat, `/add`, RAG и т.д.) |
| `/usr/local/bin/lama` | Быстрый запуск TUI: `lama [MODEL] [PROMPT...]`. Модель берётся из env `LAMA_MODEL`, иначе первый аргумент, fallback `ollama-model`. Entry point — `./lama/` в репо. |
| `/usr/local/bin/*.bak` | Бэкапы старых бинарников с именем версии (откат) |
| `/etc/systemd/system/ollama.service` + `.d/*.conf` | Сервер + все env-настройки (см. §6) |
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
- **Бэкап перед заменой каждого файла**, имя = полная версия с уехавшего бинарника.
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
sudo cp /usr/local/bin/ollama-<версия>.bak /usr/local/bin/ollama && sudo chmod 755 /usr/local/bin/ollama
sudo cp /usr/local/bin/lama-<версия>.bak   /usr/local/bin/lama   && sudo chmod 755 /usr/local/bin/lama
```

Живой сервер/ TUI откат не требуют — они держат старый inode; новый (откаты) подхватится при следующем старте.

## 6. systemd: ollama.service + drop-in'ы

Базовый юнит: `ExecStart=/usr/local/bin/ollama serve`, User=ollama, Restart=always.
Все env — в `/etc/systemd/system/ollama.service.d/`:

| Drop-in | Суть |
|---|---|
| `keepalive.conf` | `OLLAMA_KEEP_ALIVE=20m` (выгрузка модели из VRAM) |
| `kv-cache.conf` | KV cache q4_1 + Flash Attention (5060 Ti) |
| `libpath.conf` | LD_LIBRARY_PATH: **cuda_v13 ПЕРВАЯ** (sm_120 только там), потом v12, vulkan; MAX_LOADED_MODELS=2 |
| `listen.conf` | `OLLAMA_HOST=0.0.0.0:11434` (слушать все интерфейсы) |
| `override.conf` | MAX_LOADED_MODELS=2, NUM_PARALLEL=1 |
| `providers.conf` | `OLLAMA_PROVIDERS_CONFIG=/home/voron/.ollama/providers.json` (нужны ACL на ~/.ollama для юзера ollama) |
| `websearch-off.conf` | Web-инструменты TUI отключены — поиск через скилл webq |
| `lama-model.conf` | `LAMA_MODEL=orchestrator` — дефолтная модель бинарника `lama` |

После правки drop-in: `sudo systemctl daemon-reload && sudo systemctl restart ollama`.

## 7. Краткая история (чтобы не гадать)

- `0.40.12-voron-06112026` — база перед пересборками 07–08.11
- `0.40.12-voron-07112026` — добавлен бинарник `lama` (см. память `lama-binary-added-2026-11-07.md`)
- `0.40.12-voron-08112026` — `/add` принимает PDF (pdftotext), см. память `2026-11-08_add-pdf-pdftotext.md`. **Первый раз, когда lama собирали отдельно и упустили — с этого дня правило «всегда оба»**
