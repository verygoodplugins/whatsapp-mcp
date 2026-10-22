# Installs a per-user Task Scheduler task that starts the WhatsApp bridge at
# login and starts it again whenever it exits. Needs no administrator rights.
$ErrorActionPreference = 'Stop'

$Task = @{ TaskName = 'whatsapp-mcp-bridge'; TaskPath = '\' }
$RestartDelaySeconds = 10

function Fail([string]$Message) {
    Write-Host "Error: $Message"
    exit 1
}

function Test-CgoToolchain {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) { return $false }
    if ("$(go env CGO_ENABLED)".Trim() -ne '1') { return $false }
    $cc = "$(go env CC)".Trim()
    return [bool]($cc -and (Get-Command $cc -ErrorAction SilentlyContinue))
}

function Stop-InstalledTask {
    if (-not (Get-ScheduledTask @Task -ErrorAction SilentlyContinue)) { return }
    Stop-ScheduledTask @Task
    for ($i = 0; $i -lt 20 -and (Get-ScheduledTask @Task).State -eq 'Running'; $i++) {
        Start-Sleep -Milliseconds 500
    }
}

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

if ($env:OS -ne 'Windows_NT') { Fail 'Task Scheduler installation is only supported on Windows.' }
if (-not $env:LOCALAPPDATA) { Fail 'LOCALAPPDATA is not set.' }

$RepoRoot = Split-Path -Parent $PSScriptRoot
$BridgeDir = Join-Path $RepoRoot 'whatsapp-bridge'
$BridgeBinary = Join-Path $BridgeDir 'whatsapp-bridge.exe'
$NewBinary = Join-Path $BridgeDir 'whatsapp-bridge.new.exe'

if (-not (Test-Path -LiteralPath $BridgeDir -PathType Container)) { Fail "Could not find bridge directory: $BridgeDir" }

$Port = if ($env:WHATSAPP_BRIDGE_PORT) { $env:WHATSAPP_BRIDGE_PORT } else { '8080' }
if ($Port -notmatch '^[0-9]{1,5}$' -or [int]$Port -lt 1 -or [int]$Port -gt 65535) {
    Fail "WHATSAPP_BRIDGE_PORT must be a number between 1 and 65535, got: $Port"
}

$AppDir = Join-Path $env:LOCALAPPDATA 'whatsapp-mcp'
$LogDir = Join-Path $AppDir 'logs'
$ConfigFile = Join-Path $AppDir 'bridge-task.json'
$RunnerScript = Join-Path $AppDir 'run-whatsapp-bridge.ps1'
$PidFile = Join-Path $AppDir 'bridge.pid'
$Identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()

New-Item -ItemType Directory -Force -Path $AppDir, $LogDir | Out-Null

# Build next to the current binary: Windows cannot overwrite an executable
# while the existing task is still running it.
$built = $false
if (Test-CgoToolchain) {
    Write-Host 'Building WhatsApp bridge...'
    Push-Location -LiteralPath $BridgeDir
    try { go build -o $NewBinary . } finally { Pop-Location }
    if ($LASTEXITCODE -ne 0) { Fail 'go build failed; see the output above.' }
    $built = $true
} elseif (Test-Path -LiteralPath $BridgeBinary -PathType Leaf) {
    Write-Host "Go with cgo is not available; using the existing $BridgeBinary"
} else {
    Fail "Go with cgo is not available and no bridge binary exists at $BridgeBinary. See the README's Windows section for the C compiler the bridge needs."
}

Write-Host 'Stopping the existing whatsapp-mcp task if present...'
Stop-InstalledTask
Stop-InstalledBridge

if (Get-Command Get-NetTCPConnection -ErrorAction SilentlyContinue) {
    $listeners = @(Get-NetTCPConnection -State Listen -LocalPort ([int]$Port) -ErrorAction SilentlyContinue)
    if ($listeners.Count -gt 0) {
        foreach ($listener in $listeners) {
            $owner = Get-Process -Id $listener.OwningProcess -ErrorAction SilentlyContinue
            Write-Host ("  {0}:{1} pid {2} {3}" -f $listener.LocalAddress, $listener.LocalPort, $listener.OwningProcess, $owner.Path)
        }
        Fail "Port $Port is already in use after stopping the existing whatsapp-mcp task."
    }
}

if ($built) {
    try {
        Move-Item -LiteralPath $NewBinary -Destination $BridgeBinary -Force
    } catch {
        Fail "Could not replace $BridgeBinary; is a bridge you started by hand still running it? $($_.Exception.Message)"
    }
}

$environment = [ordered]@{ WHATSAPP_BRIDGE_PORT = $Port }
# The bridge treats an empty value as unset, and PowerShell 7 can leave a
# cleared variable defined but empty, so only non-empty values are kept.
foreach ($name in 'WEBHOOK_URL', 'WEBHOOK_ENABLED', 'WHATSAPP_AUTO_DOWNLOAD_MEDIA', 'FORWARD_SELF', 'WHATSAPP_BRIDGE_TOKEN', 'WHATSAPP_MEDIA_ROOTS') {
    $value = [Environment]::GetEnvironmentVariable($name)
    if ($value) { $environment[$name] = $value }
}
$config = [ordered]@{
    BridgeDir           = $BridgeDir
    BridgeBinary        = $BridgeBinary
    LogDir              = $LogDir
    RestartDelaySeconds = $RestartDelaySeconds
    Environment         = $environment
}
$utf8 = [System.Text.UTF8Encoding]::new($false)
# The config can hold WHATSAPP_BRIDGE_TOKEN, so only the current user may read
# it. Restrict the file before anything is written into it.
[System.IO.File]::WriteAllText($ConfigFile, '', $utf8)
icacls.exe $ConfigFile /inheritance:r /grant:r "*$($Identity.User.Value):(F)" | Out-Null
if ($LASTEXITCODE -ne 0) { Fail "Could not restrict access to $ConfigFile." }
[System.IO.File]::WriteAllText($ConfigFile, ($config | ConvertTo-Json), $utf8)

$runner = @'
# Generated by scripts/install-task-windows.ps1; re-run the installer instead
# of editing this file. Task Scheduler starts it at login without a window.
$ErrorActionPreference = 'Stop'

$config = [System.IO.File]::ReadAllText((Join-Path $PSScriptRoot 'bridge-task.json')) | ConvertFrom-Json
$pidFile = Join-Path $PSScriptRoot 'bridge.pid'
foreach ($entry in $config.Environment.PSObject.Properties) {
    [Environment]::SetEnvironmentVariable($entry.Name, [string]$entry.Value, 'Process')
}

# Logging must never stop the runner, or the bridge stays down until the next
# login.
function Write-RunnerLog([string]$Message) {
    $line = '{0} {1}' -f (Get-Date -Format s), $Message
    Add-Content -LiteralPath (Join-Path $config.LogDir 'runner.log') -Value $line -Encoding UTF8 -ErrorAction SilentlyContinue
}

# Appends without buffering, and shares the file so it can be read while the
# bridge runs.
function Open-Log([string]$Name) {
    [System.IO.FileStream]::new((Join-Path $config.LogDir $Name), [System.IO.FileMode]::Append,
        [System.IO.FileAccess]::Write, [System.IO.FileShare]'ReadWrite, Delete', 1)
}

# A bridge left running by an earlier runner that was stopped without its
# child. Only the recorded executable is stopped, never by image name.
function Stop-OrphanedBridge {
    if (-not (Test-Path -LiteralPath $pidFile)) { return }
    $bridgePid, $bridgePath = @(Get-Content -LiteralPath $pidFile -Encoding UTF8)
    if ($bridgePid -match '^[0-9]+$' -and $bridgePath) {
        $process = Get-Process -Id $bridgePid -ErrorAction SilentlyContinue
        if ($process -and $process.Path -eq $bridgePath) {
            Write-RunnerLog "Stopping the bridge left by an earlier runner (pid $bridgePid)."
            Stop-Process -InputObject $process -Force -ErrorAction SilentlyContinue
            [void]$process.WaitForExit(10000)
        }
    }
    Remove-Item -LiteralPath $pidFile -Force
}

Stop-OrphanedBridge
while ($true) {
    $out = $null
    $err = $null
    try {
        # Recreated on every run in case the logs were cleaned up by hand.
        New-Item -ItemType Directory -Force -Path $config.LogDir | Out-Null
        $out = Open-Log 'bridge.out.log'
        $err = Open-Log 'bridge.err.log'
        $info = [System.Diagnostics.ProcessStartInfo]::new($config.BridgeBinary)
        $info.WorkingDirectory = $config.BridgeDir
        $info.UseShellExecute = $false
        $info.CreateNoWindow = $true
        $info.RedirectStandardOutput = $true
        $info.RedirectStandardError = $true
        $bridge = [System.Diagnostics.Process]::Start($info)
        [System.IO.File]::WriteAllLines($pidFile, [string[]]@($bridge.Id, $config.BridgeBinary))
        Write-RunnerLog "Started bridge (pid $($bridge.Id))."
        $copies = [System.Threading.Tasks.Task[]]@(
            $bridge.StandardOutput.BaseStream.CopyToAsync($out),
            $bridge.StandardError.BaseStream.CopyToAsync($err)
        )
        $bridge.WaitForExit()
        [void][System.Threading.Tasks.Task]::WaitAll($copies, 5000)
        Write-RunnerLog "Bridge exited with code $($bridge.ExitCode)."
    } catch {
        Write-RunnerLog "Could not run the bridge: $($_.Exception.Message)"
    } finally {
        Remove-Item -LiteralPath $pidFile -Force -ErrorAction SilentlyContinue
        if ($out) { $out.Dispose() }
        if ($err) { $err.Dispose() }
    }
    Write-RunnerLog "Restarting in $($config.RestartDelaySeconds) seconds."
    Start-Sleep -Seconds $config.RestartDelaySeconds
}
'@
[System.IO.File]::WriteAllText($RunnerScript, $runner, $utf8)

$powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$action = New-ScheduledTaskAction -Execute $powershell -WorkingDirectory $BridgeDir `
    -Argument "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$RunnerScript`""
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $Identity.Name
$principal = New-ScheduledTaskPrincipal -UserId $Identity.Name -LogonType Interactive -RunLevel Limited
# The runner restarts the bridge 10 seconds after it exits, like launchd's
# KeepAlive and ThrottleInterval; Task Scheduler's own restart setting cannot
# retry sooner than once a minute. The runner is meant to run for the whole
# session, so the task must never hit the default 72-hour time limit or refuse
# to start on battery power.
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew

Write-Host 'Registering the whatsapp-mcp Task Scheduler task...'
Register-ScheduledTask @Task -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force `
    -Description 'Starts the WhatsApp MCP bridge at login and restarts it when it exits. Installed by scripts/install-task-windows.ps1.' | Out-Null
Start-ScheduledTask @Task

Write-Host "Installed Task Scheduler task: $($Task.TaskName)"
Write-Host "Logs: $LogDir"
