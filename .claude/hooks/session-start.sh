#!/bin/bash
# SessionStart hook: installs the plugins this repo's .claude/settings.json
# declares under "enabledPlugins", so they are available from the first turn
# of every cloud session instead of needing a manual install.
set -euo pipefail

if [ "${CLAUDE_CODE_REMOTE:-}" != "true" ]; then
  exit 0
fi

SETTINGS="$CLAUDE_PROJECT_DIR/.claude/settings.json"
if [ ! -f "$SETTINGS" ]; then
  exit 0
fi

jq -r '.enabledPlugins // {} | keys[]' "$SETTINGS" | while read -r plugin; do
  [ -z "$plugin" ] && continue
  echo "Installing plugin: $plugin"
  claude plugin install "$plugin" -s project -y --json || echo "warning: failed to install $plugin" >&2
done
