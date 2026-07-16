@echo off
rem Launches the live pairing watcher hidden and opens the auto-refreshing
rem QR page in the default browser.
set SCRIPT_DIR=%~dp0
start "" /min powershell -ExecutionPolicy Bypass -WindowStyle Hidden -File "%SCRIPT_DIR%pair-live.ps1"
timeout /t 4 /nobreak >nul
start "" "%SCRIPT_DIR%out\pair.html"
