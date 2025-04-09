# LAN Installer

![Renovate enabled](https://img.shields.io/badge/dependencies-up%20to%20date-brightgreen?logo=renovatebot)
[![Known Vulnerabilities](https://snyk.io/test/github/containerguy/lan_installer/badge.svg)](https://snyk.io/test/github/containerguy/lan_installer)

Dieses Tool ermöglicht es, einen Windows-PC vollständig für eine LAN-Party vorzubereiten – inklusive Installation von Game-Launchern, Spielen (per Kopie oder ISO), GPU-Erkennung und Treiberinstallation.

## 🔧 Features

- Electron-basierte GUI zur komfortablen Auswahl
- Dynamische Spieleerkennung aus `spiele/`
- ISO-Auswahl und automatisierte Installation
- Automatische GPU-Erkennung (NVIDIA / AMD / Intel)
- Download-Link zum passenden Treiber
- Silent-Installation für Game-Launcher
- Auswahl des Installationspfads
- Fortschrittsbalken möglich (GUI-Erweiterung)
- Unterstützung für Remote-Installation via PowerShell Remoting
- Konfigurierbar über GUI oder `config.yaml`

## ▶️ Verwendung

1. Entpacken auf Ziel-PC oder Netzwerklaufwerk
2. Führe `install.ps1` aus:
   - Ohne Parameter → Electron GUI startet
   - Mit Parameter `-config config.yaml` → automatische Installation
3. Genieße deine LAN-Party 🎉

## 💡 Hinweis

- PowerShell Execution Policy muss ggf. auf `Bypass` gestellt werden.
- Electron benötigt `npm install` im `electron-app/` Ordner.
