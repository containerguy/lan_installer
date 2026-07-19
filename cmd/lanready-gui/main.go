//go:build windows

package main

import (
	"crypto/ed25519"
	"embed"
	"encoding/base64"
	"io/fs"
	"log"
	"os"
	"strings"

	"github.com/containerguy/lan_installer/internal/selfupdate"
	"github.com/containerguy/lan_installer/internal/windowsapp"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

var version = "0.0.0-dev"
var releasePublicKey = ""
var authenticodePublisherSHA256 = ""

//go:embed all:frontend/dist
var frontend embed.FS

func main() {
	agentMode := len(os.Args) == 2 && os.Args[1] == "--lanready-agent"
	applyRequest, applyMode, err := selfupdate.ParseApplyArgs(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if applyMode {
		if applyRequest.Version != version || applyRequest.PublisherSHA256 != strings.ToLower(strings.TrimSpace(authenticodePublisherSHA256)) {
			log.Fatal("Update-Metadaten passen nicht zur kompilierten Clientversion")
		}
		if err = selfupdate.SignalApplyReady(applyRequest); err != nil {
			log.Fatal(err)
		}
		if err = selfupdate.RunApply(applyRequest); err != nil {
			log.Fatal(err)
		}
		return
	}
	healthRequest, healthMode, err := selfupdate.ParseHealthArgs(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if healthMode && healthRequest.Version != version {
		log.Fatal("Health-Check-Version passt nicht zur kompilierten Clientversion")
	}
	assets, err := fs.Sub(frontend, "frontend/dist")
	if err != nil {
		log.Fatal(err)
	}
	keyBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(releasePublicKey))
	if err != nil || len(keyBytes) != 32 {
		log.Fatal("Release-Public-Key fehlt oder ist ungültig")
	}
	backend := windowsapp.New("", version, ed25519.PublicKey(keyBytes))
	backend.SetExpectedPublisherSHA256(authenticodePublisherSHA256)
	backend.SetAgentMode(agentMode)
	if healthMode {
		backend.SetHealthRequest(healthRequest)
	}
	err = wails.Run(&options.App{
		Title:              "LANReady",
		Width:              1220,
		Height:             800,
		MinWidth:           920,
		MinHeight:          640,
		BackgroundColour:   options.NewRGB(8, 14, 29),
		StartHidden:        agentMode,
		AssetServer:        &assetserver.Options{Assets: assets},
		Bind:               []interface{}{backend},
		SingleInstanceLock: &options.SingleInstanceLock{UniqueId: "info.familie-keller.lanready", OnSecondInstanceLaunch: func(options.SecondInstanceData) { backend.ShowWindow() }},
		OnStartup:          backend.OnStartup,
		OnBeforeClose:      backend.OnBeforeClose,
		Windows: &windows.Options{
			Theme:                windows.Dark,
			BackdropType:         windows.Mica,
			DisablePinchZoom:     true,
			IsZoomControlEnabled: false,
			EnableSwipeGestures:  false,
			DLLSearchPaths:       windows.DLLSearchApplicationDir | windows.DLLSearchSystem32,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			ContentProtection:    false,
			Messages: &windows.Messages{
				InstallationRequired: "LANReady benötigt die Microsoft WebView2-Laufzeit. Jetzt herunterladen und installieren?",
				UpdateRequired:       "Die Microsoft WebView2-Laufzeit muss aktualisiert werden. Jetzt aktualisieren?",
				MissingRequirements:  "Komponente fehlt",
				Webview2NotInstalled: "Microsoft WebView2 ist nicht installiert",
				Error:                "LANReady-Fehler",
				FailedToInstall:      "WebView2 konnte nicht installiert werden. Bitte den Microsoft-Installer manuell ausführen.",
				DownloadPage:         "LANReady benötigt Microsoft WebView2. OK öffnet die Downloadseite. Mindestversion: ",
				PressOKToInstall:     "Mit OK installieren.",
				ContactAdmin:         "WebView2 wird benötigt. Bitte den Administrator kontaktieren.",
				InvalidFixedWebview2: "Die konfigurierte WebView2-Laufzeit ist ungültig.",
				WebView2ProcessCrash: "WebView2 wurde unerwartet beendet. LANReady muss neu gestartet werden.",
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
