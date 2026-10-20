param([string]$OutputDirectory = '')
$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path -Parent $PSScriptRoot
if (!$OutputDirectory) { $OutputDirectory = Join-Path $taskRoot 'release\GmPan' }
$taskOutput = [IO.Path]::GetFullPath($OutputDirectory)
if ($taskOutput -eq [IO.Path]::GetPathRoot($taskOutput)) { throw '输出目录不能是磁盘根目录' }
$taskGo = Get-Command go -ErrorAction SilentlyContinue
if ($taskGo) { $taskGoPath = $taskGo.Source } elseif (Test-Path -LiteralPath 'C:\Program Files\Go\bin\go.exe') { $taskGoPath = 'C:\Program Files\Go\bin\go.exe' } else { throw '请先安装 Go 1.26 或更新版本' }
Push-Location (Join-Path $taskRoot 'web')
try {
    & npm.cmd ci --no-audit --no-fund
    if ($LASTEXITCODE -ne 0) { throw '前端依赖安装失败' }
    & npm.cmd run build
    if ($LASTEXITCODE -ne 0) { throw '前端构建失败' }
} finally { Pop-Location }
New-Item -ItemType Directory -Path $taskOutput -Force | Out-Null
Push-Location $taskRoot
try {
    & $taskGoPath test ./...
    if ($LASTEXITCODE -ne 0) { throw '后端测试失败' }
    & $taskGoPath build -trimpath -ldflags='-s -w' -o (Join-Path $taskOutput 'gmpan.exe') ./cmd/gmpan
    if ($LASTEXITCODE -ne 0) { throw '后端构建失败' }
} finally { Pop-Location }
Copy-Item -LiteralPath (Join-Path $taskRoot 'config.example.json') -Destination $taskOutput -Force
Copy-Item -LiteralPath (Join-Path $taskRoot 'scripts\start.ps1') -Destination $taskOutput -Force
Copy-Item -LiteralPath (Join-Path $taskRoot 'scripts\install-service.ps1') -Destination $taskOutput -Force
Copy-Item -LiteralPath (Join-Path $taskRoot 'scripts\init-admin.ps1') -Destination $taskOutput -Force
Copy-Item -LiteralPath (Join-Path $taskRoot 'docs\系统部署操作手册.md') -Destination $taskOutput -Force
Copy-Item -LiteralPath (Join-Path $taskRoot 'docs\用户操作手册.md') -Destination $taskOutput -Force
Copy-Item -LiteralPath (Join-Path $taskRoot 'docs\公司文件资源管理系统需求.md') -Destination $taskOutput -Force
Copy-Item -LiteralPath (Join-Path $taskRoot 'docs\images') -Destination $taskOutput -Recurse -Force
Copy-Item -LiteralPath (Join-Path $taskRoot 'LICENSE') -Destination $taskOutput -Force
Copy-Item -LiteralPath (Join-Path $taskRoot 'SPONSORSHIP.md') -Destination $taskOutput -Force
$taskDigest = Get-FileHash -LiteralPath (Join-Path $taskOutput 'gmpan.exe') -Algorithm SHA256
($taskDigest.Hash + '  gmpan.exe') | Set-Content -LiteralPath (Join-Path $taskOutput 'SHA256SUMS.txt') -Encoding ascii
$taskDigest | Select-Object Algorithm,Hash,Path
Write-Output ('部署目录：' + $taskOutput)
