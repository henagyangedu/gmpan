param(
    [Parameter(Mandatory=$true)][string]$MySQLArchive,
    [Parameter(Mandatory=$true)][string]$MySQLRoot,
    [Parameter(Mandatory=$true)][string]$MySQLVersion,
    [Parameter(Mandatory=$true)][string]$ClamAVDirectory,
    [Parameter(Mandatory=$true)][string]$ClamDatabaseDirectory,
    [Parameter(Mandatory=$true)][string]$VCRedist,
    [string]$OutputDirectory = ''
)
$ErrorActionPreference = 'Stop'
$taskRoot = Split-Path -Parent $PSScriptRoot
if (!$OutputDirectory) { $OutputDirectory = Join-Path $taskRoot 'release\GmPan-Setup-Test' }
$taskOutput = [IO.Path]::GetFullPath($OutputDirectory)
if (Test-Path -LiteralPath $taskOutput) { throw '图形部署包输出目录已存在，请选择新的空输出位置，避免混入旧配置和数据' }
$taskZip = $taskOutput + '.zip'
if (Test-Path -LiteralPath $taskZip) { throw '目标 ZIP 已存在，不覆盖' }
if ($MySQLVersion -notmatch '^8\.4\.\d+$' -or $MySQLRoot -match '[\\/:]' -or !$MySQLRoot) { throw '必须指定 MySQL 8.4 LTS 的准确版本和压缩包内部根目录名称' }
$taskMySQL = (Resolve-Path -LiteralPath $MySQLArchive).Path
$taskClam = (Resolve-Path -LiteralPath $ClamAVDirectory).Path
$taskDatabase = (Resolve-Path -LiteralPath $ClamDatabaseDirectory).Path
$taskVC = (Resolve-Path -LiteralPath $VCRedist).Path
if ((Get-AuthenticodeSignature -LiteralPath $taskVC).Status -ne 'Valid') { throw 'Microsoft 运行库数字签名验证失败' }
if (!(Test-Path -LiteralPath (Join-Path $taskClam 'clamscan.exe'))) { throw '缺少 clamscan.exe' }
foreach ($taskDBFile in @('main.cvd','daily.cvd','bytecode.cvd')) {
    if (!(Test-Path -LiteralPath (Join-Path $taskDatabase $taskDBFile))) { throw ('缺少病毒库：' + $taskDBFile) }
}
& (Join-Path $PSScriptRoot 'build.ps1') -OutputDirectory $taskOutput
if ($LASTEXITCODE -ne 0) { throw '基础部署包构建失败' }
$taskPayload = Join-Path $taskOutput 'payload'
New-Item -ItemType Directory -Path $taskPayload | Out-Null
Copy-Item -LiteralPath $taskMySQL -Destination (Join-Path $taskPayload 'mysql.zip')
Copy-Item -LiteralPath $taskVC -Destination (Join-Path $taskPayload 'vc_redist.x64.exe')
Add-Type -AssemblyName System.IO.Compression
Add-Type -AssemblyName System.IO.Compression.FileSystem
$taskArchive = [IO.Compression.ZipFile]::Open((Join-Path $taskPayload 'clamav.zip'), [IO.Compression.ZipArchiveMode]::Create)
try {
    # Runtime files and original documentation/licenses only; no debug symbols,
    # compiler libraries, developer configuration, or update state.
    foreach ($taskFile in Get-ChildItem -LiteralPath $taskClam -File) {
        if ($taskFile.Extension -notin @('.pdb','.lib')) {
            [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($taskArchive,$taskFile.FullName,('clamav/' + $taskFile.Name),[IO.Compression.CompressionLevel]::Optimal) | Out-Null
        }
    }
    foreach ($taskSubdir in @('COPYING','UserManual','certs','conf_examples')) {
        $taskSubpath = Join-Path $taskClam $taskSubdir
        if (Test-Path -LiteralPath $taskSubpath -PathType Container) {
            foreach ($taskFile in Get-ChildItem -LiteralPath $taskSubpath -File -Recurse) {
                $taskRelative = $taskFile.FullName.Substring($taskClam.Length + 1).Replace('\','/')
                [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($taskArchive,$taskFile.FullName,('clamav/' + $taskRelative),[IO.Compression.CompressionLevel]::Optimal) | Out-Null
            }
        }
    }
    foreach ($taskDBFile in @('main.cvd','daily.cvd','bytecode.cvd')) {
        [IO.Compression.ZipFileExtensions]::CreateEntryFromFile($taskArchive,(Join-Path $taskDatabase $taskDBFile),('clamav/database/' + $taskDBFile),[IO.Compression.CompressionLevel]::NoCompression) | Out-Null
    }
} finally { $taskArchive.Dispose() }
$taskHashes = [ordered]@{}
foreach ($taskFile in @('gmpan.exe','payload/mysql.zip','payload/clamav.zip','payload/vc_redist.x64.exe')) {
    $taskHashes[$taskFile] = (Get-FileHash -LiteralPath (Join-Path $taskOutput $taskFile) -Algorithm SHA256).Hash.ToLowerInvariant()
}
$taskManifest = [ordered]@{ mysql_root = $MySQLRoot; mysql_version = $MySQLVersion; sha256 = $taskHashes }
$taskUTF8 = New-Object Text.UTF8Encoding($false)
[IO.File]::WriteAllText((Join-Path $taskPayload 'manifest.json'),($taskManifest | ConvertTo-Json -Depth 4),$taskUTF8)
[IO.Compression.ZipFile]::CreateFromDirectory($taskOutput,$taskZip,[IO.Compression.CompressionLevel]::Fastest,$true)
# Read the actual ZIP entries back, including the wizard that is not part of
# the install payload manifest. Never publish a package with mixed builds.
$taskExpected = [ordered]@{}
foreach ($taskName in $taskHashes.Keys) { $taskExpected[$taskName] = $taskHashes[$taskName] }
foreach ($taskName in @('GmPan安装向导.exe','SHA256SUMS.txt','payload/manifest.json')) {
    $taskExpected[$taskName] = (Get-FileHash -LiteralPath (Join-Path $taskOutput $taskName) -Algorithm SHA256).Hash.ToLowerInvariant()
}
$taskReadback = [IO.Compression.ZipFile]::OpenRead($taskZip)
try {
    $taskPrefix = (Split-Path -Leaf $taskOutput) + '/'
    foreach ($taskName in $taskExpected.Keys) {
        $taskEntry = $taskReadback.GetEntry($taskPrefix + $taskName)
        if ($null -eq $taskEntry) { throw ('ZIP 缺少部署文件：' + $taskName) }
        $taskStream = $taskEntry.Open()
        $taskHasher = [Security.Cryptography.SHA256]::Create()
        try { $taskActual = [BitConverter]::ToString($taskHasher.ComputeHash($taskStream)).Replace('-','').ToLowerInvariant() }
        finally { $taskStream.Dispose(); $taskHasher.Dispose() }
        if ($taskActual -ne $taskExpected[$taskName]) { throw ('ZIP 回读校验失败：' + $taskName) }
    }
} finally { $taskReadback.Dispose() }
Write-Output 'ZIP 中的程序、组件和清单回读校验通过'
$taskZipDigest = Get-FileHash -LiteralPath $taskZip -Algorithm SHA256
($taskZipDigest.Hash + '  ' + (Split-Path -Leaf $taskZip)) | Set-Content -LiteralPath ($taskZip + '.sha256') -Encoding utf8
$taskZipDigest | Format-List
Write-Output ('图形部署测试包：' + $taskZip)
