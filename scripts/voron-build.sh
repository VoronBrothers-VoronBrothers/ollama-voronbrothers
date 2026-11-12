#!/usr/bin/env bash
# scripts/voron-build.sh — автоматический цикл сборки по VORON-RUNBOOK.md §3.
#
# Собирает ОБА бинарника (ollama + lama) с -tags sqlite_fts5 и ldflags,
# делает бэкап старых, атомарно подменяет (mv), проверяет.
#
# Использование:
#   scripts/voron-build.sh            # та же базовая версия, дата = сегодня
#   scripts/voron-build.sh 0.41.0     # новая upstream-база (после merge/pull)
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"
BIN="${BIN:-/usr/local/bin}"   # переопределяемо для тестов
die() { echo "❌ $*" >&2; exit 1; }

# --- вспомогательные функции (нужны и режиму --fix-backup-names) ---
# Самый свежий бэкап = самый новый по mtime (в имени есть версия и номер,
# поэтому сортировка по строке ненадёжна).
latest_bak() { ls -1t "$BIN"/$1-*.bak 2>/dev/null | head -n1 || true; }

# real_version <путь к бинарнику или .bak> -> версия, вшитая ldflags ("", если нет).
# Читаем из самого файла, а не из имени: имя могло достаться от прошлой сборки.
real_version() {
    [ -f "$1" ] || return 0
    strings -a "$1" 2>/dev/null \
        | grep -oE '[0-9]+\.[0-9]+\.[0-9]+-voron-[0-9]{8}' \
        | sort -u | tail -n1 || true
}

# strip_bak <имя файла> <префикс> -> "<версия>" из имени (без .bak и номера дубля)
strip_bak() {
    local base ver
    base=$(basename "$1" .bak)
    ver=${base#"$2"-}
    printf '%s' "$ver" | sed -E 's/-[0-9]+$//'
}

# backup_path <name> <version> — свободное имя бэкапа:
#   <name>-<version>.bak, затем <name>-<version>-2.bak, -3.bak, ...
backup_path() {
    local name="$1" ver="$2" n=1 path
    while :; do
        if [ "$n" -eq 1 ]; then path="$BIN/$name-$ver.bak"; else path="$BIN/$name-$ver-$n.bak"; fi
        [ -e "$path" ] || { printf '%s' "$path"; return; }
        n=$((n + 1))
    done
}

# --- sudo: нужен только на этапе установки ---
SUDO=""
if [ "$(id -u)" != 0 ]; then
    if sudo -n true 2>/dev/null; then SUDO="sudo"; else
        die "установка требует root, а парольный sudo недоступен. Запусти: sudo $0 ... (или настрой NOPASSWD для /usr/local/bin/ollama и lama)"
    fi
fi

# --- режим: починить имена существующих бэкапов (-dup-HHMM → -2, -3, …) ---
if [ "${1:-}" = "--fix-backup-names" ]; then
    for f in "$BIN"/*-*.bak; do
        [ -e "$f" ] || continue
        name=""
        base=$(basename "$f" .bak)
        for cand in ollama lama; do
            case "$base" in "$cand"-*) name="$cand" ;; esac
        done
        [ -n "$name" ] || { echo "skip $base (неизвестный префикс)"; continue; }

        ver=$(real_version "$f")
        if [ -z "$ver" ]; then
            ver=$(printf '%s' "$base" | sed -E "s/^$name-//; s/(-dup-[0-9]+|[0-9]+)$//; s/-$//")
            echo "⚠️  $base: версия не найдена в файле, беру из имени: $ver"
        fi

        newf=$(backup_path "$name" "$ver")

        if [ "$newf" = "$f" ]; then echo "ok $base"; continue; fi
        $SUDO mv "$f" "$newf" && echo "✅ $base → $(basename "$newf")"
    done
    exit 0
fi

# --- Версия по ранбуку §2 ---
prev_ollama=$(real_version "$BIN/ollama"); [ -n "$prev_ollama" ] || prev_ollama=$(strip_bak "$(latest_bak ollama)" ollama)
prev_lama=$(real_version   "$BIN/lama");   [ -n "$prev_lama" ]   || prev_lama=$(strip_bak   "$(latest_bak lama)"   lama)

new_base="${1:-${prev_ollama%%-voron*}}"
[ -n "$new_base" ] || die "не удалось определить версию: передай базу аргументом, например scripts/voron-build.sh 0.41.0"

# База не должна уменьшаться (sort -V: если в конце стоит prev — новая ниже)
for p in "$prev_ollama" "$prev_lama"; do
    [ -z "$p" ] && continue
    if [ "$(printf '%s\n%s\n' "$new_base" "${p%%-voron*}" | sort -V | tail -n1)" != "$new_base" ]; then
        die "новая база $new_base НИЖЕ предыдущей ${p%%-voron*} (ранбук §2: версия не должна уменьшаться)"
    fi
done

V="${new_base}-voron-$(date +%d%m%Y)"
echo "🏷  Версия сборки: $V"
[ -n "${prev_ollama:-}" ] && echo "   (предыдущие: ollama=$prev_ollama, lama=${prev_lama:-нет бэкапа})"

# --- Сборка обоих бинарников ---
echo "🔨 go build -tags sqlite_fts5 …"
go build -tags sqlite_fts5 -ldflags "-X github.com/ollama/ollama/version.Version=$V" \
    -o /tmp/ollama-patched . || die "сборка ollama"
go build -tags sqlite_fts5 -ldflags "-X github.com/ollama/ollama/version.Version=$V" \
    -o /tmp/lama-patched ./lama/ || die "сборка lama"

# --- Бэкап + атомарная замена (mv, не cp!) каждого бинарника ---
install_one() { # $1 = имя файла в /usr/local/bin
    local name="$1" bak prev dst
    dst="$BIN/$name"
    [ -f "$dst" ] || die "$dst не найден — ставь вручную (первый раз)"
    prev=$(real_version "$dst"); [ -n "$prev" ] || prev=$(strip_bak "$(latest_bak "$name")" "$name")

    if [ -n "$prev" ]; then
        bak=$(backup_path "$name" "$prev")
        $SUDO cp "$dst" "$bak" || die "бэкап $name → $(basename "$bak")"
    else
        echo "⚠️  для $name нет прежнего бэкапа — первый .bak появится только после этой замены"
        bak=""
    fi

    $SUDO mv /tmp/$name-patched "$dst" && $SUDO chmod 755 "$dst" || die "установка $name"
    echo "✅ $name: $( [ -n "${bak:-}" ] && basename "$bak") → $(basename "$dst") ($V)"
}

install_one ollama
install_one lama

# --- Проверка (ранбук §4) ---
echo ""
ls -la "$BIN/ollama" "$BIN/lama" 2>/dev/null | sed 's/^/   /'
srv=$(curl -s --max-time 3 http://127.0.0.1:11434/api/version 2>/dev/null | grep -o '"version":"[^"]*"' | cut -d'"' -f4 || true)
[ -n "$srv" ] && echo "ℹ️  сервер пока отвечает версией $srv — это нормально, новый бинарник подхватится при следующем старте."

if [ -z "$(git status --porcelain 2>/dev/null)" ]; then
    echo "📦 git: чистое дерево"
else
    echo "⚠️  в репо есть НЕЗАКОММИЧЕННЫЕ изменения — не забудь закоммитить:"
    git status --short | sed 's/^/     /'
fi

echo ""
echo "🎉 Готово. TUI: self-restart (или новое окно konsole). Сервер рестартить не нужно."
