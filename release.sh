#!/bin/bash
# Automatisierter Release-Prozess für LAN Installer (Linux/Mac)
set -e

if [ -z "$1" ]; then
  echo "Verwendung: ./release.sh <neue-version> (z. B. 1.0.1)"
  exit 1
fi

VERSION=$1

echo "🔄 Setze Version auf v$VERSION"

# Versionsnummer in package.json setzen
cd electron-app
npm version $VERSION --no-git-tag-version
cd ..

# CHANGELOG aktualisieren
npx auto-changelog -p

# Git-Commit & Tag
git add .
git commit -m "Release v$VERSION"
git tag v$VERSION
git push origin main --tags

echo "✅ Release v$VERSION vorbereitet und gepusht."
