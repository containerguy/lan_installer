param(
    [string]$Config = "",
    [switch]$Remote,
    [string]$TargetComputer = ""
)

Import-Module "$PSScriptRoot\modules\core.psm1" -Force

if ($Config -ne "") {
    $yaml = ConvertFrom-Yaml (Get-Content $Config -Raw)
    $installPath = $yaml.install_path
    $spiele = $yaml.spiele
    $launcher = $yaml.launcher
    $isos = $yaml.isos

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
