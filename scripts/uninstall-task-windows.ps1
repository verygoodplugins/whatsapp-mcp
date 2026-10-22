# Removes the Task Scheduler task and the files that install-task-windows.ps1
# generated. Keeps whatsapp-bridge\store\ and the logs.
$ErrorActionPreference = 'Stop'

$Task = @{ TaskName = 'whatsapp-mcp-bridge'; TaskPath = '\' }

function Fail([string]$Message) {
    Write-Host "Error: $Message"
    exit 1
}

function Stop-InstalledTask {
    if (-not (Get-ScheduledTask @Task -ErrorAction SilentlyContinue)) { return }
    Stop-ScheduledTask @Task
    for ($i = 0; $i -lt 20 -and (Get-ScheduledTask @Task).State -eq 'Running'; $i++) {
        Start-Sleep -Milliseconds 500
    }
}

if ($env:OS -ne 'Windows_NT') { Fail 'Task Scheduler uninstall is only supported on Windows.' }
if (-not $env:LOCALAPPDATA) { Fail 'LOCALAPPDATA is not set.' }

$AppDir = Join-Path $env:LOCALAPPDATA 'whatsapp-mcp'
$LogDir = Join-Path $AppDir 'logs'
$PidFile = Join-Path $AppDir 'bridge.pid'

# Stopping the task ends the runner but may leave its bridge running. The
# runner records the bridge it starts; stop that process only while the PID
# still runs the recorded executable, never by image name.
function Stop-InstalledBridge {
    if (-not (Test-Path -LiteralPath $PidFile)) { return }
    $bridgePid, $bridgePath = @(Get-Content -LiteralPath $PidFile -Encoding UTF8)
    if ($bridgePid -match '^[0-9]+$' -and $bridgePath) {
        $process = Get-Process -Id $bridgePid -ErrorAction SilentlyContinue
        if ($process -and $process.Path -eq $bridgePath) {
            Stop-Process -InputObject $process -Force -ErrorAction SilentlyContinue
            if (-not $process.WaitForExit(10000)) { Fail "Could not stop the bridge process $bridgePid." }
        }
    }
    Remove-Item -LiteralPath $PidFile -Force
}

Write-Host 'Stopping the whatsapp-mcp task if present...'
Stop-InstalledTask
Stop-InstalledBridge
if (Get-ScheduledTask @Task -ErrorAction SilentlyContinue) {
    Unregister-ScheduledTask @Task -Confirm:$false
}

foreach ($name in 'run-whatsapp-bridge.ps1', 'bridge-task.json') {
    $path = Join-Path $AppDir $name
    if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
}
if ((Test-Path -LiteralPath $AppDir) -and -not (Get-ChildItem -LiteralPath $AppDir -Force)) {
    Remove-Item -LiteralPath $AppDir
}

Write-Host 'Removed the whatsapp-mcp task and generated support files.'
Write-Host 'Preserved bridge data under whatsapp-bridge\store\.'
Write-Host "Logs were left in place: $LogDir"
