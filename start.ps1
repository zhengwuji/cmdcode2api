param (
    [switch]$Console,
    [string]$HostName = "",
    [int]$Port = 0,
    [switch]$DebugMode
)

[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $ScriptDir

$ExePath = Join-Path $ScriptDir "cmdcode2api.exe"
$PidFile = Join-Path $ScriptDir "cmdcode2api.pid"
$LogFile = Join-Path $ScriptDir "cmdcode2api.log"

# 浏览器实际访问地址：默认用 127.0.0.1（避免 localhost 被解析为 IPv6）；0.0.0.0/:: 监听时同样用 127.0.0.1 访问
$effectiveHost = if ($HostName) { $HostName } else { "127.0.0.1" }
if ($effectiveHost -eq "0.0.0.0" -or $effectiveHost -eq "::" -or $effectiveHost -eq "localhost") { $effectiveHost = "127.0.0.1" }
$effectivePort = if ($Port -gt 0) { $Port } else { 11434 }
$BaseUrl = "http://${effectiveHost}:$effectivePort"
$WebuiUrl = "$BaseUrl/webui"

function Test-HttpReady {
    param([string]$Url)
    try {
        $req = [System.Net.HttpWebRequest]::Create($Url)
        $req.Timeout = 2000
        $req.ReadWriteTimeout = 2000
        $req.Proxy = $null
        $req.AllowAutoRedirect = $false
        $resp = $req.GetResponse()
        $resp.Close()
        return $true
    } catch [System.Net.WebException] {
        # 拿到 4xx 响应也说明 HTTP 服务已在监听
        if ($_.Exception.Response) { return $true }
        return $false
    } catch {
        return $false
    }
}

function Open-WebUI {
    param([int]$TimeoutSec = 30)
    Write-Host "[INFO] 正在等待服务就绪..." -ForegroundColor Cyan
    $ready = $false
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $deadline) {
        if (Test-HttpReady -Url "$BaseUrl/health") { $ready = $true; break }
        Start-Sleep -Milliseconds 300
    }
    if ($ready) {
        Write-Host "[OK] 服务已就绪，正在打开 WebUI: $WebuiUrl" -ForegroundColor Green
        Start-Process $WebuiUrl
    } else {
        Write-Host "[WARN] 服务 ${TimeoutSec} 秒内未就绪，未自动打开 WebUI，可稍后手动访问: $WebuiUrl" -ForegroundColor Yellow
    }
}

# 1. 检查可执行文件是否存在或已过期，需要时自动编译
# 只看“存在与否”会让改了源码后双击仍跑旧二进制（启动的版本不含最新改动），
# 所以同时比较源文件与 exe 的修改时间。
function Test-ExeStale {
    param([string]$Exe)
    if (-not (Test-Path $Exe)) { return $true }
    $exeTime = (Get-Item $Exe).LastWriteTimeUtc
    # 参与构建的输入：Go 源码（排除 _test.go，它们不进二进制）+ 模块与版本定义
    $inputs = @()
    foreach ($dir in @("cmd", "internal")) {
        $full = Join-Path $ScriptDir $dir
        if (Test-Path $full) {
            $inputs += Get-ChildItem -Path $full -Recurse -File -Include *.go -ErrorAction SilentlyContinue |
                Where-Object { $_.Name -notlike "*_test.go" }
        }
    }
    foreach ($name in @("go.mod", "go.sum")) {
        $full = Join-Path $ScriptDir $name
        if (Test-Path $full) { $inputs += Get-Item $full }
    }
    if ($inputs.Count -eq 0) { return $false }
    $newest = ($inputs | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1).LastWriteTimeUtc
    return $newest -gt $exeTime
}

$needBuild = Test-ExeStale -Exe $ExePath
if ($needBuild) {
    if (Test-Path $ExePath) {
        Write-Host "[INFO] 检测到源码比 cmdcode2api.exe 更新，正在重新编译..." -ForegroundColor Yellow
    } else {
        Write-Host "[INFO] cmdcode2api.exe 未找到，正在编译构建..." -ForegroundColor Yellow
    }
    & go build -o cmdcode2api.exe ./cmd/cmdcode2api
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $ExePath)) {
        Write-Host "[ERROR] 编译失败，请检查 Go 开发环境。" -ForegroundColor Red
        exit 1
    }
    Write-Host "[OK] 编译完成: $ExePath" -ForegroundColor Green
}

# 2. 检查是否已经有正在运行的进程
$running = $false
$existingPid = $null
if (Test-Path $PidFile) {
    $existingPid = (Get-Content $PidFile -ErrorAction SilentlyContinue | Out-String).Trim()
    if ($existingPid -match '^\d+$') {
        $p = Get-Process -Id ([int]$existingPid) -ErrorAction SilentlyContinue
        if ($p -and $p.ProcessName -eq "cmdcode2api") {
            $running = $true
            Write-Host "[WARN] cmdcode2api 已经在运行中 (PID: $existingPid)！" -ForegroundColor Yellow
        }
    }
}

if (-not $running) {
    $anyP = Get-Process -Name "cmdcode2api" -ErrorAction SilentlyContinue
    if ($anyP) {
        $running = $true
        $existingPid = ($anyP | Select-Object -First 1).Id
        Set-Content -Path $PidFile -Value $existingPid -Encoding ascii
        Write-Host "[WARN] 检测到已有 cmdcode2api 进程正在运行 (PID: $existingPid)" -ForegroundColor Yellow
    }
}

if ($running) {
    # 刚编译出新二进制，但旧进程还在跑 —— 旧进程仍服务旧代码。此时只提示“已在运行”
    # 会让用户以为改动已生效，所以先停掉旧进程，再走下面的正常启动流程。
    if ($needBuild) {
        Write-Host "[INFO] 新编译的二进制尚未生效，正在重启以载入最新代码..." -ForegroundColor Yellow
        & (Join-Path $ScriptDir "stop.ps1")
        Start-Sleep -Milliseconds 600
        $still = Get-Process -Name "cmdcode2api" -ErrorAction SilentlyContinue
        if ($still) {
            Write-Host "[ERROR] 旧进程未能停止，请手动运行 .\stop.bat 后重试。" -ForegroundColor Red
            exit 1
        }
        $running = $false
    } else {
        Write-Host "==================================================" -ForegroundColor Cyan
        Write-Host " 服务地址:   $BaseUrl" -ForegroundColor White
        Write-Host " WebUI 管理: $WebuiUrl" -ForegroundColor White
        Write-Host " 运行日志:   $LogFile" -ForegroundColor White
        Write-Host " 进程 PID:   $existingPid" -ForegroundColor White
        Write-Host "==================================================" -ForegroundColor Cyan
        Open-WebUI -TimeoutSec 5
        exit 0
    }
}

# 3. 命令行参数组装
$argsList = @()
if ($HostName) { $argsList += "--host", $HostName }
if ($Port -gt 0) { $argsList += "--port", $Port }
if ($DebugMode) { $argsList += "--debug" }

# 4. 前台窗口模式
if ($Console) {
    Write-Host "[INFO] 正在以前台控制台窗口模式启动 cmdcode2api..." -ForegroundColor Cyan
    $proc = Start-Process -FilePath $ExePath -ArgumentList $argsList -WorkingDirectory $ScriptDir -PassThru
    Set-Content -Path $PidFile -Value $proc.Id -Encoding ascii
    Write-Host "[OK] cmdcode2api 已在控制台窗口启动 (PID: $($proc.Id))" -ForegroundColor Green
    Write-Host "     服务地址: $BaseUrl" -ForegroundColor White
    Write-Host "     WebUI:   $WebuiUrl" -ForegroundColor White
    Open-WebUI
    exit 0
}

# 5. 后台静默模式（日志输出到 cmdcode2api.log）
# 日志轮转：后台模式用 cmd.exe 重定向追加，进程自身无法控制文件增长，
# 所以启动前检查一次大小，超过阈值就滚动成 .1（只保留一份历史）。
$LogMaxBytes = 10MB
if (Test-Path $LogFile) {
    $logSize = (Get-Item $LogFile).Length
    if ($logSize -gt $LogMaxBytes) {
        $rotated = "$LogFile.1"
        if (Test-Path $rotated) { Remove-Item $rotated -Force -ErrorAction SilentlyContinue }
        Move-Item -Path $LogFile -Destination $rotated -Force
        Write-Host ("[INFO] 日志已轮转: {0:N1} MB -> {1}" -f ($logSize / 1MB), (Split-Path -Leaf $rotated)) -ForegroundColor DarkGray
    }
}

Write-Host "[INFO] 正在启动 cmdcode2api (后台静默模式)..." -ForegroundColor Cyan
$cmdArgs = "/c `"`"$ExePath`""
if ($argsList.Count -gt 0) {
    $cmdArgs += " " + ($argsList -join " ")
}
$cmdArgs += " >> `"$LogFile`" 2>&1`""

$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = "cmd.exe"
$psi.Arguments = $cmdArgs
$psi.WorkingDirectory = $ScriptDir
$psi.UseShellExecute = $false
$psi.CreateNoWindow = $true

$parentProc = [System.Diagnostics.Process]::Start($psi)

# 轮询探测进程是否拉起
$targetPid = $null
for ($i = 0; $i -lt 15; $i++) {
    Start-Sleep -Milliseconds 200
    $p = Get-Process -Name "cmdcode2api" -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($p) {
        $targetPid = $p.Id
        break
    }
}

if ($targetPid) {
    Set-Content -Path $PidFile -Value $targetPid -Encoding ascii
    Write-Host "[OK] cmdcode2api 启动成功！" -ForegroundColor Green
    Write-Host "==================================================" -ForegroundColor Cyan
    Write-Host " 进程 PID:   $targetPid" -ForegroundColor White
    Write-Host " 服务地址:   $BaseUrl" -ForegroundColor White
    Write-Host " WebUI 管理: $WebuiUrl" -ForegroundColor White
    Write-Host " 运行日志:   $LogFile" -ForegroundColor White
    Write-Host " 停止服务:   运行 .\stop.bat 或 .\stop.ps1" -ForegroundColor DarkGray
    Write-Host "==================================================" -ForegroundColor Cyan
    Open-WebUI
} else {
    Write-Host "[ERROR] 启动可能失败，请查看日志: $LogFile" -ForegroundColor Red
    if (Test-Path $LogFile) {
        Get-Content $LogFile -Tail 15
    }
    exit 1
}
