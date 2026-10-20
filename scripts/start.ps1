param([string]$Config = (Join-Path $PSScriptRoot 'config.json'))
$ErrorActionPreference = 'Stop'
$taskExecutable = Join-Path $PSScriptRoot 'gmpan.exe'
if (!(Test-Path -LiteralPath $taskExecutable)) { throw '请在构建后的部署目录运行此脚本' }
if (!(Test-Path -LiteralPath $Config)) { throw '请将 config.example.json 复制为 config.json 并完成部署配置' }
& $taskExecutable serve -config ([IO.Path]::GetFullPath($Config))
if ($LASTEXITCODE -ne 0) { throw ('服务退出，退出码：' + $LASTEXITCODE) }
