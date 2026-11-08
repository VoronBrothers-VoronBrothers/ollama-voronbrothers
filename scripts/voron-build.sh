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
BIN=/usr/local/bin
die() { echo "❌ $*" >&2; exit 1; }

# --- sudo: нужен только на этапе установки ---
SUDO=""
if [ "$(id -u)" != 0 ]; then
    if sudo -n true 2>/dev/null; then SUDO="sudo"; else
        die "установка требует root, а парольный sudo недоступен. Запусти: sudo $0 ... (или настрой NOPASSWD для /usr/local/bin/ollama и lama)"
    fi
fi

# --- Версия по ранбуку §2 ---
latest_bak() { ls -1 "$BIN"/$1-*.bak 2>/dev/null | sort | tail -n1 || true; }

prev_ollama=$(basename "$(latest_bak ollama)" .bak 2>/dev/null | sed 's/^ollama-//' || true)
prev_lama=$(basename   "$(latest_bak lama)"   .bak 2>/dev/null | sed 's/^lama-//'   || true)

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
    prev=$(basename "$(latest_bak "$name")" .bak 2>/dev/null | sed "s/^$name-//" || true)
    [ -f "$dst" ] || die "$dst не найден — ставь вручную (первый раз)"

    if [ -n "$prev" ]; then
        bak="$BIN/$name-$prev.bak"
        [ -e "$bak" ] && bak="${bak%.bak}-dup-$(date +%H%M).bak"   # бэкап этой версии уже есть — не затираем
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
