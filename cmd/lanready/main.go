package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/containerguy/lan_installer/internal/client"
	"github.com/containerguy/lan_installer/internal/deviceclient"
	"github.com/containerguy/lan_installer/internal/discovery"
	"github.com/containerguy/lan_installer/internal/signing"
	"github.com/containerguy/lan_installer/internal/windowsupdate"
)

var version = "0.0.0-dev"

type gameFlags []string

func (g *gameFlags) String() string { return strings.Join(*g, ",") }
func (g *gameFlags) Set(value string) error {
	*g = append(*g, value)
	return nil
}

func main() {
	defaultRoot := filepath.Join(".", "LAN-Games")
	if runtime.GOOS == "windows" {
		defaultRoot = `C:\LAN-Games`
	}
	var selected gameFlags
	serverURL := flag.String("server", "", "URL des LANReady-Servers")
	eventID := flag.String("event", "", "Veranstaltungs-ID")
	token := flag.String("token", os.Getenv("LANREADY_TOKEN"), "API-Token (alternativ LANREADY_TOKEN)")
	publicKeyPath := flag.String("public-key", "lanready-public.key", "Ed25519-Public-Key")
	targetRoot := flag.String("target-root", defaultRoot, "Basisverzeichnis für Spiele")
	mode := flag.String("mode", "portable", "portable oder managed")
	yes := flag.Bool("yes", false, "alle ausgewählten Änderungen bestätigen")
	dryRun := flag.Bool("dry-run", false, "nur prüfen, nichts herunterladen")
	windowsUpdates := flag.String("windows-update", "off", "off, check oder install")
	showVersion := flag.Bool("version", false, "Version ausgeben")
	discoverInstalled := flag.Bool("discover-installed", false, "installierte Steam-, EA-App- und Ubisoft-Connect-Spiele lokal erkennen")
	enrollmentCode := flag.String("enrollment-code", "", "einmaliger Code zum Registrieren dieses Windows-PCs")
	syncInventory := flag.Bool("sync-inventory", false, "installierte Spiele nach persönlicher Browser-Anmeldung synchronisieren")
	deviceProfile := flag.String("device-profile", defaultDeviceProfile(), "lokales, benutzergebunden geschütztes Geräteprofil")
	openBrowser := flag.Bool("open-browser", true, "Browser für persönliche Anmeldung automatisch öffnen")
	flag.Var(&selected, "game", "nur diese Game-ID synchronisieren; mehrfach möglich")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *discoverInstalled {
		result, err := discovery.Discover()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Spieleerkennung fehlgeschlagen: %v\n", err)
			os.Exit(1)
		}
		encoded, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(encoded))
		return
	}
	if *enrollmentCode != "" {
		if *serverURL == "" {
			fmt.Fprintln(os.Stderr, "-server ist für Enrollment erforderlich")
			os.Exit(2)
		}
		hostname, _ := os.Hostname()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		profile, err := deviceclient.Enroll(ctx, *serverURL, *enrollmentCode, hostname, windowsupdate.OSVersion(ctx), version, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Enrollment fehlgeschlagen: %v\n", err)
			os.Exit(1)
		}
		if err = deviceclient.SaveProfile(*deviceProfile, profile); err != nil {
			fmt.Fprintf(os.Stderr, "Geräteprofil konnte nicht gespeichert werden: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Gerät %s wurde registriert.\n", profile.DeviceID)
		return
	}
	if *syncInventory {
		if err := runInventorySync(*deviceProfile, *yes, *openBrowser); err != nil {
			fmt.Fprintf(os.Stderr, "Inventarsynchronisation fehlgeschlagen: %v\n", err)
			os.Exit(1)
		}
		return
	}
	if *serverURL == "" || *eventID == "" {
		fmt.Fprintln(os.Stderr, "-server und -event sind erforderlich")
		os.Exit(2)
	}
	if *mode != "portable" && *mode != "managed" {
		fmt.Fprintln(os.Stderr, "-mode muss portable oder managed sein")
		os.Exit(2)
	}
	if *windowsUpdates != "off" && *windowsUpdates != "check" && *windowsUpdates != "install" {
		fmt.Fprintln(os.Stderr, "-windows-update muss off, check oder install sein")
		os.Exit(2)
	}
	publicKey, err := signing.ReadPublicKey(*publicKeyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Public Key konnte nicht gelesen werden: %v\n", err)
		os.Exit(2)
	}
	selectedMap := make(map[string]bool, len(selected))
	for _, id := range selected {
		selectedMap[id] = true
	}
	runner, err := client.New(client.Config{
		ServerURL:      *serverURL,
		EventID:        *eventID,
		Token:          *token,
		PublicKey:      publicKey,
		TargetRoot:     *targetRoot,
		Mode:           *mode,
		Yes:            *yes,
		DryRun:         *dryRun,
		SelectedGames:  selectedMap,
		WindowsUpdates: *windowsUpdates,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*time.Hour)
	defer cancel()
	report, err := runner.Run(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "LANReady fehlgeschlagen: %v\n", err)
		os.Exit(1)
	}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
	if !report.Ready {
		os.Exit(3)
	}
}

func defaultDeviceProfile() string {
	root, err := os.UserConfigDir()
	if err != nil || root == "" {
		return "lanready-device.json"
	}
	return filepath.Join(root, "LANReady", "device.json")
}

func runInventorySync(profilePath string, yes, openBrowser bool) error {
	profile, err := deviceclient.LoadProfile(profilePath)
	if err != nil {
		return fmt.Errorf("Geräteprofil laden: %w", err)
	}
	profile.ClientVersion = version
	result, err := discovery.Discover()
	if err != nil {
		return err
	}
	preview, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(preview))
	if !yes {
		fmt.Printf("%d Installation(en) mit dem Managementserver synchronisieren? [j/N]: ", len(result.Installations))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "j" && answer != "ja" && answer != "y" && answer != "yes" {
			return errors.New("vom Benutzer abgelehnt")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	device := &deviceclient.Client{Profile: profile}
	authorization, err := device.StartAuthorization(ctx)
	if err != nil {
		return err
	}
	verificationURL := authorization.VerificationURL + "?code=" + url.QueryEscape(authorization.UserCode)
	fmt.Printf("Anmeldung: %s\nCode: %s\n", verificationURL, authorization.UserCode)
	if openBrowser {
		if browserErr := deviceclient.OpenBrowser(verificationURL); browserErr != nil {
			fmt.Fprintf(os.Stderr, "Browser konnte nicht automatisch geöffnet werden: %v\n", browserErr)
		}
	}
	interval := time.Duration(authorization.PollIntervalSeconds) * time.Second
	if interval < time.Second {
		interval = 5 * time.Second
	}
	var accessToken string
	for {
		select {
		case <-ctx.Done():
			return errors.New("Anmeldung ist abgelaufen")
		case <-time.After(interval):
		}
		accessToken, err = device.PollAuthorization(ctx, authorization.AuthorizationID)
		if errors.Is(err, deviceclient.ErrAuthorizationPending) || errors.Is(err, deviceclient.ErrAuthorizationSlowDown) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	randomID := make([]byte, 16)
	if _, err = rand.Read(randomID); err != nil {
		return err
	}
	if err = device.UploadInventory(ctx, accessToken, base64.RawURLEncoding.EncodeToString(randomID), result); err != nil {
		return err
	}
	fmt.Println("Spieleinventar wurde synchronisiert.")
	return nil
}
