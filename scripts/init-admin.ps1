param([string]$Username = 'admin', [string]$Config = (Join-Path $PSScriptRoot 'config.json'))
$ErrorActionPreference = 'Stop'
$taskPassword = Read-Host '请输入首个管理员初始密码（10～72 字节）' -AsSecureString
$taskBstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($taskPassword)
try {
    $taskPlain = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($taskBstr)
    $taskPlain | & (Join-Path $PSScriptRoot 'gmpan.exe') init-admin -config ([IO.Path]::GetFullPath($Config)) -username $Username
    if ($LASTEXITCODE -ne 0) { throw '管理员创建失败' }
} finally {
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($taskBstr)
    $taskPlain = $null
    $taskPassword.Dispose()
}
