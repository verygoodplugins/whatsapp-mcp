@echo off
:: ============================================================
:: WPP BRIDGE — INÍCIO NORMAL (após já ter pareado)
:: Use este após o primeiro pareamento com histórico completo
:: ============================================================
cd /d "%~dp0"
echo [%date% %time%] Iniciando WhatsApp Bridge... >> bridge.log
whatsapp-bridge.exe
echo [%date% %time%] Bridge encerrado. >> bridge.log
