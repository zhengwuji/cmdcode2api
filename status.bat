@echo off
chcp 65001 >nul
cd /d "%~dp0"
title cmdcode2api Status

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0status.ps1" %*
