param(
    [string]$Config = "",
    [switch]$Remote,
    [string]$TargetComputer = ""
)

Import-Module "$PSScriptRoot\modules\core.psm1" -Force

if ($Config -ne "") {
    $json = Get-Content $Config -Raw | ConvertFrom-Json
    $installPath = $json.installPath
    $spiele = $json.spiele
    $launcher = $json.launcher
    $isos = $json.isos

    if ($Remote -and $TargetComputer) {
        Invoke-RemoteInstall -ComputerName $TargetComputer -Launcher $launcher -Spiele $spiele -ISOs $isos -InstallPath $installPath
    } else {
        Install-Launchers -launcher $launcher -installPath $installPath
        Copy-Spiele -spiele $spiele -installPath $installPath
        Install-ISOs -isos $isos -installPath $installPath
        Check-GPUAndSuggestDriver
    }
} else {
    Show-ElectronGUI
}
