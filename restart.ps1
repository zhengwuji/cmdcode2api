param (
    [switch]$Console
)

[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding = [System.Text.Encoding]::UTF8
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $ScriptDir

Write-Host "[INFO] 正在重启 cmdcode2api 服务..." -ForegroundColor Cyan
& (Join-Path $ScriptDir "stop.ps1")
Start-Sleep -Seconds 1
if ($Console) {
    & (Join-Path $ScriptDir "start.ps1") -Console
} else {
    & (Join-Path $ScriptDir "start.ps1")
}
