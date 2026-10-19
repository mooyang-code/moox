<#
.SYNOPSIS
  在 Windows 上安装 moox-factor-engine，注册为系统服务（开机延迟自动启动，异常退出 10 秒后重启）。
  与 scripts/deploy/deploy-factor-engine.sh 做相同的事：渲染配置、检查密钥、注册服务、等待 health 通过。
  需要以管理员身份运行（注册服务）。引擎只出站：经 Access 访问 FactorMgr 与 Storage，连接 EventBus。

.EXAMPLE
  .\scripts\deploy\deploy-factor-engine.ps1 -AccessAddress storage.example:11004 -AccessId access@storage `
    -AccessKeyId assigned-key -EventBusUrl tls://106.53.107.122:4222 -Binary .\bin\windows-amd64\moox-factor-engine.exe

  密钥目录（默认 <Dir>\secrets）需要：access-factor-engine.key、storage-primary-auth.secret、factor-eventbus.yaml（及其 ca 文件）。
  脚本会把密钥目录的权限收紧为只允许当前用户与 SYSTEM（服务以 LocalSystem 运行）。
#>
[CmdletBinding()]
param(
  [string]$Dir = (Join-Path $env:LOCALAPPDATA 'moox\factor-engine'),
  [string]$SecretsDir = '',
  [string]$EngineId = '',
  [Parameter(Mandatory = $true)][string]$AccessAddress,
  [Parameter(Mandatory = $true)][string]$AccessId,
  [Parameter(Mandatory = $true)][string]$AccessKeyId,
  [Parameter(Mandatory = $true)][string]$EventBusUrl,
  [string]$Binary = '',
  [switch]$SkipBuild,
  [switch]$NoStart
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$ServiceName = 'moox-factor-engine'
$Root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path

function Fail([string]$Message) { throw "[deploy-factor-engine] ERROR: $Message" }
function Log([string]$Message) { Write-Host "[deploy-factor-engine] $Message" }

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

if ($AccessAddress -notmatch '^(\[[0-9a-fA-F:]+\]|[A-Za-z0-9.-]+):[0-9]+$') { Fail '-AccessAddress must be host:port' }
if ($AccessId -notmatch '^access@[a-z][a-z0-9]*(-[a-z0-9]+)*$') { Fail '-AccessId must be access@host' }
if ($AccessKeyId -notmatch '^[A-Za-z0-9._@:-]{1,128}$') { Fail '-AccessKeyId must be the assigned credential identifier' }
if ($EventBusUrl -notmatch '^(nats|tls)://') { Fail '-EventBusUrl must be a nats:// or tls:// URL' }
if ($Dir -match '\\Documents(\\|$)') { Fail '-Dir must not be under Documents' }

New-Item -ItemType Directory -Force -Path $Dir | Out-Null
$Dir = (Resolve-Path $Dir).Path
if (-not $SecretsDir) { $SecretsDir = Join-Path $Dir 'secrets' }
if (-not $EngineId) { $EngineId = "factor-engine@$($env:COMPUTERNAME.ToLowerInvariant())" }
if ($EngineId -notmatch '^[A-Za-z0-9@._-]+$') { Fail 'engine id contains unsupported characters' }

foreach ($sub in 'bin', 'config', 'data\engine', 'logs') { New-Item -ItemType Directory -Force -Path (Join-Path $Dir $sub) | Out-Null }
New-Item -ItemType Directory -Force -Path $SecretsDir | Out-Null
Restrict-Private $SecretsDir
foreach ($secret in 'access-factor-engine.key', 'storage-primary-auth.secret', 'factor-eventbus.yaml') { Require-Secret $secret }

$exe = Join-Path $Dir 'bin\moox-factor-engine.exe'
$existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existing -and $existing.Status -ne 'Stopped') {
  Log "stop the running $ServiceName service"
  & $exe service stop
}
if ($Binary) {
  if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) { Fail "missing binary $Binary" }
  Copy-Item -LiteralPath $Binary -Destination $exe -Force
} elseif (-not $SkipBuild) {
  Log 'build moox-factor-engine'
  $version = (& git -C $Root describe --always --dirty 2>$null)
  if (-not $version) { $version = 'dev' }
  Push-Location (Join-Path $Root 'modules\factor')
  try {
    $env:CGO_ENABLED = '0'
    & go build -ldflags "-X main.Version=$version" -o $exe ./cmd/engine
    if ($LASTEXITCODE -ne 0) { Fail 'go build failed' }
  } finally { Pop-Location }
}
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { Fail "missing $exe" }

Copy-Item -LiteralPath (Join-Path $Root 'modules\factor\config\trpc_go.engine.yaml') -Destination (Join-Path $Dir 'config\trpc_go.engine.yaml') -Force
$secretsYaml = $SecretsDir -replace '\\', '/'
$config = @"
# Rendered by scripts/deploy/deploy-factor-engine.ps1; edit the parameters, not this file.
engine:
  id: $EngineId
  heartbeat_interval: 10s

gateway_client:
  access_address: "$AccessAddress"
  access_id: "$AccessId"
  caller: factor-engine
  key_id: "$AccessKeyId"
  key_file: $secretsYaml/access-factor-engine.key

manager:
  timeout: 30s

catalog_sync:
  interval: 1m
  offset: 45s
  state_file: ./data/engine/catalog.json

storage:
  auth_secret_file: $secretsYaml/storage-primary-auth.secret

eventbus:
  urls:
    - $EventBusUrl
  credential_file: $secretsYaml/factor-eventbus.yaml
  fetch_max_wait: 10s

compute:
  max_memory: 1GiB

pipeline:
  read_batch_subjects: 50
  read_timeout: 60s
  write_batch_rows: 1000
  period_budget_min: 60s
  period_budget_max: 15m

recalc:
  chunk_periods: 500
  poll_interval: 5s
"@
$configPath = Join-Path $Dir 'config\engine.yaml'
Set-Content -LiteralPath $configPath -Value $config -Encoding utf8
Restrict-Private $configPath

# 健康检查签名凭据：首次安装时生成，写进服务的环境变量（注册表 Services\<名称>\Environment）。
$healthEnv = Join-Path $SecretsDir 'health-auth.env'
if (-not (Test-Path -LiteralPath $healthEnv)) {
  @(
    'MOOX_HEALTH_AUTH_VERSION=moox-health-v1',
    "MOOX_HEALTH_AUTH_ACCESS_KEY=factor-engine-$(New-HexSecret 8)",
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
& $exe service install -config $configPath -conf (Join-Path $Dir 'config\trpc_go.engine.yaml')
if ($LASTEXITCODE -ne 0) { Fail "cannot register the $ServiceName service" }
$serviceKey = "HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName"
New-ItemProperty -Path $serviceKey -Name Environment -PropertyType MultiString -Value $healthLines -Force | Out-Null

if ($NoStart) {
  Log "installed to $Dir (not started)"
  exit 0
}
& $exe service start
if ($LASTEXITCODE -ne 0) { Fail "cannot start the $ServiceName service" }

Log 'wait for moox-factor-engine readiness'
$env:MOOX_HEALTH_AUTH_ACCESS_KEY = $healthValues['MOOX_HEALTH_AUTH_ACCESS_KEY']
$env:MOOX_HEALTH_AUTH_SECRET_KEY = $healthValues['MOOX_HEALTH_AUTH_SECRET_KEY']
for ($i = 0; $i -lt 90; $i++) {
  & $exe health *> $null
  if ($LASTEXITCODE -eq 0) {
    Log "moox-factor-engine is ready ($EngineId)"
    exit 0
  }
  Start-Sleep -Seconds 2
}
& $exe health
Fail 'moox-factor-engine did not become ready; check the logs directory and the Windows event log'
