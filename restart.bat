@echo off
chcp 65001 >nul
cd /d "%~dp0"
title cmdcode2api Restart

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0restart.ps1" %*
if errorlevel 1 (
    echo.
    echo [ERROR] 重启遇到错误。
    pause
)
