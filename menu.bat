@echo off
chcp 65001 >nul
cd /d "%~dp0"
title cmdcode2api 管理控制台

:menu
cls
echo ========================================================
echo               cmdcode2api 服务管理控制台
echo ========================================================
echo  [1] 启动服务 (后台静默模式)
echo  [2] 启动服务 (前台窗口模式)
echo  [3] 停止服务
echo  [4] 重启服务
echo  [5] 查看服务运行状态与日志
echo  [6] 打开 WebUI 管理界面 (浏览器)
echo  [7] 绑定 Command Code 账号 (OAuth 授权)
echo  [0] 退出
echo ========================================================
set /p choice=请输入操作编号 [0-7]: 

if "%choice%"=="1" (
    echo.
    call "%~dp0start.bat"
    echo.
    pause
    goto menu
)
if "%choice%"=="2" (
    echo.
    call "%~dp0start-console.bat"
    echo.
    pause
    goto menu
)
if "%choice%"=="3" (
    echo.
    call "%~dp0stop.bat"
    echo.
    pause
    goto menu
)
if "%choice%"=="4" (
    echo.
    call "%~dp0restart.bat"
    echo.
    pause
    goto menu
)
if "%choice%"=="5" (
    echo.
    call "%~dp0status.bat"
    echo.
    pause
    goto menu
)
if "%choice%"=="6" (
    echo.
    echo 正在打开浏览器访问 http://127.0.0.1:11434/webui ...
    start "" http://127.0.0.1:11434/webui
    goto menu
)
if "%choice%"=="7" (
    echo.
    call "%~dp0oauth.bat"
    goto menu
)
if "%choice%"=="0" (
    exit /b 0
)

echo 无效选项，请重新输入。
timeout /t 1 >nul
goto menu
