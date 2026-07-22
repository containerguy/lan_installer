@echo off
setlocal
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0Start-LANReady.ps1" -ServerUrl "https://game-manager.familie-keller.info" -EventId "demo" %*
set "LANREADY_EXIT=%ERRORLEVEL%"
echo.
echo LANReady wurde mit Rueckgabecode %LANREADY_EXIT% beendet.
pause
exit /b %LANREADY_EXIT%
