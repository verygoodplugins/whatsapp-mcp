@echo off
:: ============================================================
:: WPP BRIDGE — INÍCIO COM HISTÓRICO COMPLETO
:: Usa --full-history-pair para pedir até 3 anos de mensagens
:: Execute UMA VEZ após deletar whatsapp.db
:: ============================================================
cd /d "%~dp0"
echo.
echo ================================================
echo  WHATSAPP BRIDGE — MODO HISTÓRICO COMPLETO
echo ================================================
echo.
echo  Este modo vai pedir ao WhatsApp o maximo de
echo  historico possivel (ate 3 anos de mensagens).
echo.
echo  IMPORTANTE: Voce precisara escanear o QR Code
echo  com o seu celular para conectar.
echo.
echo  Passos:
echo  1. Abra o WhatsApp no celular
echo  2. Menu (3 pontos) > Aparelhos Conectados
echo  3. Clique em "Conectar aparelho"
echo  4. Escaneie o QR Code que aparecer aqui
echo.
echo  Aguardando... (o sync pode levar algumas horas)
echo ================================================
echo.
whatsapp-bridge.exe --full-history-pair
pause
