#!/bin/zsh
set -euo pipefail

BRIDGE_LABEL="com.whatsapp-mcp.bridge"
MONITOR_LABEL="com.whatsapp-mcp.bridge-monitor"

fail() {
  print -r -- "Error: $1" >&2
  exit 1
}

require_macos_user() {
  [[ "$(uname -s)" == "Darwin" ]] || fail "launchd installation is only supported on macOS."
  [[ "${EUID:-$(id -u)}" != "0" ]] || fail "Do not run this installer with sudo; it installs per-user LaunchAgents."
}

shell_quote() {
  local value="$1"
  printf "'"
  printf "%s" "$value" | sed "s/'/'\\\\''/g"
  printf "'"
}

xml_escape() {
  printf "%s" "$1" | sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}

write_export() {
  local name="$1"
  local value="$2"
  printf "export %s=%s\n" "$name" "$(shell_quote "$value")" >> "$ENV_FILE"
}

validate_port() {
  local port="$1"
  [[ "$port" =~ '^[0-9]+$' ]] || fail "WHATSAPP_BRIDGE_PORT must be a number, got: $port"
  (( port >= 1 && port <= 65535 )) || fail "WHATSAPP_BRIDGE_PORT must be between 1 and 65535, got: $port"
}

bootout_label() {
  local domain="$1"
  local label="$2"
  launchctl bootout "$domain/$label" 2>/dev/null || true
}

require_macos_user

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
BRIDGE_DIR="$REPO_ROOT/whatsapp-bridge"
BRIDGE_BINARY="$BRIDGE_DIR/whatsapp-bridge"

[[ -d "$BRIDGE_DIR" ]] || fail "Could not find bridge directory: $BRIDGE_DIR"

PORT="${WHATSAPP_BRIDGE_PORT:-8080}"
validate_port "$PORT"
API_URL="${WHATSAPP_API_URL:-http://127.0.0.1:${PORT}/api}"

SUPPORT_DIR="$HOME/Library/Application Support/whatsapp-mcp"
STATE_DIR="$SUPPORT_DIR/state"
LOG_DIR="$HOME/Library/Logs/whatsapp-mcp"
LAUNCH_AGENTS_DIR="$HOME/Library/LaunchAgents"
ENV_FILE="$SUPPORT_DIR/launchd.env"
RUNNER_SCRIPT="$SUPPORT_DIR/run-whatsapp-bridge.sh"
MONITOR_SCRIPT="$SUPPORT_DIR/monitor-whatsapp-bridge.sh"
BRIDGE_PLIST="$LAUNCH_AGENTS_DIR/$BRIDGE_LABEL.plist"
MONITOR_PLIST="$LAUNCH_AGENTS_DIR/$MONITOR_LABEL.plist"

USER_ID="$(id -u)"
LAUNCHD_DOMAIN="gui/$USER_ID"

mkdir -p "$SUPPORT_DIR" "$STATE_DIR" "$LOG_DIR" "$LAUNCH_AGENTS_DIR"

if command -v go >/dev/null 2>&1; then
  print -r -- "Building WhatsApp bridge..."
  (cd "$BRIDGE_DIR" && go build -o "$BRIDGE_BINARY" .)
elif [[ ! -x "$BRIDGE_BINARY" ]]; then
  fail "Go is not available and no executable bridge binary exists at $BRIDGE_BINARY."
fi

[[ -x "$BRIDGE_BINARY" ]] || fail "Bridge binary is not executable: $BRIDGE_BINARY"

print -r -- "Stopping existing whatsapp-mcp LaunchAgents if present..."
bootout_label "$LAUNCHD_DOMAIN" "$MONITOR_LABEL"
bootout_label "$LAUNCHD_DOMAIN" "$BRIDGE_LABEL"

if command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
  lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >&2 || true
  fail "Port $PORT is already in use after stopping existing whatsapp-mcp LaunchAgents."
fi

old_umask="$(umask)"
umask 077
: > "$ENV_FILE"
umask "$old_umask"
write_export "WHATSAPP_MCP_REPO_ROOT" "$REPO_ROOT"
write_export "WHATSAPP_BRIDGE_DIR" "$BRIDGE_DIR"
write_export "WHATSAPP_BRIDGE_BINARY" "$BRIDGE_BINARY"
write_export "WHATSAPP_BRIDGE_PORT" "$PORT"
write_export "WHATSAPP_API_URL" "$API_URL"
write_export "WHATSAPP_MCP_LOG_DIR" "$LOG_DIR"
write_export "WHATSAPP_MCP_STATE_DIR" "$STATE_DIR"

for optional_var in WEBHOOK_URL FORWARD_SELF WHATSAPP_BRIDGE_TOKEN WHATSAPP_MEDIA_ROOTS; do
  if [[ -v "$optional_var" ]]; then
    write_export "$optional_var" "${(P)optional_var}"
  fi
done
chmod 600 "$ENV_FILE"

cat > "$RUNNER_SCRIPT" <<EOF
#!/bin/zsh
set -euo pipefail

source $(shell_quote "$ENV_FILE")
cd "\$WHATSAPP_BRIDGE_DIR"
exec "\$WHATSAPP_BRIDGE_BINARY"
EOF
chmod 755 "$RUNNER_SCRIPT"

cat > "$MONITOR_SCRIPT" <<EOF
#!/bin/zsh
set -euo pipefail

source $(shell_quote "$ENV_FILE")

BRIDGE_LABEL="$BRIDGE_LABEL"
STATE_DIR="\${WHATSAPP_MCP_STATE_DIR:-\$HOME/Library/Application Support/whatsapp-mcp/state}"
LOG_DIR="\${WHATSAPP_MCP_LOG_DIR:-\$HOME/Library/Logs/whatsapp-mcp}"
TOKEN_FILE="\$WHATSAPP_BRIDGE_DIR/store/.bridge-token"
BRIDGE_LOG="\$LOG_DIR/bridge.out.log"

mkdir -p "\$STATE_DIR"

notify() {
  local title="\$1"
  local msg="\$2"
  if command -v terminal-notifier >/dev/null 2>&1; then
    terminal-notifier -title "\$title" -message "\$msg" >/dev/null 2>&1 || true
  else
    osascript \\
      -e 'on run argv' \\
      -e 'display notification (item 2 of argv) with title (item 1 of argv)' \\
      -e 'end run' \\
      "\$title" "\$msg" >/dev/null 2>&1 || true
  fi
}

alert_once() {
  local key="\$1"
  local title="\$2"
  local msg="\$3"
  local marker="\$STATE_DIR/\$key.alerted"
  if [[ ! -f "\$marker" ]]; then
    notify "\$title" "\$msg"
    : > "\$marker"
  fi
}

clear_alert() {
  local key="\$1"
  rm -f "\$STATE_DIR/\$key.alerted"
}

USER_ID="\$(id -u)"
if ! launchctl print "gui/\$USER_ID/\$BRIDGE_LABEL" >/dev/null 2>&1; then
  alert_once "down" "WhatsApp Bridge Down" "The bridge LaunchAgent is not loaded."
  exit 0
fi
clear_alert "down"

TOKEN="\${WHATSAPP_BRIDGE_TOKEN:-}"
if [[ -z "\$TOKEN" && -r "\$TOKEN_FILE" ]]; then
  TOKEN="\$(tr -d '[:space:]' < "\$TOKEN_FILE")"
fi

if [[ -z "\$TOKEN" ]]; then
  alert_once "token" "WhatsApp Bridge Token Missing" "No WHATSAPP_BRIDGE_TOKEN is configured and \$TOKEN_FILE is unreadable."
  exit 0
fi
clear_alert "token"

API_URL="\${WHATSAPP_API_URL%/}"
HEALTH="\$(curl -sS -m 5 -H "Authorization: Bearer \$TOKEN" "\$API_URL/health" 2>/dev/null || true)"
if [[ -z "\$HEALTH" ]]; then
  alert_once "api" "WhatsApp Bridge API Unreachable" "The health endpoint did not respond at \$API_URL/health."
  exit 0
fi
clear_alert "api"

connected=false
if print -r -- "\$HEALTH" | grep -Eq '"connected"[[:space:]]*:[[:space:]]*true'; then
  connected=true
fi

if [[ "\$connected" == "true" ]]; then
  clear_alert "relink"
  clear_alert "qr"
else
  alert_once "relink" "WhatsApp Relink Needed" "The bridge is running but WhatsApp is disconnected. Check logs and scan a QR code if prompted."
  if [[ -f "\$BRIDGE_LOG" ]]; then
    if tail -n 200 "\$BRIDGE_LOG" 2>/dev/null | grep -Eiq 'Scan this QR code|Device logged out|QR code timed out|Timeout waiting for QR code scan'; then
      alert_once "qr" "WhatsApp QR Action Needed" "The bridge logs indicate that phone linking is required."
    else
      clear_alert "qr"
    fi
  fi
fi
EOF
chmod 755 "$MONITOR_SCRIPT"

cat > "$BRIDGE_PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key><string>$BRIDGE_LABEL</string>
    <key>ProgramArguments</key>
    <array>
      <string>$(xml_escape "$RUNNER_SCRIPT")</string>
    </array>
    <key>WorkingDirectory</key><string>$(xml_escape "$BRIDGE_DIR")</string>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>ThrottleInterval</key><integer>10</integer>
    <key>StandardOutPath</key><string>$(xml_escape "$LOG_DIR/bridge.out.log")</string>
    <key>StandardErrorPath</key><string>$(xml_escape "$LOG_DIR/bridge.err.log")</string>
  </dict>
</plist>
EOF

cat > "$MONITOR_PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key><string>$MONITOR_LABEL</string>
    <key>ProgramArguments</key>
    <array>
      <string>$(xml_escape "$MONITOR_SCRIPT")</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>StartInterval</key><integer>60</integer>
    <key>StandardOutPath</key><string>$(xml_escape "$LOG_DIR/monitor.out.log")</string>
    <key>StandardErrorPath</key><string>$(xml_escape "$LOG_DIR/monitor.err.log")</string>
  </dict>
</plist>
EOF

print -r -- "Loading whatsapp-mcp LaunchAgents..."
launchctl bootstrap "$LAUNCHD_DOMAIN" "$BRIDGE_PLIST"
launchctl enable "$LAUNCHD_DOMAIN/$BRIDGE_LABEL"
launchctl kickstart -k "$LAUNCHD_DOMAIN/$BRIDGE_LABEL"

launchctl bootstrap "$LAUNCHD_DOMAIN" "$MONITOR_PLIST"
launchctl enable "$LAUNCHD_DOMAIN/$MONITOR_LABEL"
launchctl kickstart -k "$LAUNCHD_DOMAIN/$MONITOR_LABEL"

print -r -- "Installed whatsapp-mcp LaunchAgents:"
print -r -- "  $BRIDGE_LABEL"
print -r -- "  $MONITOR_LABEL"
print -r -- "Logs: $LOG_DIR"
