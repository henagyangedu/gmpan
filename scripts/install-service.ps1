param([string]$Config = (Join-Path $PSScriptRoot 'config.json'))
$ErrorActionPreference = 'Stop'
$taskIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
$taskPrincipal = [Security.Principal.WindowsPrincipal]::new($taskIdentity)
if (!$taskPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) { throw '请以管理员身份运行服务安装脚本' }
$taskExecutable = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'gmpan.exe'))
$taskConfigPath = (Resolve-Path -LiteralPath $Config).Path
if (!(Test-Path -LiteralPath $taskExecutable)) { throw '未找到 gmpan.exe' }
if (Get-Service -Name GmPan -ErrorAction SilentlyContinue) { throw 'GmPan 服务已存在。升级时停服后替换程序，再启动服务，无需重复安装' }
$taskCommand = '"' + $taskExecutable + '" serve -config "' + $taskConfigPath + '"'
& sc.exe create GmPan binPath= $taskCommand start= demand DisplayName= 'GmPan 公司资源管理'
if ($LASTEXITCODE -ne 0) { throw '服务注册失败' }
& sc.exe failure GmPan reset= 86400 actions= restart/10000/restart/30000/restart/60000
Write-Output '服务已注册为手动启动，尚未运行。请按部署手册设置专用服务账号、目录权限和内网防火墙规则，再改为自动启动并运行 Start-Service GmPan。'
