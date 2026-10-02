param ()

[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$ErrorActionPreference = "SilentlyContinue"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $ScriptDir

$PidFile = Join-Path $ScriptDir "cmdcode2api.pid"
$stopped = $false

# 1. 尝试通过 PID 文件精准终止
if (Test-Path $PidFile) {
    $existingPid = (Get-Content $PidFile -ErrorAction SilentlyContinue | Out-String).Trim()
    if ($existingPid -match '^\d+$') {
        $p = Get-Process -Id ([int]$existingPid) -ErrorAction SilentlyContinue
        if ($p -and $p.ProcessName -eq "cmdcode2api") {
            Write-Host "[INFO] 正在停止 cmdcode2api (PID: $existingPid)..." -ForegroundColor Cyan
            Stop-Process -Id $p.Id -Force -ErrorAction SilentlyContinue
            $stopped = $true
        }
    }
    Remove-Item -Path $PidFile -Force -ErrorAction SilentlyContinue
}

# 2. 终止残留的 cmdcode2api 进程
$remaining = Get-Process -Name "cmdcode2api" -ErrorAction SilentlyContinue
if ($remaining) {
    foreach ($proc in $remaining) {
        Write-Host "[INFO] 正在终止 cmdcode2api 进程 (PID: $($proc.Id))..." -ForegroundColor Cyan
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        $stopped = $true
    }
}

# 3. 兜底使用 taskkill 确保完全退出
& taskkill.exe /F /IM cmdcode2api.exe /T 2>$null | Out-Null

Start-Sleep -Milliseconds 400

$check = Get-Process -Name "cmdcode2api" -ErrorAction SilentlyContinue
if (-not $check) {
    if ($stopped) {
        Write-Host "[OK] cmdcode2api 服务已成功停止。" -ForegroundColor Green
    } else {
        Write-Host "[INFO] 未发现正在运行的 cmdcode2api 服务。" -ForegroundColor Yellow
    }
} else {
    Write-Host "[ERROR] 停止服务失败，请手动打开任务管理器检查 cmdcode2api.exe。" -ForegroundColor Red
    exit 1
}
