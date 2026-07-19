package main

import (
	"context"
	"crypto/ed25519"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/artifact"
	"github.com/containerguy/lan_installer/internal/auth"
	"github.com/containerguy/lan_installer/internal/cacheworker"
	"github.com/containerguy/lan_installer/internal/deviceapi"
	"github.com/containerguy/lan_installer/internal/protocol"
	lanrelease "github.com/containerguy/lan_installer/internal/release"
	"github.com/containerguy/lan_installer/internal/secretbox"
	lanserver "github.com/containerguy/lan_installer/internal/server"
	"github.com/containerguy/lan_installer/internal/signing"
	"github.com/containerguy/lan_installer/internal/sourceprobe"
	"github.com/containerguy/lan_installer/internal/store"
	"github.com/containerguy/lan_installer/internal/webadmin"
)

var version = "0.0.0-dev"

func main() {
	listen := flag.String("listen", ":8080", "Listen-Adresse")
	dataDir := flag.String("data", "./data", "Datenverzeichnis")
	clientToken := flag.String("client-token", os.Getenv("LANREADY_CLIENT_TOKEN"), "Client-Token (alternativ LANREADY_CLIENT_TOKEN)")
	adminToken := flag.String("admin-token", os.Getenv("LANREADY_ADMIN_TOKEN"), "Admin-Token (alternativ LANREADY_ADMIN_TOKEN)")
	databasePath := flag.String("database", envOr("LANREADY_DATABASE", ""), "SQLite-Datenbank; Standard: <data>/lanready.db")
	cacheRoot := flag.String("cache-root", os.Getenv("LANREADY_CACHE_ROOT"), "CAS-Verzeichnis; Standard: <data>/artifacts")
	cacheQuotaBytes := flag.String("cache-quota-bytes", envOr("LANREADY_CACHE_QUOTA_BYTES", "10737418240"), "Harte Gesamtgröße des CAS in Bytes")
	cacheVolumeID := flag.String("cache-volume-id", os.Getenv("LANREADY_CACHE_VOLUME_ID"), "Erwartete ID aus <cache-root>/.lanready-cache-volume; leer deaktiviert die Mountprüfung")
	webAdminUser := flag.String("web-admin-user", envOr("LANREADY_WEB_ADMIN_USER", "admin"), "Initialer Web-Admin")
	webAdminPasswordFile := flag.String("web-admin-password-file", os.Getenv("LANREADY_WEB_ADMIN_PASSWORD_FILE"), "Datei mit initialem Admin-Passwort")
	webDAVMasterKeyFile := flag.String("webdav-master-key-file", os.Getenv("LANREADY_WEBDAV_MASTER_KEY_FILE"), "Datei mit 256-Bit-Masterschlüssel für WebDAV-Secrets")
	sourcePrivateAllowlist := flag.String("source-private-allowlist", os.Getenv("LANREADY_SOURCE_PRIVATE_ALLOWLIST"), "Private Source-Ziele als host=cidr,cidr;host=cidr")
	secureCookies := flag.Bool("secure-cookies", envOr("LANREADY_SECURE_COOKIES", "true") != "false", "Cookies ausschließlich über HTTPS senden")
	publicURL := flag.String("public-url", os.Getenv("LANREADY_PUBLIC_URL"), "Öffentliche HTTPS-URL für die Browser-Anmeldung")
	trustedProxyCIDRs := flag.String("trusted-proxy-cidrs", os.Getenv("LANREADY_TRUSTED_PROXY_CIDRS"), "Vertrauenswürdige Reverse-Proxy-CIDRs für X-Forwarded-For")
	releasePublicKeyFiles := flag.String("release-public-key-files", os.Getenv("LANREADY_RELEASE_PUBLIC_KEY_FILES"), "Kommagetrennte Ed25519-Public-Key-Dateien für signierte Releases")
	releaseSchemaDir := flag.String("release-schema-dir", envOr("LANREADY_RELEASE_SCHEMA_DIR", "/usr/share/lanready/schemas"), "Verzeichnis der verbindlichen Release-JSON-Schemata")
	authenticodePublisherSHA256 := flag.String("authenticode-publisher-sha256", os.Getenv("LANREADY_AUTHENTICODE_PUBLISHER_SHA256"), "SHA-256 des erwarteten Authenticode-Herausgeberzertifikats")
	showVersion := flag.Bool("version", false, "Version ausgeben")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	var adminHandler http.Handler
	var deviceHandler http.Handler
	var database *store.Store
	if *webAdminPasswordFile != "" {
		if err := os.MkdirAll(*dataDir, 0o755); err != nil {
			log.Fatal(err)
		}
		if *databasePath == "" {
			*databasePath = filepath.Join(*dataDir, "lanready.db")
		}
		if *cacheRoot == "" {
			*cacheRoot = filepath.Join(*dataDir, "artifacts")
		}
		var err error
		database, err = store.Open(*databasePath)
		if err != nil {
			log.Fatal(err)
		}
		defer database.Close()
		secret, err := os.ReadFile(*webAdminPasswordFile)
		if err != nil {
			log.Fatalf("Admin-Passwortdatei lesen: %v", err)
		}
		password := strings.TrimSpace(string(secret))
		hash, err := auth.HashPassword(password)
		if err != nil {
			log.Fatalf("Admin-Passwort: %v", err)
		}
		created, err := database.BootstrapAdmin(context.Background(), *webAdminUser, hash)
		if err != nil {
			log.Fatal(err)
		}
		if created {
			log.Printf("initialer Web-Admin %q wurde angelegt", *webAdminUser)
		}
		if *webDAVMasterKeyFile == "" {
			log.Fatal("LANREADY_WEBDAV_MASTER_KEY_FILE ist für die Web-UI erforderlich")
		}
		vault, err := secretbox.FromFile(*webDAVMasterKeyFile)
		if err != nil {
			log.Fatalf("WebDAV-Masterschlüssel: %v", err)
		}
		privateAllow, err := sourceprobe.ParsePrivateAllowlist(*sourcePrivateAllowlist)
		if err != nil {
			log.Fatalf("Source-Allowlist: %v", err)
		}
		probe := sourceprobe.New(sourceprobe.Policy{PrivateAllow: privateAllow})
		if *publicURL == "" {
			log.Fatal("LANREADY_PUBLIC_URL ist für Windows-Client-Anmeldung und Inventarsynchronisation erforderlich")
		}
		cacheQuota, err := strconv.ParseInt(strings.TrimSpace(*cacheQuotaBytes), 10, 64)
		if err != nil || cacheQuota < 1 {
			log.Fatal("LANREADY_CACHE_QUOTA_BYTES muss eine positive Bytegröße sein")
		}
		if err = verifyCacheVolume(*cacheRoot, *cacheVolumeID); err != nil {
			log.Fatalf("Cache-Volume prüfen: %v", err)
		}
		artifactStore, err := artifact.NewWithQuota(*cacheRoot, database, cacheQuota)
		if err != nil {
			log.Fatalf("Artefaktspeicher: %v", err)
		}
		worker, err := cacheworker.New(database, artifactStore, probe, vault, cacheQuota)
		if err != nil {
			log.Fatalf("Cache-Worker: %v", err)
		}
		if err = worker.Start(context.Background()); err != nil {
			log.Fatalf("Cache-Worker starten: %v", err)
		}
		var adminOptions = []webadmin.Option{webadmin.WithSourceTester(probe), webadmin.WithArtifactStore(artifactStore)}
		if strings.TrimSpace(*releasePublicKeyFiles) != "" {
			validator, validatorErr := protocol.NewReleaseValidator(*releaseSchemaDir)
			if validatorErr != nil {
				log.Fatalf("Release-Schemata: %v", validatorErr)
			}
			trustedKeys := make(map[string]ed25519.PublicKey)
			for _, keyFile := range strings.Split(*releasePublicKeyFiles, ",") {
				publicKey, keyErr := signing.ReadPublicKey(strings.TrimSpace(keyFile))
				if keyErr != nil {
					log.Fatalf("Release-Public-Key: %v", keyErr)
				}
				trustedKeys[protocol.KeyID(publicKey)] = publicKey
			}
			releaseService, serviceErr := lanrelease.New(database, validator, trustedKeys, artifactStore, *authenticodePublisherSHA256)
			if serviceErr != nil {
				log.Fatalf("Release-Service: %v", serviceErr)
			}
			adminOptions = append(adminOptions, webadmin.WithReleaseService(releaseService))
		} else {
			log.Print("Release-Publishing deaktiviert: LANREADY_RELEASE_PUBLIC_KEY_FILES ist nicht gesetzt")
		}
		adminHandler = webadmin.New(database, *secureCookies, vault, adminOptions...)
		deviceHandler, err = deviceapi.NewWithArtifactStore(database, *publicURL, artifactStore, *trustedProxyCIDRs)
		if err != nil {
			log.Fatalf("öffentliche URL: %v", err)
		}
	} else {
		log.Print("Web-UI deaktiviert: LANREADY_WEB_ADMIN_PASSWORD_FILE ist nicht gesetzt")
	}

	app, err := lanserver.NewWithHandlers(*dataDir, *clientToken, *adminToken, adminHandler, deviceHandler)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Minute, IdleTimeout: 2 * time.Minute}
	log.Printf("LANReady server %s listening on %s", version, *listen)
	log.Fatal(server.ListenAndServe())
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
