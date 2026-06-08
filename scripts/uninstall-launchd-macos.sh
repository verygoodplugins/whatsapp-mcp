#!/bin/zsh
set -euo pipefail

BRIDGE_LABEL="com.whatsapp-mcp.bridge"
MONITOR_LABEL="com.whatsapp-mcp.bridge-monitor"

fail() {
  print -r -- "Error: $1" >&2
  exit 1
}

[[ "$(uname -s)" == "Darwin" ]] || fail "launchd uninstall is only supported on macOS."
[[ "${EUID:-$(id -u)}" != "0" ]] || fail "Do not run this uninstaller with sudo; it removes per-user LaunchAgents."

SUPPORT_DIR="$HOME/Library/Application Support/whatsapp-mcp"
LOG_DIR="$HOME/Library/Logs/whatsapp-mcp"
LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
BRIDGE_PLIST="$LAUNCH_AGENTS_DIR/$BRIDGE_LABEL.plist"
MONITOR_PLIST="$LAUNCH_AGENTS_DIR/$MONITOR_LABEL.plist"
LAUNCHD_DOMAIN="gui/$(id -u)"

bootout_label() {
  local label="$1"
  launchctl bootout "$LAUNCHD_DOMAIN/$label" 2>/dev/null || true
}

print -r -- "Stopping whatsapp-mcp LaunchAgents if present..."
bootout_label "$MONITOR_LABEL"
bootout_label "$BRIDGE_LABEL"

rm -f "$BRIDGE_PLIST" "$MONITOR_PLIST"
rm -f "$SUPPORT_DIR/run-whatsapp-bridge.sh"
rm -f "$SUPPORT_DIR/monitor-whatsapp-bridge.sh"
rm -f "$SUPPORT_DIR/launchd.env"
rm -rf "$SUPPORT_DIR/state"
rmdir "$SUPPORT_DIR" 2>/dev/null || true

print -r -- "Removed whatsapp-mcp LaunchAgents and generated support files."
print -r -- "Preserved bridge data under whatsapp-bridge/store/."
print -r -- "Logs were left in place: $LOG_DIR"
