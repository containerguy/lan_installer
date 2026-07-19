package selfupdate

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/containerguy/lan_installer/internal/deviceclient"
)

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type ApplyRequest struct {
	Target, ProfilePath, MarkerPath, ApplyReadyPath, Version, SHA256, PublisherSHA256 string
	Sequence, ParentPID, Size                                                         int64
}

type HealthRequest struct {
	ProfilePath, MarkerPath, Version string
	Sequence                         int64
}

func ParseApplyArgs(args []string) (ApplyRequest, bool, error) {
	var out ApplyRequest
	if len(args) == 0 || args[0] != "--lanready-apply-update" {
		return out, false, nil
	}
	flags := flag.NewFlagSet("lanready-apply-update", flag.ContinueOnError)
	flags.StringVar(&out.Target, "target", "", "")
	flags.StringVar(&out.ProfilePath, "profile", "", "")
	flags.StringVar(&out.MarkerPath, "marker", "", "")
	flags.StringVar(&out.Version, "version", "", "")
	flags.Int64Var(&out.Sequence, "sequence", 0, "")
	flags.Int64Var(&out.ParentPID, "parent-pid", 0, "")
	flags.Int64Var(&out.Size, "size", 0, "")
	flags.StringVar(&out.SHA256, "sha256", "", "")
	flags.StringVar(&out.PublisherSHA256, "publisher-sha256", "", "")
	flags.StringVar(&out.ApplyReadyPath, "apply-ready", "", "")
	if err := flags.Parse(args[1:]); err != nil {
		return out, true, err
	}
	if out.Target == "" || out.ProfilePath == "" || out.MarkerPath == "" || out.ApplyReadyPath == "" || out.Version == "" || out.Sequence < 1 || out.ParentPID < 1 || out.Size < 1 || !digestPattern.MatchString(out.SHA256) || !digestPattern.MatchString(out.PublisherSHA256) || !filepath.IsAbs(out.Target) || !filepath.IsAbs(out.ProfilePath) || !filepath.IsAbs(out.MarkerPath) || !filepath.IsAbs(out.ApplyReadyPath) {
		return out, true, errors.New("update apply request is incomplete")
	}
	return out, true, nil
}

func ParseHealthArgs(args []string) (HealthRequest, bool, error) {
	var out HealthRequest
	if len(args) == 0 || args[0] != "--lanready-update-health" {
		return out, false, nil
	}
	flags := flag.NewFlagSet("lanready-update-health", flag.ContinueOnError)
	flags.StringVar(&out.ProfilePath, "profile", "", "")
	flags.StringVar(&out.MarkerPath, "marker", "", "")
	flags.StringVar(&out.Version, "version", "", "")
	flags.Int64Var(&out.Sequence, "sequence", 0, "")
	if err := flags.Parse(args[1:]); err != nil {
		return out, true, err
	}
	if out.ProfilePath == "" || out.MarkerPath == "" || out.Version == "" || out.Sequence < 1 || !filepath.IsAbs(out.ProfilePath) || !filepath.IsAbs(out.MarkerPath) {
		return out, true, errors.New("update health request is incomplete")
	}
	return out, true, nil
}

func NewMarkerPath(profilePath string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(profilePath), "update-health-"+hex.EncodeToString(random)+".ok"), nil
}

func SignalApplyReady(request ApplyRequest) error {
	return writeMarker(request.ApplyReadyPath, request.Sequence)
}

func CommitHealthy(request HealthRequest) error {
	profile, err := deviceclient.LoadProfile(request.ProfilePath)
	if err != nil {
		return err
	}
	if request.Sequence < profile.UpdateSequence {
		return errors.New("update sequence moved backwards")
	}
	profile.UpdateSequence = request.Sequence
	profile.ClientVersion = request.Version
	if err = deviceclient.SaveProfile(request.ProfilePath, profile); err != nil {
		return err
	}
	return nil
}

func SignalHealthy(request HealthRequest) error {
	return writeMarker(request.MarkerPath, request.Sequence)
}

func writeMarker(path string, sequence int64) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(strconv.FormatInt(sequence, 10)+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}
