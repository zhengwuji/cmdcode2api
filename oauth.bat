@echo off
chcp 65001 >nul
cd /d "%~dp0"
title cmdcode2api OAuth Helper

echo ==================================================
echo   启动 Command Code 浏览器 OAuth 授权绑定
echo ==================================================
"%~dp0cmdcode2api.exe" --oauth %*
echo.
pause
