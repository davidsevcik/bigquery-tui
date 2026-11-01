#!/bin/bash
# Records the screenshots in site/img with VHS (in Docker) against the demo
# emulator: the real bqt, in a real terminal, on fictional data.
# Usage: internal/tools/screenshots/record.sh [tape ...]   (default: every tape)
# Needs Docker (Docker Desktop or colima), Go and python3; nothing else to install.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../../.." && pwd)
E="$REPO/.scratch/shots"
VHS=ghcr.io/charmbracelet/vhs:v0.12.1

"$HERE/setup.sh"
echo '{}' > "$E/data/state/tui.json"   # default layout and style, and no first-run hint
cp -R "$E/data" "$E/data.clean"

tapes=("$@")
[ ${#tapes[@]} -gt 0 ] || tapes=($(cd "$HERE" && ls *.tape | grep -v '^common'))
for t in "${tapes[@]}"; do
  name=${t%.tape}
  rm -rf "$E/data" && cp -R "$E/data.clean" "$E/data"   # each screen starts fresh
  echo "recording $name"
  docker run --rm --network bqt-shots -v "$HERE:/vhs" -v "$E:/work" $VHS "$name.tape" >/dev/null
  cp "$E/out/$name.png" "$REPO/site/img/$name.png"
done
docker rm -f bqt-shots-emu >/dev/null
echo "done: site/img"
