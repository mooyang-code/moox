# Windows 上的构建前置检查：构建脚本是 bash 写的，要在 WSL（推荐）或 Git Bash 里运行。
# 本脚本只检查 Windows 一侧有没有这些入口，缺什么就给出 winget 命令；通过后进入 WSL 或 Git Bash，运行
#   bash scripts/build/check-build-env.sh --profile all
# 做完整检查。完整说明见 docs/构建手册.md。
param(
    [switch]$Json
)

$ErrorActionPreference = 'SilentlyContinue'

function Test-Command([string]$Name) {
    return [bool](Get-Command $Name -ErrorAction SilentlyContinue)
}

$items = @()

# WSL：推荐的构建环境（Ubuntu 里的行为与 Linux 一致）。
$wslStatus = 'missing'
if (Test-Command 'wsl') {
    $distros = (& wsl.exe --list --quiet 2>$null) -join ' '
    if ($distros -and $distros.Trim().Length -gt 0) { $wslStatus = 'ok' } else { $wslStatus = 'no_distro' }
}
$items += [pscustomobject]@{
    id = 'wsl'; label = 'WSL2（推荐）'; status = $wslStatus
    fix = 'wsl --install -d Ubuntu   # 需要管理员权限，装完重启；之后在 Ubuntu 里按 Linux 的步骤准备环境'
}

# Git for Windows 自带 Git Bash，是不装 WSL 时的备选。
$gitBash = Join-Path ${env:ProgramFiles} 'Git\bin\bash.exe'
$gitStatus = 'missing'
if (Test-Path $gitBash) { $gitStatus = 'ok' }
$items += [pscustomobject]@{
    id = 'git-bash'; label = 'Git Bash（备选）'; status = $gitStatus
    fix = 'winget install --id Git.Git -e'
}

# Docker：构建 Storage（DuckDB，需要 CGO）时用。
$dockerStatus = 'missing'
if (Test-Command 'docker') {
    & docker info *> $null
    if ($LASTEXITCODE -eq 0) { $dockerStatus = 'ok' } else { $dockerStatus = 'not_running' }
}
$items += [pscustomobject]@{
    id = 'docker'; label = 'Docker Desktop'; status = $dockerStatus
    fix = 'winget install --id Docker.DockerDesktop -e   # 启动后在 Settings 里打开 WSL2 后端和对应发行版的 WSL 集成'
}

# winget 本身：下面的修复命令要用到。
$wingetStatus = 'missing'
if (Test-Command 'winget') { $wingetStatus = 'ok' }
$items += [pscustomobject]@{
    id = 'winget'; label = 'winget'; status = $wingetStatus
    fix = '在 Microsoft Store 安装“应用安装程序”（App Installer）'
}

$bashReady = ($wslStatus -eq 'ok') -or ($gitStatus -eq 'ok')
$ready = $bashReady -and ($dockerStatus -eq 'ok')

if ($Json) {
    [pscustomobject]@{ os = 'windows'; ok = $ready; bash_ready = $bashReady; checks = $items } | ConvertTo-Json -Depth 4
} else {
    Write-Output '系统：Windows'
    foreach ($item in $items) {
        $mark = switch ($item.status) { 'ok' { '[通过]' } 'not_running' { '[未运行]' } 'no_distro' { '[未安装发行版]' } default { '[缺少]' } }
        Write-Output ("  {0} {1}" -f $mark, $item.label)
        if ($item.status -ne 'ok') { Write-Output ("           修复：{0}" -f $item.fix) }
    }
    Write-Output ''
    if (-not $bashReady) {
        Write-Output '还没有 bash 环境：安装 WSL2 或 Git Bash 之一，然后在其中运行 bash scripts/build/check-build-env.sh --profile all。'
    } elseif ($dockerStatus -ne 'ok') {
        Write-Output 'bash 环境可用。只构建纯 Go 服务可以不装 Docker；构建 Storage（DuckDB）需要 Docker Desktop。'
    } else {
        Write-Output 'Windows 一侧就绪：进入 WSL 或 Git Bash，运行 bash scripts/build/check-build-env.sh --profile all 做完整检查。'
    }
}

if ($bashReady) { exit 0 } else { exit 1 }
