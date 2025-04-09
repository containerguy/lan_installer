$gpu = Get-WmiObject win32_VideoController | Select-Object -ExpandProperty Name
Write-Host "Gefundene GPU: $gpu"

if ($gpu -match "NVIDIA") {
    Start-Process "https://www.nvidia.com/Download/index.aspx"
}
elseif ($gpu -match "AMD") {
    Start-Process "https://www.amd.com/de/support"
}
elseif ($gpu -match "Intel") {
    Start-Process "https://www.intel.de/content/www/de/de/download-center/home.html"
} else {
    Write-Host "GPU-Hersteller nicht erkannt."
}
