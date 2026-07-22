//go:build windows

package selfupdate

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/containerguy/lan_installer/internal/deviceclient"
	"golang.org/x/sys/windows"
)

var (
	winTrust                    = windows.NewLazySystemDLL("wintrust.dll")
	procWinVerifyTrust          = winTrust.NewProc("WinVerifyTrust")
	crypt32                     = windows.NewLazySystemDLL("crypt32.dll")
	procCryptMsgGetParam        = crypt32.NewProc("CryptMsgGetParam")
	procCryptMsgClose           = crypt32.NewProc("CryptMsgClose")
	winTrustActionGenericVerify = windows.GUID{Data1: 0x00aac56b, Data2: 0xcd44, Data3: 0x11d0, Data4: [8]byte{0x8c, 0xc2, 0x00, 0xc0, 0x4f, 0xc2, 0x95, 0xee}}
)

const (
	winTrustNoUI                 = 2
	winTrustRevokeWholeChain     = 1
	winTrustChoiceFile           = 1
	winTrustStateActionVerify    = 1
	winTrustStateActionClose     = 2
	winTrustRevocationCheckChain = 0x00000040
)

type winTrustFileInfo struct {
	Size         uint32
	FilePath     *uint16
	File         windows.Handle
	KnownSubject *windows.GUID
}

type winTrustData struct {
	Size              uint32
	PolicyData        uintptr
	SIPClientData     uintptr
	UIChoice          uint32
	RevocationChecks  uint32
	UnionChoice       uint32
	FileInfo          uintptr
	StateAction       uint32
	StateData         windows.Handle
	URLReference      uintptr
	ProviderFlags     uint32
	UIContext         uint32
	SignatureSettings uintptr
}

type cryptSignerInfoPrefix struct {
	Version      uint32
	Issuer       windows.CertNameBlob
	SerialNumber windows.CryptIntegerBlob
}

func Launch(update deviceclient.PreparedUpdate, target, profilePath, expectedPublisherSHA256 string) error {
	if update.PublisherCertificateSHA256 != expectedPublisherSHA256 || !digestPattern.MatchString(expectedPublisherSHA256) {
		return errors.New("signierter Update-Herausgeber stimmt nicht mit diesem Clientbuild überein")
	}
	if err := VerifyAuthenticode(update.StagedPath, expectedPublisherSHA256); err != nil {
		return fmt.Errorf("Authenticode-Signatur des Updates pr\u00fcfen: %w", err)
	}
	marker, err := NewMarkerPath(profilePath)
	if err != nil {
		return err
	}
	applyReady, err := NewMarkerPath(profilePath)
	if err != nil {
		return err
	}
	command := exec.Command(update.StagedPath,
		"--lanready-apply-update",
		"--target", target,
		"--profile", profilePath,
		"--marker", marker,
		"--version", update.Version,
		"--sequence", strconv.FormatInt(update.Sequence, 10),
		"--parent-pid", strconv.Itoa(os.Getpid()),
		"--size", strconv.FormatInt(update.Size, 10),
		"--sha256", update.SHA256,
		"--publisher-sha256", expectedPublisherSHA256,
		"--apply-ready", applyReady,
	)
	command.Dir = filepath.Dir(update.StagedPath)
	if err = command.Start(); err != nil {
		return fmt.Errorf("Updater starten: %w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if content, readErr := os.ReadFile(applyReady); readErr == nil && string(content) == strconv.FormatInt(update.Sequence, 10)+"\n" {
			_ = os.Remove(applyReady)
			return command.Process.Release()
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
	_ = os.Remove(applyReady)
	return errors.New("Updateartefakt hat das Updater-Protokoll nicht bestätigt")
}

// VerifyAuthenticode applies the Windows generic Authenticode policy to an
// embedded executable signature. The Ed25519 release envelope still pins the
// exact artifact digest; this second check makes Windows publisher trust a
// mandatory, independent condition before replacement.
func VerifyAuthenticode(path, expectedPublisherSHA256 string) error {
	if !digestPattern.MatchString(expectedPublisherSHA256) {
		return errors.New("erwarteter Authenticode-Herausgeber fehlt oder ist ungültig")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	filePath, err := windows.UTF16PtrFromString(absolute)
	if err != nil {
		return err
	}
	file := winTrustFileInfo{Size: uint32(unsafe.Sizeof(winTrustFileInfo{})), FilePath: filePath}
	data := winTrustData{
		Size:             uint32(unsafe.Sizeof(winTrustData{})),
		UIChoice:         winTrustNoUI,
		RevocationChecks: winTrustRevokeWholeChain,
		UnionChoice:      winTrustChoiceFile,
		FileInfo:         uintptr(unsafe.Pointer(&file)),
		StateAction:      winTrustStateActionVerify,
		ProviderFlags:    winTrustRevocationCheckChain,
	}
	status, _, _ := procWinVerifyTrust.Call(
		0,
		uintptr(unsafe.Pointer(&winTrustActionGenericVerify)),
		uintptr(unsafe.Pointer(&data)),
	)
	data.StateAction = winTrustStateActionClose
	_, _, _ = procWinVerifyTrust.Call(
		0,
		uintptr(unsafe.Pointer(&winTrustActionGenericVerify)),
		uintptr(unsafe.Pointer(&data)),
	)
	if uint32(status) != 0 {
		return fmt.Errorf("Windows hat die Herausgebervertrauensstellung abgelehnt (0x%08X)", uint32(status))
	}
	actualPublisher, err := authenticodePublisherSHA256(filePath)
	if err != nil {
		return fmt.Errorf("Authenticode-Herausgeber ermitteln: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(actualPublisher), []byte(expectedPublisherSHA256)) != 1 {
		return fmt.Errorf("Authenticode-Herausgeber stimmt nicht überein (erhalten %s)", actualPublisher)
	}
	return nil
}

func authenticodePublisherSHA256(filePath *uint16) (string, error) {
	var store, message windows.Handle
	if err := windows.CryptQueryObject(
		windows.CERT_QUERY_OBJECT_FILE,
		unsafe.Pointer(filePath),
		windows.CERT_QUERY_CONTENT_FLAG_PKCS7_SIGNED_EMBED,
		windows.CERT_QUERY_FORMAT_FLAG_BINARY,
		0, nil, nil, nil, &store, &message, nil,
	); err != nil {
		return "", err
	}
	defer windows.CertCloseStore(store, 0)
	defer procCryptMsgClose.Call(uintptr(message))
	var size uint32
	result, _, callErr := procCryptMsgGetParam.Call(uintptr(message), 6, 0, 0, uintptr(unsafe.Pointer(&size)))
	if result == 0 || size < uint32(unsafe.Sizeof(cryptSignerInfoPrefix{})) {
		return "", callErr
	}
	buffer := make([]byte, size)
	result, _, callErr = procCryptMsgGetParam.Call(uintptr(message), 6, 0, uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&size)))
	if result == 0 {
		return "", callErr
	}
	signer := (*cryptSignerInfoPrefix)(unsafe.Pointer(&buffer[0]))
	certificateInfo := windows.CertInfo{Issuer: signer.Issuer, SerialNumber: signer.SerialNumber}
	certificate, err := windows.CertFindCertificateInStore(store, windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, 0, windows.CERT_FIND_SUBJECT_CERT, unsafe.Pointer(&certificateInfo), nil)
	if err != nil {
		return "", err
	}
	defer windows.CertFreeCertificateContext(certificate)
	if certificate.Length == 0 || certificate.EncodedCert == nil {
		return "", errors.New("Authenticode-Signerzertifikat ist leer")
	}
	digest := sha256.Sum256(unsafe.Slice(certificate.EncodedCert, certificate.Length))
	return hex.EncodeToString(digest[:]), nil
}

func RunApply(request ApplyRequest) error {
	if err := waitForProcess(uint32(request.ParentPID), 60*time.Second); err != nil {
		return err
	}
	unlock, err := lockUpdateTarget(request.Target)
	if err != nil {
		return err
	}
	defer unlock()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err = VerifyAuthenticode(self, request.PublisherSHA256); err != nil {
		return fmt.Errorf("laufenden Updater erneut prüfen: %w", err)
	}
	backup := request.Target + ".lanready-backup"
	newTarget := request.Target + ".lanready-new"
	_ = os.Remove(newTarget)
	_ = os.Remove(backup)
	if err = os.Rename(request.Target, backup); err != nil {
		return fmt.Errorf("bestehenden Client sichern: %w", err)
	}
	rollback := func(reason error) error {
		_ = os.Remove(request.Target)
		if restoreErr := os.Rename(backup, request.Target); restoreErr != nil {
			return fmt.Errorf("Update fehlgeschlagen (%v) und Wiederherstellung fehlgeschlagen: %w", reason, restoreErr)
		}
		if restartErr := exec.Command(request.Target).Start(); restartErr != nil {
			return fmt.Errorf("Update fehlgeschlagen (%v), vorige Version wurde wiederhergestellt, konnte aber nicht gestartet werden: %w", reason, restartErr)
		}
		return reason
	}
	if err = copyExecutable(self, newTarget, request.Size, request.SHA256); err != nil {
		return rollback(err)
	}
	if err = os.Rename(newTarget, request.Target); err != nil {
		return rollback(fmt.Errorf("neuen Client atomar aktivieren: %w", err))
	}
	command := exec.Command(request.Target,
		"--lanready-update-health",
		"--profile", request.ProfilePath,
		"--marker", request.MarkerPath,
		"--version", request.Version,
		"--sequence", strconv.FormatInt(request.Sequence, 10),
	)
	command.Dir = filepath.Dir(request.Target)
	if err = command.Start(); err != nil {
		return rollback(fmt.Errorf("aktualisierten Client starten: %w", err))
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if content, readErr := os.ReadFile(request.MarkerPath); readErr == nil && string(content) == strconv.FormatInt(request.Sequence, 10)+"\n" {
			health := HealthRequest{ProfilePath: request.ProfilePath, MarkerPath: request.MarkerPath, Version: request.Version, Sequence: request.Sequence}
			if commitErr := CommitHealthy(health); commitErr != nil {
				_ = command.Process.Kill()
				_, _ = command.Process.Wait()
				return rollback(fmt.Errorf("Updatezustand festschreiben: %w", commitErr))
			}
			_ = command.Process.Release()
			_ = os.Remove(backup)
			_ = os.Remove(request.MarkerPath)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
	return rollback(errors.New("aktualisierter Client hat den Gesundheitscheck nicht bestätigt"))
}

func lockUpdateTarget(target string) (func(), error) {
	absolute, err := filepath.Abs(target)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(absolute))))
	name, err := windows.UTF16PtrFromString("Local\\LANReady-Update-" + hex.EncodeToString(digest[:16]))
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, err
	}
	result, waitErr := windows.WaitForSingleObject(handle, 0)
	if waitErr != nil || result != uint32(windows.WAIT_OBJECT_0) {
		windows.CloseHandle(handle)
		return nil, errors.New("für diesen Client läuft bereits ein Update")
	}
	return func() {
		_ = windows.ReleaseMutex(handle)
		_ = windows.CloseHandle(handle)
	}, nil
}

func waitForProcess(pid uint32, timeout time.Duration) error {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return fmt.Errorf("laufenden Clientprozess für Update prüfen: %w", err)
	}
	defer windows.CloseHandle(handle)
	result, err := windows.WaitForSingleObject(handle, uint32(timeout/time.Millisecond))
	if err != nil {
		return err
	}
	if result == uint32(windows.WAIT_TIMEOUT) {
		return errors.New("laufender Client wurde für das Update nicht beendet")
	}
	return nil
}

func copyExecutable(source, destination string, expectedSize int64, expectedDigest string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(input, expectedSize+1))
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if written != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedDigest {
		_ = os.Remove(destination)
		return errors.New("Updaterbytes stimmen nicht mit dem signierten Digest überein")
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
