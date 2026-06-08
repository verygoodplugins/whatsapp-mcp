#!/bin/zsh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
GREP_BIN="$(command -v grep)"
WC_BIN="$(command -v wc)"
TR_BIN="$(command -v tr)"

failures=0

fail() {
  print -r -- "FAIL: $1" >&2
  failures=$((failures + 1))
}

assert_file() {
  local path="$1"
  [[ -f "$path" ]] || fail "expected file: $path"
}

assert_executable() {
  local path="$1"
  [[ -x "$path" ]] || fail "expected executable: $path"
}

assert_dir() {
  local path="$1"
  [[ -d "$path" ]] || fail "expected directory: $path"
}

assert_not_exists() {
  local path="$1"
  [[ ! -e "$path" ]] || fail "expected path to be removed: $path"
}

assert_contains() {
  local path="$1"
  local needle="$2"
  if ! "$GREP_BIN" -Fq -- "$needle" "$path"; then
    fail "expected $path to contain: $needle"
  fi
}

assert_line_count() {
  local path="$1"
  local expected="$2"
  local actual=0
  if [[ -f "$path" ]]; then
    actual="$("$WC_BIN" -l < "$path" | "$TR_BIN" -d '[:space:]')"
  fi
  [[ "$actual" == "$expected" ]] || fail "expected $path to have $expected lines, got $actual"
}

make_fixture() {
  local tmp
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/whatsapp-mcp-launchd-test.XXXXXX")"
  mkdir -p "$tmp/repo/scripts" "$tmp/repo/whatsapp-bridge/store" "$tmp/home" "$tmp/fakebin"

  cp "$REPO_ROOT/scripts/install-launchd-macos.sh" "$tmp/repo/scripts/"
  cp "$REPO_ROOT/scripts/uninstall-launchd-macos.sh" "$tmp/repo/scripts/"

  cat > "$tmp/fakebin/uname" <<'EOF'
#!/bin/sh
printf 'Darwin\n'
EOF

  cat > "$tmp/fakebin/id" <<'EOF'
#!/bin/sh
if [ "$1" = "-u" ]; then
  printf '501\n'
  exit 0
fi
/usr/bin/id "$@"
EOF

  cat > "$tmp/fakebin/go" <<'EOF'
#!/bin/sh
printf 'go %s\n' "$*" >> "$FAKE_CMD_LOG"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
  fi
  shift || true
done
if [ -n "$out" ]; then
  mkdir -p "$(dirname "$out")"
  printf '#!/bin/sh\nexit 0\n' > "$out"
  chmod +x "$out"
fi
exit 0
EOF

  cat > "$tmp/fakebin/lsof" <<'EOF'
#!/bin/sh
printf 'lsof %s\n' "$*" >> "$FAKE_CMD_LOG"
if [ "${FAKE_LSOF_BUSY:-0}" = "1" ]; then
  printf 'whatsapp 123 user 10u IPv4 TCP 127.0.0.1:8080 (LISTEN)\n'
  exit 0
fi
exit 1
EOF

  cat > "$tmp/fakebin/launchctl" <<'EOF'
#!/bin/sh
printf 'launchctl %s\n' "$*" >> "$FAKE_CMD_LOG"
if [ "${1:-}" = "print" ] && [ "${FAKE_LAUNCHCTL_PRINT_FAIL:-0}" = "1" ]; then
  exit 1
fi
exit 0
EOF

  cat > "$tmp/fakebin/curl" <<'EOF'
#!/bin/sh
printf 'curl %s\n' "$*" >> "$FAKE_CMD_LOG"
if [ "${FAKE_CURL_EMPTY:-0}" = "1" ]; then
  exit 7
fi
printf '%s\n' "${FAKE_CURL_RESPONSE:-{\"status\":\"ok\",\"connected\":true}}"
exit "${FAKE_CURL_EXIT:-0}"
EOF

  cat > "$tmp/fakebin/osascript" <<'EOF'
#!/bin/sh
printf 'osascript %s\n' "$*" >> "$FAKE_NOTIFY_LOG"
exit 0
EOF

  chmod +x "$tmp/fakebin/"*
  print -r -- "$tmp"
}

run_installer() {
  local tmp="$1"
  (
    cd "$tmp/repo"
    HOME="$tmp/home" \
    PATH="$tmp/fakebin:/usr/bin:/bin" \
    FAKE_CMD_LOG="$tmp/cmd.log" \
    ./scripts/install-launchd-macos.sh
  )
}

run_uninstaller() {
  local tmp="$1"
  (
    cd "$tmp/repo"
    HOME="$tmp/home" \
    PATH="$tmp/fakebin:/usr/bin:/bin" \
    FAKE_CMD_LOG="$tmp/cmd.log" \
    ./scripts/uninstall-launchd-macos.sh
  )
}

run_monitor() {
  local tmp="$1"
  local response="$2"
  HOME="$tmp/home" \
  PATH="$tmp/fakebin:/usr/bin:/bin" \
  FAKE_CMD_LOG="$tmp/cmd.log" \
  FAKE_NOTIFY_LOG="$tmp/notify.log" \
  FAKE_CURL_RESPONSE="$response" \
  "$tmp/home/Library/Application Support/whatsapp-mcp/monitor-whatsapp-bridge.sh"
}

test_install_generates_launchd_files() {
  local tmp support launch_agents logs repo
  tmp="$(make_fixture)"
  repo="$(cd "$tmp/repo" && pwd)"
  run_installer "$tmp"

  support="$tmp/home/Library/Application Support/whatsapp-mcp"
  launch_agents="$tmp/home/Library/LaunchAgents"
  logs="$tmp/home/Library/Logs/whatsapp-mcp"

  assert_dir "$support"
  assert_dir "$logs"
  assert_executable "$support/run-whatsapp-bridge.sh"
  assert_executable "$support/monitor-whatsapp-bridge.sh"
  assert_file "$support/launchd.env"
  assert_file "$launch_agents/com.whatsapp-mcp.bridge.plist"
  assert_file "$launch_agents/com.whatsapp-mcp.bridge-monitor.plist"

  assert_contains "$support/launchd.env" "export WHATSAPP_BRIDGE_PORT='8080'"
  assert_contains "$support/launchd.env" "export WHATSAPP_API_URL='http://127.0.0.1:8080/api'"
  assert_contains "$support/launchd.env" "export WHATSAPP_MCP_REPO_ROOT='$repo'"
  assert_contains "$support/launchd.env" "export WHATSAPP_BRIDGE_DIR='$repo/whatsapp-bridge'"
  assert_contains "$support/launchd.env" "export WHATSAPP_BRIDGE_BINARY='$repo/whatsapp-bridge/whatsapp-bridge'"

  assert_contains "$launch_agents/com.whatsapp-mcp.bridge.plist" "com.whatsapp-mcp.bridge"
  assert_contains "$launch_agents/com.whatsapp-mcp.bridge.plist" "$support/run-whatsapp-bridge.sh"
  assert_contains "$launch_agents/com.whatsapp-mcp.bridge.plist" "$logs/bridge.out.log"
  assert_contains "$launch_agents/com.whatsapp-mcp.bridge-monitor.plist" "com.whatsapp-mcp.bridge-monitor"
  assert_contains "$launch_agents/com.whatsapp-mcp.bridge-monitor.plist" "$support/monitor-whatsapp-bridge.sh"
  assert_contains "$launch_agents/com.whatsapp-mcp.bridge-monitor.plist" "<key>StartInterval</key><integer>60</integer>"

  assert_contains "$tmp/cmd.log" "go build -o $repo/whatsapp-bridge/whatsapp-bridge ."
  assert_contains "$tmp/cmd.log" "launchctl bootout gui/501/com.whatsapp-mcp.bridge"
  assert_contains "$tmp/cmd.log" "launchctl bootstrap gui/501 $launch_agents/com.whatsapp-mcp.bridge.plist"
  assert_contains "$tmp/cmd.log" "launchctl kickstart -k gui/501/com.whatsapp-mcp.bridge-monitor"
}

test_install_preserves_optional_env_values() {
  local tmp support
  tmp="$(make_fixture)"
  (
    cd "$tmp/repo"
    HOME="$tmp/home" \
    PATH="$tmp/fakebin:/usr/bin:/bin" \
    FAKE_CMD_LOG="$tmp/cmd.log" \
    WHATSAPP_BRIDGE_PORT="9090" \
    WEBHOOK_URL="http://127.0.0.1:8769/whatsapp/webhook" \
    FORWARD_SELF="true" \
    WHATSAPP_BRIDGE_TOKEN="test token with spaces" \
    WHATSAPP_MEDIA_ROOTS="/tmp/outbox:/tmp/other outbox" \
    ./scripts/install-launchd-macos.sh
  )

  support="$tmp/home/Library/Application Support/whatsapp-mcp"
  assert_contains "$support/launchd.env" "export WHATSAPP_BRIDGE_PORT='9090'"
  assert_contains "$support/launchd.env" "export WHATSAPP_API_URL='http://127.0.0.1:9090/api'"
  assert_contains "$support/launchd.env" "export WEBHOOK_URL='http://127.0.0.1:8769/whatsapp/webhook'"
  assert_contains "$support/launchd.env" "export FORWARD_SELF='true'"
  assert_contains "$support/launchd.env" "export WHATSAPP_BRIDGE_TOKEN='test token with spaces'"
  assert_contains "$support/launchd.env" "export WHATSAPP_MEDIA_ROOTS='/tmp/outbox:/tmp/other outbox'"
}

test_uninstall_removes_generated_files_only() {
  local tmp support launch_agents logs
  tmp="$(make_fixture)"
  run_installer "$tmp"

  support="$tmp/home/Library/Application Support/whatsapp-mcp"
  launch_agents="$tmp/home/Library/LaunchAgents"
  logs="$tmp/home/Library/Logs/whatsapp-mcp"
  print -r -- "db" > "$tmp/repo/whatsapp-bridge/store/messages.db"
  print -r -- "log" > "$logs/bridge.out.log"

  run_uninstaller "$tmp"

  assert_not_exists "$launch_agents/com.whatsapp-mcp.bridge.plist"
  assert_not_exists "$launch_agents/com.whatsapp-mcp.bridge-monitor.plist"
  assert_not_exists "$support"
  assert_file "$tmp/repo/whatsapp-bridge/store/messages.db"
  assert_file "$logs/bridge.out.log"
  assert_contains "$tmp/cmd.log" "launchctl bootout gui/501/com.whatsapp-mcp.bridge"
  assert_contains "$tmp/cmd.log" "launchctl bootout gui/501/com.whatsapp-mcp.bridge-monitor"
}

test_monitor_alerts_once_and_clears_on_recovery() {
  local tmp support state
  tmp="$(make_fixture)"
  run_installer "$tmp"
  support="$tmp/home/Library/Application Support/whatsapp-mcp"
  state="$support/state"
  print -r -- "token-1234567890123456" > "$tmp/repo/whatsapp-bridge/store/.bridge-token"

  run_monitor "$tmp" '{"status":"disconnected","connected":false}'
  run_monitor "$tmp" '{"status":"disconnected","connected":false}'
  assert_line_count "$tmp/notify.log" 1
  assert_file "$state/relink.alerted"

  run_monitor "$tmp" '{"status":"ok","connected":true}'
  assert_line_count "$tmp/notify.log" 1
  assert_not_exists "$state/relink.alerted"
}

for test_name in \
  test_install_generates_launchd_files \
  test_install_preserves_optional_env_values \
  test_uninstall_removes_generated_files_only \
  test_monitor_alerts_once_and_clears_on_recovery
do
  print -r -- "Running $test_name"
  "$test_name"
done

if (( failures > 0 )); then
  print -r -- "$failures test failure(s)" >&2
  exit 1
fi

print -r -- "All launchd script tests passed"
