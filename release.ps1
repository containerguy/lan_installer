param (
    [string]$version
)

if (-not $version) {
    Write-Host "Verwendung: .\release.ps1 -version 1.0.1"
    exit 1
}

Write-Host "🔄 Setze Version auf v$version"

Set-Location electron-app
npm version $version --no-git-tag-version
Set-Location ..

npx auto-changelog -p

git add .
git commit -m "Release v$version"
git tag "v$version"
git push origin main --tags

Write-Host "✅ Release v$version vorbereitet und gepusht."
