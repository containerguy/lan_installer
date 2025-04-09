function Install-Launchers {
    param($launcher, $installPath)
    foreach ($l in $launcher) {
        $exe = Join-Path -Path "launcher" -ChildPath "$l.exe"
        if (Test-Path $exe) {
            Write-Host "Installiere $l..."
            Start-Process -FilePath $exe -ArgumentList "/S" -Wait
        }
    }
}

function Copy-Spiele {
    param($spiele, $installPath)
    foreach ($s in $spiele) {
        $source = $s.path
        $target = Join-Path $installPath $s.name
        Write-Host "Kopiere Spiel: $($s.name)"
        Copy-Item -Path $source -Destination $target -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Install-ISOs {
    param($isos, $installPath)
    foreach ($iso in $isos) {
        Write-Host "Installiere von ISO: $($iso.name)"
        $mount = Mount-DiskImage -ImagePath $iso.path -PassThru
        Start-Sleep -Seconds 2
        $drive = ($mount | Get-Volume).DriveLetter + ":"
        $setup = Get-ChildItem "$drive\setup.exe" -ErrorAction SilentlyContinue
        if ($setup) {
            Start-Process -FilePath $setup.FullName -ArgumentList "/SILENT" -Wait
        }
        Dismount-DiskImage -ImagePath $iso.path
    }
}

function Check-GPUAndSuggestDriver {
    Write-Host "Starte GPU-Erkennung..."
    powershell -ExecutionPolicy Bypass -File "$PSScriptRoot\..\scripts\gpu-check.ps1"
}

function Invoke-RemoteInstall {
    param($ComputerName, $Launcher, $Spiele, $ISOs, $InstallPath)
    Invoke-Command -ComputerName $ComputerName -ScriptBlock {
        param($Launcher, $Spiele, $ISOs, $InstallPath)
        Import-Module "$using:PSScriptRoot\core.psm1"
        Install-Launchers -launcher $Launcher -installPath $InstallPath
        Copy-Spiele -spiele $Spiele -installPath $InstallPath
        Install-ISOs -isos $ISOs -installPath $InstallPath
        Check-GPUAndSuggestDriver
    } -ArgumentList $Launcher, $Spiele, $ISOs, $InstallPath
}

function Show-ElectronGUI {
    Write-Host "Starte Electron GUI..."
    Start-Process "npm" -ArgumentList "start" -WorkingDirectory "$PSScriptRoot\..\electron-app"
}
