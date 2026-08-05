# Live WhatsApp pairing watcher (Windows).
#
# WhatsApp pairing QR codes rotate every 20-60 seconds, so a static QR
# (screenshot, mis-rendered terminal, etc.) is usually stale by the time
# it is scanned. This script runs the bridge, converts each rotating code
# to an SVG, and keeps an auto-refreshing pair.html updated so the browser
# always shows a scannable, current code.
#
# Usage:  powershell -ExecutionPolicy Bypass -File pair-live.ps1
#         then open the pair.html path it prints (or use pair-whatsapp.cmd).
#
# Requires: a built whatsapp-bridge.exe (cd whatsapp-bridge; go build -o
# whatsapp-bridge.exe .) and Python with the `qrcode` package installed.

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Split-Path -Parent (Split-Path -Parent $scriptDir)
$bridgeDir = Join-Path $repoRoot "whatsapp-bridge"
$bridgeExe = Join-Path $bridgeDir "whatsapp-bridge.exe"
$dir = Join-Path $scriptDir "out"
New-Item -ItemType Directory -Force $dir | Out-Null

$log = Join-Path $dir "bridge.log"
$page = Join-Path $dir "pair.html"
$svgPath = Join-Path $dir "qr.svg"
$dbg = Join-Path $dir "watch.log"
$pythonExe = (Get-Command python -ErrorAction SilentlyContinue).Source
if (-not $pythonExe) { Write-Error "python not found on PATH (needed for QR rendering)"; exit 1 }
if (-not (Test-Path $bridgeExe)) { Write-Error "bridge not built: $bridgeExe (run: cd whatsapp-bridge; go build -o whatsapp-bridge.exe .)"; exit 1 }
Set-Content $dbg "watcher start $(Get-Date -Format HH:mm:ss)"

function Write-Page($status, $detail, $svg, $refresh) {
    $meta = ""
    if ($refresh) { $meta = '<meta http-equiv="refresh" content="2">' }
    $html = @"
<!doctype html><html><head><meta charset="utf-8">$meta<title>WhatsApp Pairing</title>
<style>body{font-family:Segoe UI,Arial,sans-serif;display:flex;flex-direction:column;align-items:center;justify-content:center;min-height:95vh;background:#111b21;color:#e9edef;margin:0}
h1{font-size:22px;margin:12px 0 4px}p{color:#8696a0;margin:4px 0 16px;font-size:15px}
.qr{background:#fff;padding:12px;border-radius:12px}.ok{color:#25d366;font-size:44px}</style></head>
<body><h1>$status</h1><p>$detail</p><div class="qr">$svg</div></body></html>
"@
    $tmp = "$page.tmp"
    Set-Content -Path $tmp -Value $html -Encoding utf8
    Move-Item -Force $tmp $page
}

Write-Page "Starting WhatsApp bridge..." "A QR code will appear here in a few seconds. Keep this page open." "" $true
Write-Output "Pairing page: $page"

$overallDeadline = (Get-Date).AddMinutes(30)
$lastCode = ""

for ($round = 1; $round -le 5; $round++) {
    taskkill /im whatsapp-bridge.exe /f 2>$null | Out-Null
    Start-Sleep -Seconds 1
    Remove-Item $log -Force -ErrorAction SilentlyContinue
    $proc = Start-Process -FilePath $bridgeExe -WorkingDirectory $bridgeDir `
        -RedirectStandardOutput $log -RedirectStandardError (Join-Path $dir "bridge.err.log") `
        -WindowStyle Hidden -PassThru

    while (-not $proc.HasExited -and (Get-Date) -lt $overallDeadline) {
        Start-Sleep -Seconds 2
        $text = ""
        try {
            # The redirect target is write-locked by Start-Process; a plain
            # ReadAllText requests a share mode that conflicts with it.
            $fs = New-Object System.IO.FileStream($log, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
            $sr = New-Object System.IO.StreamReader($fs)
            $text = $sr.ReadToEnd()
            $sr.Close()
        } catch { Add-Content $dbg "readfail: $_" }
        if (-not $text) { continue }

        if ($text -match "Successfully connected and authenticated!") {
            Write-Page "&#10003; Paired successfully!" "WhatsApp is now linked. You can close this page." '<span class="ok">&#10003;</span>' $false
            exit 0
        }

        $codes = [regex]::Matches($text, "Emitting QR code (\S+)")
        if ($codes.Count -gt 0) {
            $latest = $codes[$codes.Count - 1].Groups[1].Value
            if ($latest -ne $lastCode) {
                try {
                    & $pythonExe (Join-Path $scriptDir "payload2svg.py") $log $svgPath >> $dbg 2>&1
                } catch { Add-Content $dbg "pyfail: $_" }
                $svg = ""
                try { $svg = [System.IO.File]::ReadAllText($svgPath) } catch { Add-Content $dbg "svgread: $_" }
                if ($svg -and $svg.StartsWith("<svg")) {
                    $lastCode = $latest
                    Write-Page "Scan with WhatsApp" "Phone: WhatsApp &gt; Settings &gt; Linked Devices &gt; Link a Device. This code refreshes automatically &mdash; scan any time." $svg $true
                    Add-Content $dbg "page updated $(Get-Date -Format HH:mm:ss)"
                }
            }
        }
    }
    if ((Get-Date) -ge $overallDeadline) { break }
    Write-Page "Getting a fresh code..." "The pairing window is restarting. A new QR appears shortly." "" $true
}

Write-Page "Pairing window closed" "Thirty minutes passed without a scan. Run pair-whatsapp.cmd to try again." "" $false
