@echo off
:: Start TypeAnything daemon
start /B "" "%~dp0ta-daemon.exe" -pipe "\\.\pipe\typeanything-ime" -rime "%APPDATA%\Rime" -dict "%APPDATA%\Rime\wubi86.dict.yaml"
echo ta-daemon launched (PID: %ERRORLEVEL%)
