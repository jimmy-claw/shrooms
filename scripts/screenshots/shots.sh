#!/usr/bin/env bash
# Photographs the Basecamp views on made-up data for the website:
# site/shots/*.png. Nothing real is in them — no machine, address, key or
# conversation of anybody's — so they are safe to publish.
#
#   scripts/screenshots/shots.sh
#
# Needs a qml runtime (Qt 6), as basecamp/test/check.sh does, and Go for the
# invite's QR code.
set -euo pipefail
cd "$(dirname "$0")/../.."
QML=${QML:-$(command -v qml6 || command -v qml || ls -d /nix/store/*-qtdeclarative-*/bin/qml 2>/dev/null | sort -V | tail -1)}
[ -x "$QML" ] || { echo "no qml runtime; set QML=" >&2; exit 1; }
QMLDIR=$(dirname "$(dirname "$QML")")/lib/qt-6/qml
[ -d "$QMLDIR" ] || QMLDIR=$(dirname "$(dirname "$QML")")/qml
out="$PWD/site/shots"
mkdir -p "$out"
work=$(mktemp -d); trap 'rm -rf "$work"' EXIT

run() {  # dir-with-Main.qml demo.qml
    TZ=UTC QT_SCALE_FACTOR=${SCALE:-2} QT_QPA_PLATFORM=offscreen QT_ASSUME_STDERR_HAS_CONSOLE=1 \
    QML_IMPORT_PATH="$QMLDIR" QML2_IMPORT_PATH="$QMLDIR" \
    timeout 120 "$QML" -I "$1" "$1/$2" "$out" 2>&1 | grep -E '^qml: SHOT|Error' || true
}

mkdir -p "$work/shrooms" "$work/agents"
cp basecamp/Main.qml "$work/shrooms/"
cp scripts/screenshots/ShroomsDemo.qml "$work/shrooms/"
run "$work/shrooms" ShroomsDemo.qml
# The same view inside a real Basecamp window (scripts/screenshots/frame.py):
# rendered at the window's panel size, at 1x to match the frame.
mkdir -p "$work/framed"
TZ=UTC QT_SCALE_FACTOR=1 QT_QPA_PLATFORM=offscreen QT_ASSUME_STDERR_HAS_CONSOLE=1 \
QML_IMPORT_PATH="$QMLDIR" QML2_IMPORT_PATH="$QMLDIR" \
timeout 120 "$QML" -I "$work/shrooms" "$work/shrooms/ShroomsDemo.qml" size=1784x1087 "$work/framed" 2>&1 | grep -E '^qml: SHOT' || true
python3 scripts/screenshots/frame.py "$work/framed" "$out"
# The framed ones are what the site uses; the bare renders were the step.
rm -f "$out"/basecamp-*.png

if [ -f scripts/screenshots/AgentsDemo.qml ]; then
    cp basecamp-agents/Main.qml "$work/agents/"
    cp scripts/screenshots/AgentsDemo.qml "$work/agents/"
    run "$work/agents" AgentsDemo.qml
fi
ls -la "$out"
# Lossless, and worth a third of the bytes on a dark UI.
python3 - "$out" <<'PY'
import sys, glob
from PIL import Image
for f in glob.glob(sys.argv[1] + "/*.png"):
    Image.open(f).convert("RGB").save(f, optimize=True)
PY
du -sh "$out"
