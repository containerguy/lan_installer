param(
    [Parameter(Mandatory = $true)][string]$Version,
    [Parameter(Mandatory = $true)][string]$ReleasePublicKey,
    [Parameter(Mandatory = $true)][ValidatePattern('^[0-9a-f]{64}$')][string]$PublisherCertificateSHA256,
    [Parameter(Mandatory = $true)][ValidatePattern('^[0-9A-Fa-f]{40}$')][string]$CertificateThumbprint,
    [Parameter(Mandatory = $true)][ValidatePattern('^https?://')][string]$TimestampUrl,
    [string]$Wails = 'wails',
    [string]$SignTool = 'signtool.exe',
    [string]$MakeNSIS = 'makensis.exe',
    [string]$Go = 'go'
)

$ErrorActionPreference = 'Stop'
if ([System.Environment]::OSVersion.Platform -ne [System.PlatformID]::Win32NT) {
    throw 'Dieser Releasepfad muss auf einer vertrauenswürdigen Windows-Buildstation laufen.'
}

function Assert-LANReadySignature {
    param([string]$Path, [string]$ExpectedCertificateSHA256)
    $signature = Get-AuthenticodeSignature -FilePath $Path
    if ($signature.Status -ne 'Valid' -or $null -eq $signature.SignerCertificate) {
        throw "Ungültige Authenticode-Signatur: $Path ($($signature.Status))"
    }
    if ($null -eq $signature.TimeStamperCertificate) {
        throw "Authenticode-Zeitstempel fehlt: $Path"
    }
    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        $actual = ([System.BitConverter]::ToString($sha256.ComputeHash($signature.SignerCertificate.RawData))).Replace('-', '').ToLowerInvariant()
    } finally {
        $sha256.Dispose()
    }
    if ($actual -ne $ExpectedCertificateSHA256) {
        throw "Herausgeberzertifikat stimmt nicht: $actual statt $ExpectedCertificateSHA256"
    }
}

$root = Split-Path -Parent $PSScriptRoot
$gui = Join-Path $root 'cmd/lanready-gui'
$config = Get-Content (Join-Path $gui 'wails.json') -Raw | ConvertFrom-Json
if ($config.info.productVersion -ne $Version) {
    throw "Version $Version stimmt nicht mit wails.json productVersion $($config.info.productVersion) überein."
}

$ldflags = "-s -w -X main.version=$Version -X main.releasePublicKey=$ReleasePublicKey -X main.authenticodePublisherSHA256=$PublisherCertificateSHA256"
Push-Location $gui
try {
    & $Wails build -platform windows/amd64 -s -skipbindings -skipembedcreate -m -trimpath -webview2 download -ldflags $ldflags -o LANReady.exe
    if ($LASTEXITCODE -ne 0) { throw 'Wails-Build fehlgeschlagen.' }

    $exe = Join-Path $gui 'build/bin/LANReady.exe'
    & $SignTool sign /sha1 $CertificateThumbprint /fd SHA256 /tr $TimestampUrl /td SHA256 /v $exe
    if ($LASTEXITCODE -ne 0) { throw 'Authenticode-Signatur der Client-EXE fehlgeschlagen.' }
    & $SignTool verify /pa /all /v $exe
    if ($LASTEXITCODE -ne 0) { throw 'Authenticode-Verifikation der Client-EXE fehlgeschlagen.' }
    Assert-LANReadySignature -Path $exe -ExpectedCertificateSHA256 $PublisherCertificateSHA256

    $signCommand = "`"$SignTool`" sign /sha1 $CertificateThumbprint /fd SHA256 /tr `"$TimestampUrl`" /td SHA256 /v"
    Push-Location (Join-Path $gui 'build/windows/installer')
    try {
        & $MakeNSIS '-DARG_WAILS_AMD64_BINARY=..\..\bin\LANReady.exe' '-DWAILS_INSTALL_SCOPE=user' '-DREQUEST_EXECUTION_LEVEL=user' "-DLANREADY_SIGN_COMMAND=$signCommand" project.nsi
        if ($LASTEXITCODE -ne 0) { throw 'NSIS-Paketierung fehlgeschlagen.' }
    } finally {
        Pop-Location
    }

    $installer = Join-Path $gui 'build/bin/LANReady-amd64-installer.exe'
    & $SignTool verify /pa /all /v $installer
    if ($LASTEXITCODE -ne 0) { throw 'Authenticode-Verifikation des Installers fehlgeschlagen.' }
    Assert-LANReadySignature -Path $installer -ExpectedCertificateSHA256 $PublisherCertificateSHA256
} finally {
    Pop-Location
}

$dist = Join-Path $root 'dist'
New-Item -ItemType Directory -Force $dist | Out-Null
$releaseTool = Join-Path $dist 'lanready-release.exe'
& $Go build -trimpath -o $releaseTool ./cmd/lanready-release
if ($LASTEXITCODE -ne 0) { throw 'Windows-Releasewerkzeug konnte nicht gebaut werden.' }
$setup = Join-Path $dist "LANReady-$Version-windows-x64-setup.exe"
Copy-Item (Join-Path $gui 'build/bin/LANReady-amd64-installer.exe') $setup -Force
& $Go run ./cmd/lanready-package -exe (Join-Path $gui 'build/bin/LANReady.exe') -readme (Join-Path $gui 'PORTABLE-README.txt') -out (Join-Path $dist "LANReady-$Version-windows-x64-portable.zip") -setup $setup -manifest (Join-Path $dist 'SHA256SUMS.txt')
if ($LASTEXITCODE -ne 0) { throw 'Portable Paketierung fehlgeschlagen.' }
