# Сборка/пересборка оллама/атомарная замена бинарника


## Как пересобрать и поставить бинарник (проверенный цикл)

> **Имя версии в ldflags:** `<upstream-версия>-voron-<DDMMYYYY>`.
> Пример: `0.40.2-voron-10102026`.
>
> Правило определения версии:
> 1. Выполнить `ollama --version` → получить текущий номер (например `0.40.11-voron-09102026`).
> 2. **Версия не должна уменьшаться.** Новое число должно быть ≥ того что стояло до пересборки.
> 3. Если работа с upstream (merge, pull) → брать версию из upstream (например `0.40.11`).
> 4. Если просто патчим свой код (без изменения upstream-базы) → держать то же число что было, менять только дату в суффиксе.
> 5. Суффикс: `-voron-` + текущая дата `DDMMYYYY`.
>
> Это позволяет отличить свой билд от чужого, видеть дату сборки и базовый upstream.

```bash
# Шаг 1: узнать текущую версию
ollama --version   # → например "0.40.11-voron-01122035"
# Базовое число (до -voron) = версия upstream, НЕ ниже этого ставить!

# Шаг 2: собрать (версия = та же или выше + свежая дата)
cd /home/voron/Документы/VB-ollama/GitHub Ollama кастомный бинарник/ollama fork our fork
go build -tags sqlite_fts5 -ldflags "-X github.com/ollama/ollama/version.Version=0.48.8-voron-$(date +%d%m%Y)" \
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

