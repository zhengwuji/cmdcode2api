@echo off
chcp 65001 >nul
cd /d "%~dp0"
title cmdcode2api (Console Mode)

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0start.ps1" -Console %*
