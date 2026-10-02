@echo off
chcp 65001 >nul
cd /d "%~dp0"
title cmdcode2api

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0start.ps1" %*
if errorlevel 1 (
    echo.
    echo [ERROR] 启动失败，请检查上方提示。
    pause
)
