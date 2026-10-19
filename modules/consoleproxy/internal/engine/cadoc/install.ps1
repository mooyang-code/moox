# 把 MooX 管理台的 HTTPS 根证书导入当前用户的“受信任的根证书颁发机构”。
# 先核对证书的 SHA-256 指纹与同目录 root.sha256 一致；Windows 会弹出确认框，需要用户确认。
param(
    [string]$CaFile = (Join-Path $PSScriptRoot 'moox-caddy-root.crt'),
    [string]$FingerprintFile = (Join-Path $PSScriptRoot 'root.sha256'),
    [switch]$Check
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $CaFile)) { throw "找不到证书文件：$CaFile" }
$raw = Get-Content -LiteralPath $CaFile -Raw
if ($raw -match 'PRIVATE KEY') { throw '证书文件里含有私钥，拒绝处理' }

$certificate = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2 -ArgumentList $CaFile
$sha256 = [System.Security.Cryptography.SHA256]::Create()
$actual = ($sha256.ComputeHash($certificate.RawData) | ForEach-Object { $_.ToString('X2') }) -join ':'
Write-Output "SHA256 Fingerprint=$actual"

if (Test-Path -LiteralPath $FingerprintFile) {
    $expected = (Get-Content -LiteralPath $FingerprintFile -Raw).Trim().ToUpperInvariant()
    if ($expected -ne $actual) { throw "指纹与 root.sha256 不一致（期望 $expected），拒绝安装" }
}

$trusted = Get-ChildItem Cert:\CurrentUser\Root, Cert:\LocalMachine\Root -ErrorAction SilentlyContinue |
    Where-Object { $_.Thumbprint -eq $certificate.Thumbprint } |
    Select-Object -First 1
if ($trusted) {
    Write-Output '证书已被信任，无需改动。'
    exit 0
}
if ($Check) { exit 1 }

Import-Certificate -FilePath $CaFile -CertStoreLocation Cert:\CurrentUser\Root | Out-Null
Write-Output '已导入当前用户的受信任根证书颁发机构。请彻底退出并重新打开浏览器。'
