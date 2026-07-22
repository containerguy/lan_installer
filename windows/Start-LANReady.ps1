[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string]$ServerUrl,
    [Parameter(Mandatory = $true)] [string]$EventId,
    [string]$TargetRoot = 'C:\LAN-Games',
    [ValidateSet('portable', 'managed')] [string]$Mode = 'portable',
    [ValidateSet('off', 'check', 'install')] [string]$WindowsUpdate = 'off',
    [switch]$Yes,
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
$baseDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$exe = Join-Path $baseDir 'LANReady.exe'
$publicKey = Join-Path $baseDir 'lanready-public.key'
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw "LANReady.exe fehlt neben diesem Skript: $exe" }
if (-not (Test-Path -LiteralPath $publicKey -PathType Leaf)) { throw "lanready-public.key fehlt neben diesem Skript: $publicKey" }

if ([string]::IsNullOrWhiteSpace($env:LANREADY_TOKEN)) {
    $secureToken = Read-Host 'LANReady Event-Token' -AsSecureString
    $tokenPointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secureToken)
    try { $env:LANREADY_TOKEN = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($tokenPointer) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($tokenPointer) }
}

$arguments = @('-server', $ServerUrl, '-event', $EventId, '-public-key', $publicKey, '-target-root', $TargetRoot, '-mode', $Mode, '-windows-update', $WindowsUpdate)
if ($Yes) { $arguments += '-yes' }
if ($DryRun) { $arguments += '-dry-run' }
try {
    & $exe @arguments
    exit $LASTEXITCODE
}
finally { Remove-Item Env:LANREADY_TOKEN -ErrorAction SilentlyContinue }
