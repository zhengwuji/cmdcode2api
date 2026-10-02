param ()

[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $ScriptDir

$PidFile = Join-Path $ScriptDir "cmdcode2api.pid"
$LogFile = Join-Path $ScriptDir "cmdcode2api.log"

# 服务默认只监听 127.0.0.1；用 localhost 时 .NET 会先尝试 ::1，连接失败后才回退到
# IPv4，整个探测会稳定耗时约 2 秒。配合 -TimeoutSec 2 正好卡在边界上，于是服务明明
# 正常也报“端口暂未响应”。统一改用 127.0.0.1 并给足超时。
$BaseUrl = "http://127.0.0.1:11434"

$processes = Get-Process -Name "cmdcode2api" -ErrorAction SilentlyContinue

Write-Host "==================================================" -ForegroundColor Cyan
Write-Host "            cmdcode2api 服务状态" -ForegroundColor Cyan
Write-Host "==================================================" -ForegroundColor Cyan

if ($processes) {
    Write-Host " 运行状态: [ 运行中 / RUNNING ]" -ForegroundColor Green
    foreach ($p in $processes) {
        $memMB = [math]::Round($p.WorkingSet64 / 1MB, 2)
        Write-Host " 进程 PID: $($p.Id)" -ForegroundColor White
        Write-Host " 内存占用: ${memMB} MB" -ForegroundColor White
        try {
            Write-Host " 启动时间: $($p.StartTime)" -ForegroundColor White
        } catch {}
    }
    Write-Host " 服务地址: $BaseUrl" -ForegroundColor White
    Write-Host " WebUI 管理: $BaseUrl/webui" -ForegroundColor White
    
    # 探测 health 接口
    try {
        $resp = Invoke-RestMethod -Uri "$BaseUrl/health" -TimeoutSec 5 -ErrorAction Stop
        Write-Host " 接口健康: 正常 (status: $($resp.status))" -ForegroundColor Green
    } catch {
        Write-Host " 接口健康: 端口暂未响应或在初始化中" -ForegroundColor Yellow
    }
} else {
    Write-Host " 运行状态: [ 已停止 / STOPPED ]" -ForegroundColor Red
}

if (Test-Path $LogFile) {
    Write-Host "--------------------------------------------------" -ForegroundColor DarkGray
    Write-Host " 最近运行日志 (最新 10 行):" -ForegroundColor DarkGray
    Get-Content $LogFile -Tail 10 | ForEach-Object { Write-Host "   $_" -ForegroundColor Gray }
}
Write-Host "==================================================" -ForegroundColor Cyan
