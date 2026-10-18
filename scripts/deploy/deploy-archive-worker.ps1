<#
.SYNOPSIS
  在 Windows 上安装 moox-archive-worker，注册为系统服务（开机延迟自动启动，异常退出 10 秒后重启）。
  与 scripts/deploy/deploy-archive-worker.sh 做相同的事：渲染配置、检查密钥、注册服务、等待 health 通过。
  需要以管理员身份运行（注册服务）。worker 只出站：经固定的 Access 访问 archive-mgr 与 Storage。

.EXAMPLE
  .\scripts\deploy\deploy-archive-worker.ps1 -RootDir D:\moox-archive -AccessAddress storage.example:11004 `
    -AccessId access@storage -AccessKeyId assigned-key -Binary .\bin\windows-amd64\moox-archive-worker.exe

  密钥目录（默认 <Dir>\secrets）需要：access-archive-worker.key、storage-primary-auth.secret。
  脚本会把密钥目录的权限收紧为只允许当前用户与 SYSTEM（服务以 LocalSystem 运行）。
#>
[CmdletBinding()]
param(
  [string]$Dir = (Join-Path $env:LOCALAPPDATA 'moox\archive-worker'),
  [Parameter(Mandatory = $true)][string]$RootDir,
  [string]$SecretsDir = '',
  [string]$WorkerId = '',
  [Parameter(Mandatory = $true)][string]$AccessAddress,
  [Parameter(Mandatory = $true)][string]$AccessId,
  [Parameter(Mandatory = $true)][string]$AccessKeyId,
  [string]$Binary = '',
  [switch]$SkipBuild,
  [switch]$NoStart
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ServiceName = 'moox-archive-worker'
$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path

function Fail([string]$Message) { throw "[deploy-archive-worker] ERROR: $Message" }
function Log([string]$Message) { Write-Host "[deploy-archive-worker] $Message" }

# Restrict-Private 去掉继承的权限，只允许当前用户与 SYSTEM 访问（程序启动时会按同样的规则检查 ACL）；
# 目录上的授权同时由其中的新文件继承。
function Restrict-Private([string]$Path) {
  if (Test-Path -LiteralPath $Path -PathType Container) {
    & icacls $Path /inheritance:r /grant:r "$($env:USERNAME):(OI)(CI)F" '*S-1-5-18:(OI)(CI)F' | Out-Null
  } else {
    & icacls $Path /inheritance:r /grant:r "$($env:USERNAME):F" '*S-1-5-18:F' | Out-Null
  }
  if ($LASTEXITCODE -ne 0) { Fail "cannot restrict permissions of $Path" }
}

function Require-Secret([string]$Name) {
  $path = Join-Path $SecretsDir $Name
  if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { Fail "missing credential $path" }
  if ((Get-Item -LiteralPath $path).Attributes -band [IO.FileAttributes]::ReparsePoint) { Fail "credential $path must not be a link" }
  if (-not ((Get-Content -LiteralPath $path -Raw) -match '\S')) { Fail "credential $path is empty" }
  Restrict-Private $path
}

function New-HexSecret([int]$Bytes) {
  $buffer = New-Object byte[] $Bytes
  [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($buffer)
  return ([BitConverter]::ToString($buffer) -replace '-', '').ToLowerInvariant()
}

if (-not [IO.Path]::IsPathRooted($RootDir) -or $RootDir -match '\\Documents(\\|$)') { Fail '-RootDir must be an absolute path outside Documents' }
if ($AccessAddress -notmatch '^(\[[0-9a-fA-F:]+\]|[A-Za-z0-9.-]+):[0-9]+$') { Fail '-AccessAddress must be host:port' }
if ($AccessId -notmatch '^access@[a-z][a-z0-9]*(-[a-z0-9]+)*$') { Fail '-AccessId must be access@host' }
if ($AccessKeyId -notmatch '^[A-Za-z0-9._@:-]{1,128}$') { Fail '-AccessKeyId must be the assigned credential identifier' }
if ($Dir -match '\\Documents(\\|$)') { Fail '-Dir must not be under Documents' }

New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$Dir = (Resolve-Path $Dir).Path
if (-not $SecretsDir) { $SecretsDir = Join-Path $Dir 'secrets' }
if (-not $WorkerId) { $WorkerId = "archive-worker@$($env:COMPUTERNAME.ToLowerInvariant())" }
if ($WorkerId -notmatch '^[A-Za-z0-9@._-]+$') { Fail 'worker id contains unsupported characters' }

foreach ($sub in 'bin', 'config', 'logs') { New-Item -ItemType Directory -Force -Path (Join-Path $Dir $sub) | Out-Null }
New-Item -ItemType Directory -Force -Path $RootDir | Out-Null
New-Item -ItemType Directory -Force -Path $SecretsDir | Out-Null
Restrict-Private $SecretsDir
foreach ($secret in 'access-archive-worker.key', 'storage-primary-auth.secret') { Require-Secret $secret }

$exe = Join-Path $Dir 'bin\moox-archive-worker.exe'
$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing -and $existing.Status -ne 'Stopped') {
  Log "stop the running $ServiceName service"
  & $exe service stop
}
if ($Binary) {
  if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) { Fail "missing binary $Binary" }
  Copy-Item -LiteralPath $Binary -Destination $exe -Force
} elseif (-not $SkipBuild) {
  Log 'build moox-archive-worker'
  $version = (& git -C $Root describe --always --dirty 2>$null)
  if (-not $version) { $version = 'dev' }
  Push-Location (Join-Path $Root 'modules\archive')
  try {
    $env:CGO_ENABLED = '0'
    & go build -ldflags "-X main.Version=$version" -o $exe ./cmd/worker
    if ($LASTEXITCODE -ne 0) { Fail 'go build failed' }
  } finally { Pop-Location }
}
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { Fail "missing $exe" }

$secretsYaml = $SecretsDir -replace '\\', '/'
$rootYaml = $RootDir -replace '\\', '/'
$config = @"
# Rendered by scripts/deploy/deploy-archive-worker.ps1; edit the parameters, not this file.
gateway_client:
  access_address: "$AccessAddress"
  access_id: "$AccessId"
  caller: archive-worker
  key_id: "$AccessKeyId"
  key_file: $secretsYaml/access-archive-worker.key
  compression: gzip

storage_auth_file: $secretsYaml/storage-primary-auth.secret

worker:
  worker_id: $WorkerId
  root_dir: $rootYaml
  export_formats: [csv]
  min_free_bytes: 5GB
  heartbeat_interval: 10s
  pull_interval: 5s

staging:
  cache_size: 256MB
  rebuild_ratio: 2

pacing:
  request_seconds: [25, 55]
  minute_window: true
  hour_quiet: ["58:00", "04:00"]
  request_interval: 200ms
  max_bytes_per_second: 512KB
  max_concurrent_runs: 4
  max_runs_per_data_node: 2
  page_rows: 500

health:
  addr: 127.0.0.1:11418

maintenance:
  at: "04:17:43"
"@
$configPath = Join-Path $Dir 'config\app.yaml'
Set-Content -LiteralPath $configPath -Value $config -Encoding utf8
Restrict-Private $configPath

# 健康检查签名凭据：首次安装时生成，写进服务的环境变量（注册表 Services\<名称>\Environment）。
$healthEnv = Join-Path $SecretsDir 'health-auth.env'
if (-not (Test-Path -LiteralPath $healthEnv)) {
  @(
    'MOOX_HEALTH_AUTH_VERSION=moox-health-v1',
    "MOOX_HEALTH_AUTH_ACCESS_KEY=archive-worker-$(New-HexSecret 8)",
    "MOOX_HEALTH_AUTH_SECRET_KEY=$(New-HexSecret 32)"
  ) | Set-Content -LiteralPath $healthEnv -Encoding ascii
  Restrict-Private $healthEnv
}
$healthLines = @(Get-Content -LiteralPath $healthEnv | Where-Object { $_ -match '^MOOX_HEALTH_AUTH_[A-Z_]+=.+' })
$healthValues = @{}
foreach ($line in $healthLines) { $key, $value = $line -split '=', 2; $healthValues[$key] = $value }
if (-not $healthValues['MOOX_HEALTH_AUTH_ACCESS_KEY'] -or -not $healthValues['MOOX_HEALTH_AUTH_SECRET_KEY']) { Fail "invalid $healthEnv" }

if ($existing) {
  & $exe service uninstall
  if ($LASTEXITCODE -ne 0) { Fail "cannot remove the old $ServiceName service" }
}
& $exe service install -config $configPath
if ($LASTEXITCODE -ne 0) { Fail "cannot register the $ServiceName service" }
$serviceKey = "HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName"
New-ItemProperty -Path $serviceKey -Name Environment -PropertyType MultiString -Value $healthLines -Force | Out-Null

if ($NoStart) {
  Log "installed to $Dir (not started)"
  exit 0
}
& $exe service start
if ($LASTEXITCODE -ne 0) { Fail "cannot start the $ServiceName service" }

Log 'wait for moox-archive-worker readiness'
$env:MOOX_HEALTH_AUTH_ACCESS_KEY = $healthValues['MOOX_HEALTH_AUTH_ACCESS_KEY']
$env:MOOX_HEALTH_AUTH_SECRET_KEY = $healthValues['MOOX_HEALTH_AUTH_SECRET_KEY']
for ($i = 0; $i -lt 90; $i++) {
  & $exe health *> $null
  if ($LASTEXITCODE -eq 0) {
    Log "moox-archive-worker is ready ($WorkerId)"
    exit 0
  }
  Start-Sleep -Seconds 2
}
& $exe health
Fail 'moox-archive-worker did not become ready; check the logs directory and the Windows event log'
