# Removes the Task Scheduler task and the files that install-task-windows.ps1
# generated. Keeps whatsapp-bridge\store\ and the logs.
$ErrorActionPreference = 'Stop'

$Task = @{ TaskName = 'whatsapp-mcp-bridge'; TaskPath = '\' }

function Fail([string]$Message) {
    Write-Host "Error: $Message"
    exit 1
}

# Stopping the task ends the launcher, and its job object ends the runner and
# the bridge with it.
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

Write-Host 'Stopping the whatsapp-mcp task if present...'
Stop-InstalledTask
if (Get-ScheduledTask @Task -ErrorAction SilentlyContinue) {
    Unregister-ScheduledTask @Task -Confirm:$false
}

foreach ($name in 'whatsapp-bridge-launcher.exe', 'run-whatsapp-bridge.ps1', 'bridge-task.json') {
    $path = Join-Path $AppDir $name
    if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
}
if ((Test-Path -LiteralPath $AppDir) -and -not (Get-ChildItem -LiteralPath $AppDir -Force)) {
    Remove-Item -LiteralPath $AppDir
}

Write-Host 'Removed the whatsapp-mcp task and generated support files.'
Write-Host 'Preserved bridge data under whatsapp-bridge\store\.'
Write-Host "Logs were left in place: $LogDir"
