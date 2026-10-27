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
# Keep installed runtime/session paths and settings unless explicitly overridden.
# This matters when the store was relocated out of a TCC-protected directory.
existing_env="$HOME/Library/Application Support/whatsapp-mcp/launchd.env"
saved_default_api_url=""
if [[ -v WHATSAPP_BRIDGE_PORT && -r "$existing_env" ]]; then
  saved_port="$(source "$existing_env"; print -r -- "${WHATSAPP_BRIDGE_PORT:-8080}")"
  saved_default_api_url="http://127.0.0.1:${saved_port}/api"
fi
if [[ -r "$existing_env" ]]; then
  for preserved_var in WHATSAPP_BRIDGE_DIR WHATSAPP_BRIDGE_BINARY WHATSAPP_BRIDGE_PORT WHATSAPP_API_URL WHATSAPP_HUB_URL WEBHOOK_URL WEBHOOK_ENABLED WHATSAPP_AUTO_DOWNLOAD_MEDIA FORWARD_SELF WHATSAPP_BRIDGE_TOKEN WHATSAPP_MEDIA_ROOTS WHATSAPP_DEVICE_NAME; do
    if [[ ! -v "$preserved_var" ]]; then
      if preserved_value="$(source "$existing_env"; [[ -v "$preserved_var" ]] || exit 1; print -r -- "${(P)preserved_var}")"; then
        # A port-only override moves the generated API URL with the listener.
        # Preserve genuinely custom URLs and explicit API URL overrides.
        if [[ "$preserved_var" == "WHATSAPP_API_URL" && "$preserved_value" == "$saved_default_api_url" ]]; then
          continue
        fi
        export "$preserved_var=$preserved_value"
      fi
    fi
  done
fi
SOURCE_BRIDGE_DIR="$REPO_ROOT/whatsapp-bridge"
BRIDGE_DIR="${WHATSAPP_BRIDGE_DIR:-$SOURCE_BRIDGE_DIR}"
BRIDGE_BINARY="${WHATSAPP_BRIDGE_BINARY:-$SOURCE_BRIDGE_DIR/whatsapp-bridge}"

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
  (cd "$SOURCE_BRIDGE_DIR" && go build -o "$BRIDGE_BINARY" .)
elif [[ ! -x "$BRIDGE_BINARY" ]]; then
  fail "Go is not available and no executable bridge binary exists at $BRIDGE_BINARY."
fi

[[ -x "$BRIDGE_BINARY" ]] || fail "Bridge binary is not executable: $BRIDGE_BINARY"

print -r -- "Stopping existing whatsapp-mcp LaunchAgents if present..."
bootout_label "$LAUNCHD_DOMAIN" "$MONITOR_LABEL"
bootout_label "$LAUNCHD_DOMAIN" "$BRIDGE_LABEL"

if command -v lsof >/dev/null 2>&1; then
  # bootout sends SIGTERM asynchronously. Allow the old process to release its
  # listener before deciding another process owns the port.
  for stop_attempt in {1..40}; do
    if ! lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
      break
    fi
    sleep 0.25
  done
  if lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >/dev/null 2>&1; then
    lsof -nP -iTCP:"$PORT" -sTCP:LISTEN >&2 || true
    fail "Port $PORT is already in use after stopping existing whatsapp-mcp LaunchAgents."
  fi
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

for optional_var in WEBHOOK_URL WEBHOOK_ENABLED WHATSAPP_AUTO_DOWNLOAD_MEDIA FORWARD_SELF WHATSAPP_BRIDGE_TOKEN WHATSAPP_MEDIA_ROOTS WHATSAPP_DEVICE_NAME WHATSAPP_HUB_URL; do
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

NOTIFIER_BINARY="$(command -v terminal-notifier 2>/dev/null || true)"
cat > "$MONITOR_SCRIPT" <<EOF
#!/bin/zsh
set -euo pipefail

source $(shell_quote "$ENV_FILE")

BRIDGE_LABEL="$BRIDGE_LABEL"
NOTIFIER_BINARY=$(shell_quote "$NOTIFIER_BINARY")
STATE_DIR="\${WHATSAPP_MCP_STATE_DIR:-\$HOME/Library/Application Support/whatsapp-mcp/state}"
LOG_DIR="\${WHATSAPP_MCP_LOG_DIR:-\$HOME/Library/Logs/whatsapp-mcp}"
TOKEN_FILE="\$WHATSAPP_BRIDGE_DIR/store/.bridge-token"
HUB_URL="\${WHATSAPP_HUB_URL:-http://localhost:8767/ui/whatsapp}"

mkdir -p "\$STATE_DIR"

notify() {
  local title="\$1"
  local msg="\$2"
  local notifier="\$NOTIFIER_BINARY"
  if [[ ! -x "\$notifier" ]]; then
    notifier="\$(command -v terminal-notifier 2>/dev/null || true)"
  fi
  if [[ -x "\$notifier" ]] && "\$notifier" -title "\$title" -message "\$msg" -open "\$HUB_URL" >/dev/null 2>&1; then
    return 0
  fi
    osascript \\
      -e 'on run argv' \\
      -e 'display notification (item 2 of argv) with title (item 1 of argv)' \\
      -e 'end run' \\
      "\$title" "\$msg" >/dev/null 2>&1 || true
}

alert_once() {
  local key="\$1"
  local title="\$2"
  local msg="\$3"
  local marker="\$STATE_DIR/\$key.alerted"
  local previous=0
  if [[ -f "\$marker" ]]; then
    previous="\$(stat -f %m "\$marker" 2>/dev/null || print 0)"
  fi
  if (( \$(date +%s) - previous >= 21600 )); then
    notify "\$title" "\$msg Open \$HUB_URL"
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
  # A denied read (e.g. macOS TCC/sandbox blocking this LaunchAgent's open())
  # must not abort the monitor under 'set -e'; '|| TOKEN=""' keeps the failing
  # assignment out of set -e's reach so the alert below still fires (#249).
  TOKEN="\$(tr -d '[:space:]' < "\$TOKEN_FILE" 2>/dev/null)" || TOKEN=""
fi

if [[ -z "\$TOKEN" ]]; then
  alert_once "token" "WhatsApp Bridge Token Missing" "Could not read a bridge token. macOS may be blocking access to \$TOKEN_FILE; set WHATSAPP_BRIDGE_TOKEN or re-run scripts/install-launchd-macos.sh to store it in launchd.env."
  exit 0
fi

API_URL="\${WHATSAPP_API_URL%/}"
response="\$(curl -sS -m 5 -H "Authorization: Bearer \$TOKEN" -w \$'\n%{http_code}' "\$API_URL/health" 2>/dev/null || true)"
if [[ -z "\$response" ]]; then
  alert_once "api" "WhatsApp Bridge API Unreachable" "The health endpoint did not respond at \$API_URL/health."
  exit 0
fi
http_code="\${response##*\$'\n'}"
HEALTH="\${response%\$'\n'*}"
if [[ "\$http_code" == "401" || "\$http_code" == "403" ]]; then
  alert_once "token" "WhatsApp Bridge Token Invalid" "The health endpoint rejected the configured token. Re-sync WHATSAPP_BRIDGE_TOKEN or \$TOKEN_FILE."
  exit 0
fi
if [[ "\$http_code" != "200" && "\$http_code" != "503" ]]; then
  alert_once "api" "WhatsApp Bridge API Unreachable" "Unexpected HTTP \$http_code from \$API_URL/health."
  exit 0
fi
clear_alert "token"
clear_alert "api"

connected=false
if print -r -- "\$HEALTH" | grep -Eq '"connected"[[:space:]]*:[[:space:]]*true'; then
  connected=true
fi

if [[ "\$connected" == "true" ]]; then
  if [[ -f "\$STATE_DIR/connection-state" ]]; then
    notify "WhatsApp Connected Again" "AutoJack can receive WhatsApp messages again."
    rm -f "\$STATE_DIR/connection-state"
  fi
  rm -f "\$STATE_DIR/connection-started"
  clear_alert "relink"
  clear_alert "qr"
  clear_alert "connection"
  exit 0
fi

# Read the structured cause. A bridge upgrade is different from a phone logout;
# never infer a fresh logout from historical log lines.
connection_state="\$(print -r -- "\$HEALTH" | sed -nE 's/.*"state"[[:space:]]*:[[:space:]]*"([^" ]+)".*/\\1/p')"
connection_state="\${connection_state:-disconnected}"
[[ "\$connection_state" == "pairing" ]] && connection_state="logged_out"
previous_state="\$(cat "\$STATE_DIR/connection-state" 2>/dev/null || true)"
if [[ "\$connection_state" == "connecting" ]]; then
  # Track the outage across attempts: a 60s monitor can miss each 45s timeout.
  if [[ ! -f "\$STATE_DIR/connection-started" ]]; then
    date +%s > "\$STATE_DIR/connection-started"
  fi
  outage_started="\$(cat "\$STATE_DIR/connection-started")"
  if [[ -z "\$previous_state" ]] && (( \$(date +%s) - outage_started < 90 )); then
    exit 0
  fi
  # Existing outages retain their cause and six-hour reminder during retries.
  connection_state="\${previous_state:-disconnected}"
fi
if [[ "\$connection_state" != "\$previous_state" ]]; then
  clear_alert "connection"
fi
print -r -- "\$connection_state" > "\$STATE_DIR/connection-state"
case "\$connection_state" in
  outdated)
    alert_once "connection" "WhatsApp Update Required" "Update and rebuild the bridge. Scanning a QR code will not fix an outdated client."
    ;;
  logged_out|pairing)
    alert_once "connection" "WhatsApp Linking Needed" "Open AutoHub and scan the current QR code from WhatsApp > Linked Devices > Link a Device."
    ;;
  *)
    alert_once "connection" "WhatsApp Disconnected" "AutoJack cannot receive messages. The bridge will retry; open AutoHub for status or reconnect."
    ;;
esac

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

# Persist the bridge token into launchd.env (already mode 600) so the monitor
# reads it from the environment instead of store/.bridge-token. That file can
# live inside a macOS TCC-protected location (e.g. ~/Documents), where a
# per-user LaunchAgent shell may be denied the read and previously aborted the
# monitor (#249). launchd.env lives under ~/Library/Application Support and is
# not subject to that boundary. Skip when an explicit token was already
# provided (written above). The bridge writes the token on first start, so wait
# briefly for it to appear.
if [[ -z "${WHATSAPP_BRIDGE_TOKEN:-}" ]]; then
  token_src="$BRIDGE_DIR/store/.bridge-token"
  captured=""
  attempt=0
  while (( attempt < 20 )); do
    if [[ -e "$token_src" ]]; then
      captured="$(tr -d '[:space:]' < "$token_src" 2>/dev/null || true)"
      if [[ -n "$captured" ]]; then
        break
      fi
    fi
    attempt=$((attempt + 1))
    sleep 0.5
  done
  if [[ -n "$captured" ]]; then
    write_export "WHATSAPP_BRIDGE_TOKEN" "$captured"
    print -r -- "Stored bridge token in launchd.env for the monitor."
  else
    print -r -- "Note: no bridge token captured; the monitor will read $token_src directly (see #249 if it exits)." >&2
  fi
fi

launchctl bootstrap "$LAUNCHD_DOMAIN" "$MONITOR_PLIST"
launchctl enable "$LAUNCHD_DOMAIN/$MONITOR_LABEL"
launchctl kickstart -k "$LAUNCHD_DOMAIN/$MONITOR_LABEL"

print -r -- "Installed whatsapp-mcp LaunchAgents:"
print -r -- "  $BRIDGE_LABEL"
print -r -- "  $MONITOR_LABEL"
print -r -- "Logs: $LOG_DIR"
