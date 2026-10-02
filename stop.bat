@echo off
chcp 65001 >nul
cd /d "%~dp0"
title cmdcode2api Stopper

powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0stop.ps1" %*
