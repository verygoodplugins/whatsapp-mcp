$ErrorActionPreference = 'Stop'

$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$TaskName = 'whatsapp-mcp-bridge'
$PassThroughVars = @(
    'WHATSAPP_BRIDGE_PORT', 'WEBHOOK_URL', 'WEBHOOK_ENABLED', 'WHATSAPP_AUTO_DOWNLOAD_MEDIA',
    'FORWARD_SELF', 'WHATSAPP_BRIDGE_TOKEN', 'WHATSAPP_MEDIA_ROOTS', 'WHATSAPP_DEVICE_NAME'
)

$script:Failures = 0
$script:Fixtures = [System.Collections.Generic.List[string]]::new()

function Fail([string]$Message) {
    Write-Host "FAIL: $Message"
    $script:Failures++
}

function Assert-File([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { Fail "expected file: $Path" }
}

function Assert-NotExists([string]$Path) {
    if (Test-Path -LiteralPath $Path) { Fail "expected path to be removed: $Path" }
}

function Assert-Equal($Expected, $Actual, [string]$What) {
    if ($Expected -ne $Actual) { Fail "expected $What to be '$Expected', got '$Actual'" }
}

function Assert-Contains([string]$Haystack, [string]$Needle, [string]$What) {
    if (-not $Haystack.Contains($Needle)) { Fail "expected $What to contain: $Needle" }
}

# Load the real modules before defining the stand-ins: autoloading a module
# later would import its functions over the stand-ins and reach the real Task
# Scheduler.
Import-Module ScheduledTasks, NetTCPIP

$global:FakeCalls = [System.Collections.Generic.List[string]]::new()
$global:FakeRegistered = $null
$global:FakeCgoEnabled = '1'
$global:FakeListener = $null
$global:FakeBridgeExe = $null

function global:go {
    $global:FakeCalls.Add("go $($args -join ' ')")
    $global:LASTEXITCODE = 0
    if ($args[0] -eq 'env' -and $args[1] -eq 'CGO_ENABLED') { return $global:FakeCgoEnabled }
    if ($args[0] -eq 'env' -and $args[1] -eq 'CC') { return 'fake-gcc' }
    if ($args[0] -eq 'build') {
        Copy-Item -LiteralPath $global:FakeBridgeExe -Destination $args[[array]::IndexOf($args, '-o') + 1]
    }
}

function global:fake-gcc {}

function global:Get-ScheduledTask {
    [CmdletBinding()]
    param([string]$TaskName, [string]$TaskPath)
    if ($global:FakeRegistered) { [pscustomobject]@{ TaskName = $TaskName; State = 'Ready' } }
}

function global:Register-ScheduledTask {
    [CmdletBinding()]
    param([string]$TaskName, [string]$TaskPath, $Action, $Trigger, $Settings, $Principal, [string]$Description, [switch]$Force)
    $global:FakeCalls.Add("Register-ScheduledTask $TaskPath$TaskName")
    $global:FakeRegistered = [pscustomobject]$PSBoundParameters
}

function global:Unregister-ScheduledTask {
    [CmdletBinding()]
    param([string]$TaskName, [string]$TaskPath, [switch]$Confirm)
    $global:FakeCalls.Add("Unregister-ScheduledTask $TaskPath$TaskName")
    $global:FakeRegistered = $null
}

function global:Start-ScheduledTask {
    [CmdletBinding()]
    param([string]$TaskName, [string]$TaskPath)
    $global:FakeCalls.Add("Start-ScheduledTask $TaskPath$TaskName")
}

function global:Stop-ScheduledTask {
    [CmdletBinding()]
    param([string]$TaskName, [string]$TaskPath)
    $global:FakeCalls.Add("Stop-ScheduledTask $TaskPath$TaskName")
}

function global:Get-NetTCPConnection {
    [CmdletBinding()]
    param([string]$State, [int]$LocalPort)
    $global:FakeCalls.Add("Get-NetTCPConnection $LocalPort")
    if ($global:FakeListener) { $global:FakeListener }
}

foreach ($name in 'Get-ScheduledTask', 'Register-ScheduledTask', 'Unregister-ScheduledTask', 'Start-ScheduledTask', 'Stop-ScheduledTask', 'Get-NetTCPConnection') {
    if ((Get-Command $name).Module) { throw "The stand-in for $name is not active; refusing to touch the real system." }
}

function New-FakeBridgeExe([string]$Path) {
    # A real console executable, so the runner starts a real process.
    # csc.exe ships with the .NET Framework on every supported Windows version.
    $source = [System.IO.Path]::ChangeExtension($Path, '.cs')
    Set-Content -LiteralPath $source -Encoding ASCII -Value @'
using System;
using System.IO;
using System.Text;
using System.Threading;

class FakeBridge {
    static int Main() {
        // Writes UTF-8 like the Go bridge, not the console code page, so
        // non-ASCII paths in the output match.
        Console.SetOut(new StreamWriter(Console.OpenStandardOutput(), new UTF8Encoding(false)) { AutoFlush = true });
        Console.SetError(new StreamWriter(Console.OpenStandardError(), new UTF8Encoding(false)) { AutoFlush = true });
        Console.Out.WriteLine("fake bridge stdout");
        Console.Out.WriteLine("cwd=" + Environment.CurrentDirectory);
        Console.Out.WriteLine("port=" + Environment.GetEnvironmentVariable("WHATSAPP_BRIDGE_PORT"));
        Console.Error.WriteLine("fake bridge stderr");
        int sleepMs;
        if (int.TryParse(Environment.GetEnvironmentVariable("FAKE_BRIDGE_SLEEP_MS"), out sleepMs)) {
            Thread.Sleep(sleepMs);
        }
        int exitCode;
        return int.TryParse(Environment.GetEnvironmentVariable("FAKE_BRIDGE_EXIT_CODE"), out exitCode) ? exitCode : 1;
    }
}
'@
    $csc = Join-Path $env:WINDIR 'Microsoft.NET\Framework64\v4.0.30319\csc.exe'
    if (-not (Test-Path -LiteralPath $csc)) { $csc = Join-Path $env:WINDIR 'Microsoft.NET\Framework\v4.0.30319\csc.exe' }
    & $csc /nologo /target:exe "/out:$Path" $source | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "csc.exe could not build the fake bridge." }
}

function New-Fixture {
    $root = Join-Path ([System.IO.Path]::GetTempPath()) ('whatsapp-mcp-task-test-' + [guid]::NewGuid().ToString('N').Substring(0, 8))
    foreach ($dir in "$root\repo\scripts", "$root\repo\whatsapp-bridge\store", "$root\localappdata") {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }
    Copy-Item -Path "$RepoRoot\scripts\*-task-windows.ps1" -Destination "$root\repo\scripts\"
    $global:FakeCalls.Clear()
    $global:FakeRegistered = $null
    $global:FakeCgoEnabled = '1'
    $global:FakeListener = $null
    $script:Fixtures.Add($root)
    return $root
}

# Setting a variable to $null through .NET leaves it defined but empty in
# PowerShell 7, so unset variables are removed through the Env: drive.
function Set-ProcessEnv([string]$Name, $Value) {
    if ($null -eq $Value) { Remove-Item -LiteralPath "Env:$Name" -ErrorAction SilentlyContinue }
    else { Set-Item -LiteralPath "Env:$Name" -Value $Value }
}

function Invoke-TaskScript([string]$Root, [string]$Name, [hashtable]$Environment = @{}) {
    $names = @('LOCALAPPDATA') + $PassThroughVars
    $saved = @{}
    foreach ($n in $names) { $saved[$n] = [Environment]::GetEnvironmentVariable($n, 'Process') }
    try {
        foreach ($n in $PassThroughVars) { Set-ProcessEnv $n $null }
        Set-ProcessEnv 'LOCALAPPDATA' "$Root\localappdata"
        foreach ($n in $Environment.Keys) { Set-ProcessEnv $n $Environment[$n] }
        $global:LASTEXITCODE = 0
        $output = & "$Root\repo\scripts\$Name" *>&1 | Out-String
        return [pscustomobject]@{ ExitCode = $LASTEXITCODE; Output = $output }
    } finally {
        foreach ($n in $names) { Set-ProcessEnv $n $saved[$n] }
    }
}

function Read-TaskConfig([string]$Root) {
    [System.IO.File]::ReadAllText("$Root\localappdata\whatsapp-mcp\bridge-task.json") | ConvertFrom-Json
}

function Set-RestartDelay([string]$Root, [int]$Seconds = 1) {
    $config = Read-TaskConfig $Root
    $config.RestartDelaySeconds = $Seconds
    [System.IO.File]::WriteAllText("$Root\localappdata\whatsapp-mcp\bridge-task.json", ($config | ConvertTo-Json))
}

# Starts the launcher the way the task does; it starts the runner, which starts
# the bridge.
function Start-Launcher([string]$Root, [hashtable]$Environment) {
    foreach ($n in $Environment.Keys) { Set-ProcessEnv $n $Environment[$n] }
    try {
        Start-Process -FilePath "$Root\localappdata\whatsapp-mcp\whatsapp-bridge-launcher.exe" -PassThru
    } finally {
        foreach ($n in $Environment.Keys) { Set-ProcessEnv $n $null }
    }
}

# Reads the Subsystem field of a PE file's optional header, which follows the
# 4-byte signature and the 20-byte file header. The field is at the same
# offset in 32-bit and 64-bit images.
function Get-PeSubsystem([string]$Path) {
    $bytes = [System.IO.File]::ReadAllBytes($Path)
    $peOffset = [BitConverter]::ToInt32($bytes, 0x3C)
    if ([System.Text.Encoding]::ASCII.GetString($bytes, $peOffset, 4) -ne "PE`0`0") { throw "$Path is not a PE file" }
    return [BitConverter]::ToUInt16($bytes, $peOffset + 24 + 68)
}

function Wait-Until([scriptblock]$Condition, [int]$TimeoutSeconds = 30) {
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        if (& $Condition) { return $true }
        Start-Sleep -Milliseconds 200
    }
    return $false
}

function Get-MatchCount([string]$Path, [string]$Pattern) {
    if (-not (Test-Path -LiteralPath $Path)) { return 0 }
    return [regex]::Matches([System.IO.File]::ReadAllText($Path), $Pattern).Count
}

function Test-InstallRegistersLogonTask {
    $root = New-Fixture
    $result = Invoke-TaskScript $root 'install-task-windows.ps1'
    Assert-Equal 0 $result.ExitCode "installer exit code ($($result.Output))"

    $app = "$root\localappdata\whatsapp-mcp"
    $bridgeDir = "$root\repo\whatsapp-bridge"
    $launcher = "$app\whatsapp-bridge-launcher.exe"
    $userId = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    Assert-File "$app\run-whatsapp-bridge.ps1"
    Assert-File $launcher
    Assert-NotExists "$app\whatsapp-bridge-launcher.new.exe"
    Assert-NotExists "$app\whatsapp-bridge-launcher.cs"
    Assert-File "$bridgeDir\whatsapp-bridge.exe"
    Assert-NotExists "$bridgeDir\whatsapp-bridge.new.exe"
    # IMAGE_SUBSYSTEM_WINDOWS_GUI: Windows never gives the launcher a console
    # window.
    Assert-Equal 2 (Get-PeSubsystem $launcher) 'launcher PE subsystem'

    $config = Read-TaskConfig $root
    Assert-Equal $bridgeDir $config.BridgeDir 'BridgeDir'
    Assert-Equal "$bridgeDir\whatsapp-bridge.exe" $config.BridgeBinary 'BridgeBinary'
    Assert-Equal "$app\logs" $config.LogDir 'LogDir'
    Assert-Equal '8080' $config.Environment.WHATSAPP_BRIDGE_PORT 'WHATSAPP_BRIDGE_PORT'
    Assert-Equal $null $config.Environment.WHATSAPP_BRIDGE_TOKEN 'WHATSAPP_BRIDGE_TOKEN without an explicit token'

    $task = $global:FakeRegistered
    if (-not $task) { Fail 'expected the task to be registered'; return }
    Assert-Equal $TaskName $task.TaskName 'task name'
    Assert-Equal $launcher $task.Action.Execute 'action executable'
    Assert-Equal $bridgeDir $task.Action.WorkingDirectory 'action working directory'
    Assert-Equal 'MSFT_TaskLogonTrigger' $task.Trigger.CimClass.CimClassName 'trigger type'
    Assert-Equal $userId $task.Trigger.UserId 'trigger user'
    Assert-Equal $userId $task.Principal.UserId 'principal user'
    Assert-Equal 'Interactive' "$($task.Principal.LogonType)" 'logon type'
    Assert-Equal 'Limited' "$($task.Principal.RunLevel)" 'run level'
    Assert-Equal 'PT0S' $task.Settings.ExecutionTimeLimit 'execution time limit'
    Assert-Equal $false $task.Settings.DisallowStartIfOnBatteries 'DisallowStartIfOnBatteries'
    Assert-Equal $false $task.Settings.StopIfGoingOnBatteries 'StopIfGoingOnBatteries'
    Assert-Equal 'IgnoreNew' "$($task.Settings.MultipleInstances)" 'MultipleInstances'

    $calls = $global:FakeCalls -join "`n"
    Assert-Contains $calls "go build -o $bridgeDir\whatsapp-bridge.new.exe ." 'commands'
    Assert-Contains $calls "Start-ScheduledTask \$TaskName" 'commands'
}

function Test-InstallPreservesOptionalEnvValues {
    $root = New-Fixture
    $values = @{
        WHATSAPP_BRIDGE_PORT         = '9090'
        WEBHOOK_URL                  = 'http://127.0.0.1:8769/whatsapp/webhook'
        WEBHOOK_ENABLED              = 'false'
        WHATSAPP_AUTO_DOWNLOAD_MEDIA = 'false'
        FORWARD_SELF                 = 'true'
        WHATSAPP_BRIDGE_TOKEN        = "test token with spaces, 'quotes' and `"double quotes`""
        WHATSAPP_MEDIA_ROOTS         = 'C:\outbox;C:\other outbox'
        WHATSAPP_DEVICE_NAME         = 'Work laptop'
    }
    $result = Invoke-TaskScript $root 'install-task-windows.ps1' $values
    Assert-Equal 0 $result.ExitCode "installer exit code ($($result.Output))"

    $config = Read-TaskConfig $root
    foreach ($name in $values.Keys) {
        Assert-Equal $values[$name] $config.Environment.$name $name
    }
}

function Test-ReinstallKeepsSavedSettings {
    $root = New-Fixture
    $values = @{
        WHATSAPP_BRIDGE_PORT  = '9090'
        WEBHOOK_URL           = 'http://127.0.0.1:8769/whatsapp/webhook'
        WHATSAPP_BRIDGE_TOKEN = 'saved-token'
    }
    $result = Invoke-TaskScript $root 'install-task-windows.ps1' $values
    Assert-Equal 0 $result.ExitCode "installer exit code ($($result.Output))"

    $global:FakeCalls.Clear()
    $result = Invoke-TaskScript $root 'install-task-windows.ps1' @{ WEBHOOK_URL = 'http://127.0.0.1:9999/hook' }
    Assert-Equal 0 $result.ExitCode "reinstall exit code ($($result.Output))"
    $config = Read-TaskConfig $root
    Assert-Equal '9090' $config.Environment.WHATSAPP_BRIDGE_PORT 'saved WHATSAPP_BRIDGE_PORT'
    Assert-Equal 'saved-token' $config.Environment.WHATSAPP_BRIDGE_TOKEN 'saved WHATSAPP_BRIDGE_TOKEN'
    Assert-Equal 'http://127.0.0.1:9999/hook' $config.Environment.WEBHOOK_URL 'overridden WEBHOOK_URL'
    Assert-Contains ($global:FakeCalls -join "`n") 'Get-NetTCPConnection 9090' 'commands'

    # Uninstalling forgets the saved settings.
    Assert-Equal 0 (Invoke-TaskScript $root 'uninstall-task-windows.ps1').ExitCode 'uninstaller exit code'
    $result = Invoke-TaskScript $root 'install-task-windows.ps1'
    Assert-Equal 0 $result.ExitCode "install after uninstall exit code ($($result.Output))"
    $config = Read-TaskConfig $root
    Assert-Equal '8080' $config.Environment.WHATSAPP_BRIDGE_PORT 'WHATSAPP_BRIDGE_PORT after uninstall'
    Assert-Equal $null $config.Environment.WEBHOOK_URL 'WEBHOOK_URL after uninstall'
}

function Test-InstallRestrictsConfigToCurrentUser {
    $root = New-Fixture
    $result = Invoke-TaskScript $root 'install-task-windows.ps1' @{ WHATSAPP_BRIDGE_TOKEN = 'secret-token' }
    Assert-Equal 0 $result.ExitCode "installer exit code ($($result.Output))"

    $acl = Get-Acl -LiteralPath "$root\localappdata\whatsapp-mcp\bridge-task.json"
    $rules = @($acl.GetAccessRules($true, $true, [System.Security.Principal.SecurityIdentifier]))
    $userSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User
    Assert-Equal $true $acl.AreAccessRulesProtected 'inheritance disabled on bridge-task.json'
    Assert-Equal 1 $rules.Count 'number of access rules on bridge-task.json'
    Assert-Equal $userSid.Value $rules[0].IdentityReference.Value 'the only account with access to bridge-task.json'
}

function Test-InstallUsesExistingBinaryWithoutCgo {
    $root = New-Fixture
    $global:FakeCgoEnabled = '0'
    Copy-Item -LiteralPath $global:FakeBridgeExe -Destination "$root\repo\whatsapp-bridge\whatsapp-bridge.exe"
    $result = Invoke-TaskScript $root 'install-task-windows.ps1'
    Assert-Equal 0 $result.ExitCode "installer exit code ($($result.Output))"
    if ($global:FakeCalls -match '^go build') { Fail 'expected no go build without cgo' }
    if (-not $global:FakeRegistered) { Fail 'expected the task to be registered' }
}

function Test-InstallFailsWithoutCgoOrBinary {
    $root = New-Fixture
    $global:FakeCgoEnabled = '0'
    $result = Invoke-TaskScript $root 'install-task-windows.ps1'
    Assert-Equal 1 $result.ExitCode 'installer exit code'
    Assert-Contains $result.Output 'no bridge binary exists' 'installer output'
    if ($global:FakeRegistered) { Fail 'expected no task to be registered' }
}

function Test-InstallRefusesBusyPort {
    $root = New-Fixture
    $global:FakeListener = [pscustomobject]@{ LocalAddress = '127.0.0.1'; LocalPort = 8080; OwningProcess = $PID }
    $result = Invoke-TaskScript $root 'install-task-windows.ps1'
    Assert-Equal 1 $result.ExitCode 'installer exit code'
    Assert-Contains $result.Output 'Port 8080 is already in use' 'installer output'
    Assert-Contains $result.Output "pid $PID" 'installer output'
    if ($global:FakeRegistered) { Fail 'expected no task to be registered' }
}

function Test-RunnerRestartsBridgeAndAppendsLogs {
    $root = New-Fixture
    $result = Invoke-TaskScript $root 'install-task-windows.ps1'
    Assert-Equal 0 $result.ExitCode "installer exit code ($($result.Output))"
    Set-RestartDelay $root

    $logs = "$root\localappdata\whatsapp-mcp\logs"
    $launcher = Start-Launcher $root @{ FAKE_BRIDGE_EXIT_CODE = '3' }
    try {
        $restarted = Wait-Until { (Get-MatchCount "$logs\runner.log" 'Bridge exited with code 3') -ge 2 }
    } finally {
        Stop-Process -Id $launcher.Id -Force -ErrorAction SilentlyContinue
    }
    if (-not $restarted) { Fail 'expected the runner to start the bridge again after it exited'; return }

    if ((Get-MatchCount "$logs\bridge.out.log" 'fake bridge stdout') -lt 2) { Fail 'expected bridge.out.log to keep the output of every run' }
    if ((Get-MatchCount "$logs\bridge.err.log" 'fake bridge stderr') -lt 2) { Fail 'expected bridge.err.log to keep the output of every run' }
    $out = [System.IO.File]::ReadAllText("$logs\bridge.out.log")
    Assert-Contains $out "cwd=$root\repo\whatsapp-bridge" 'bridge.out.log'
    Assert-Contains $out 'port=8080' 'bridge.out.log'
}

function Test-RunnerSurvivesLogFolderRemoval {
    $root = New-Fixture
    $result = Invoke-TaskScript $root 'install-task-windows.ps1'
    Assert-Equal 0 $result.ExitCode "installer exit code ($($result.Output))"
    Set-RestartDelay $root 3

    $logs = "$root\localappdata\whatsapp-mcp\logs"
    $launcher = Start-Launcher $root @{ FAKE_BRIDGE_EXIT_CODE = '3' }
    try {
        if (-not (Wait-Until { (Get-MatchCount "$logs\runner.log" 'Restarting in') -ge 1 })) {
            Fail 'expected the runner to wait before restarting the bridge'
            return
        }
        # Between runs, as someone cleaning up old logs by hand would.
        Remove-Item -LiteralPath $logs -Recurse -Force
        $recovered = Wait-Until { (Get-MatchCount "$logs\runner.log" 'Bridge exited with code 3') -ge 1 }
    } finally {
        Stop-Process -Id $launcher.Id -Force -ErrorAction SilentlyContinue
    }
    if (-not $recovered) { Fail 'expected the runner to keep restarting the bridge after the logs folder was removed' }
}

function Test-UninstallRemovesGeneratedFilesOnly {
    $root = New-Fixture
    $result = Invoke-TaskScript $root 'install-task-windows.ps1'
    Assert-Equal 0 $result.ExitCode "installer exit code ($($result.Output))"

    $app = "$root\localappdata\whatsapp-mcp"
    Set-Content -LiteralPath "$root\repo\whatsapp-bridge\store\messages.db" -Value 'db'
    Set-Content -LiteralPath "$root\repo\whatsapp-bridge\store\.bridge-token" -Value 'token'
    Set-Content -LiteralPath "$app\logs\bridge.out.log" -Value 'log'

    $result = Invoke-TaskScript $root 'uninstall-task-windows.ps1'
    Assert-Equal 0 $result.ExitCode "uninstaller exit code ($($result.Output))"
    if ($global:FakeRegistered) { Fail 'expected the task to be unregistered' }
    Assert-Contains ($global:FakeCalls -join "`n") "Unregister-ScheduledTask \$TaskName" 'commands'
    Assert-NotExists "$app\whatsapp-bridge-launcher.exe"
    Assert-NotExists "$app\run-whatsapp-bridge.ps1"
    Assert-NotExists "$app\bridge-task.json"
    Assert-File "$root\repo\whatsapp-bridge\store\messages.db"
    Assert-File "$root\repo\whatsapp-bridge\store\.bridge-token"
    Assert-File "$root\repo\whatsapp-bridge\whatsapp-bridge.exe"
    Assert-File "$app\logs\bridge.out.log"
}

# Task Scheduler ends only the launcher when the task is stopped; its job
# object must end the runner and the bridge with it.
function Test-KillingLauncherStopsRunnerAndBridge {
    $root = New-Fixture
    $result = Invoke-TaskScript $root 'install-task-windows.ps1'
    Assert-Equal 0 $result.ExitCode "installer exit code ($($result.Output))"

    $logs = "$root\localappdata\whatsapp-mcp\logs"
    $launcher = Start-Launcher $root @{ FAKE_BRIDGE_SLEEP_MS = '120000' }
    $children = @()
    try {
        if (-not (Wait-Until { (Get-MatchCount "$logs\runner.log" 'Started bridge') -ge 1 })) {
            Fail "expected the launcher to start the runner and the bridge ($([System.IO.File]::ReadAllText("$logs\runner.log")))"
            return
        }
        $runnerPid = (Get-CimInstance Win32_Process -Filter "ParentProcessId = $($launcher.Id) AND Name = 'powershell.exe'").ProcessId
        $bridgePid = [regex]::Match([System.IO.File]::ReadAllText("$logs\runner.log"), 'Started bridge \(pid ([0-9]+)\)').Groups[1].Value
        $children = @(Get-Process -Id $runnerPid, $bridgePid)
        Assert-Equal "$root\repo\whatsapp-bridge\whatsapp-bridge.exe" $children[1].Path 'bridge process path'

        Stop-Process -Id $launcher.Id -Force
        foreach ($child in $children) {
            if (-not $child.WaitForExit(10000)) { Fail "expected killing the launcher to stop $($child.Path) (pid $($child.Id))" }
        }
    } finally {
        foreach ($process in @($launcher) + $children) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
    }
}

$global:FakeBridgeExe = Join-Path ([System.IO.Path]::GetTempPath()) ('whatsapp-mcp-fake-bridge-' + [guid]::NewGuid().ToString('N').Substring(0, 8) + '.exe')
New-FakeBridgeExe $global:FakeBridgeExe

foreach ($test in @(
    'Test-InstallRegistersLogonTask',
    'Test-InstallPreservesOptionalEnvValues',
    'Test-ReinstallKeepsSavedSettings',
    'Test-InstallRestrictsConfigToCurrentUser',
    'Test-InstallUsesExistingBinaryWithoutCgo',
    'Test-InstallFailsWithoutCgoOrBinary',
    'Test-InstallRefusesBusyPort',
    'Test-RunnerRestartsBridgeAndAppendsLogs',
    'Test-RunnerSurvivesLogFolderRemoval',
    'Test-UninstallRemovesGeneratedFilesOnly',
    'Test-KillingLauncherStopsRunnerAndBridge'
)) {
    Write-Host "Running $test"
    try { & $test } catch { Fail "$test threw: $_" }
}

foreach ($path in @($script:Fixtures) + $global:FakeBridgeExe + [System.IO.Path]::ChangeExtension($global:FakeBridgeExe, '.cs')) {
    Remove-Item -LiteralPath $path -Recurse -Force -ErrorAction SilentlyContinue
}

if ($script:Failures -gt 0) {
    Write-Host "$($script:Failures) test failure(s)"
    exit 1
}

Write-Host 'All Windows Task Scheduler script tests passed'
