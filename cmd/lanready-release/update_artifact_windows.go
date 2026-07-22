//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"github.com/containerguy/lan_installer/internal/protocol"
	"github.com/containerguy/lan_installer/internal/selfupdate"
	"golang.org/x/sys/windows"
)

type fixedFileInfo struct {
	Signature, StructVersion           uint32
	FileVersionMS, FileVersionLS       uint32
	ProductVersionMS, ProductVersionLS uint32
	FileFlagsMask, FileFlags, FileOS   uint32
	FileType, FileSubtype              uint32
	FileDateMS, FileDateLS             uint32
}

func platformVerifyClientUpdateArtifact(path string, metadata protocol.ClientUpdateMetadata) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != metadata.Size {
		file.Close()
		return errors.New("Updateartefakt ist keine reguläre Datei mit der signierten Größe")
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != metadata.SHA256 {
		return errors.New("Updateartefakt stimmt nicht mit dem signierten SHA-256 überein")
	}
	if err = selfupdate.VerifyAuthenticode(path, metadata.PublisherCertificateSHA256); err != nil {
		return err
	}
	major, minor, patch, err := semanticVersionTriplet(metadata.Version)
	if err != nil {
		return err
	}
	var zero windows.Handle
	size, err := windows.GetFileVersionInfoSize(path, &zero)
	if err != nil || size == 0 {
		return errors.New("Windows-Dateiversion des Updateartefakts fehlt")
	}
	buffer := make([]byte, size)
	if err = windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buffer[0])); err != nil {
		return err
	}
	var value *fixedFileInfo
	var valueSize uint32
	if err = windows.VerQueryValue(unsafe.Pointer(&buffer[0]), `\`, unsafe.Pointer(&value), &valueSize); err != nil || value == nil || valueSize < uint32(unsafe.Sizeof(fixedFileInfo{})) || value.Signature != 0xFEEF04BD {
		return errors.New("Windows-Dateiversionsblock des Updateartefakts ist ungültig")
	}
	actualMajor, actualMinor := int(value.FileVersionMS>>16), int(value.FileVersionMS&0xffff)
	actualPatch := int(value.FileVersionLS >> 16)
	if actualMajor != major || actualMinor != minor || actualPatch != patch {
		return fmt.Errorf("Windows-Dateiversion %d.%d.%d stimmt nicht mit Releaseversion %s überein", actualMajor, actualMinor, actualPatch, metadata.Version)
	}
	return nil
}

func semanticVersionTriplet(version string) (int, int, int, error) {
	base := strings.SplitN(version, "-", 2)[0]
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return 0, 0, 0, errors.New("Releaseversion ist ungültig")
	}
	values := make([]int, 3)
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || value > 65535 {
			return 0, 0, 0, errors.New("Releaseversion passt nicht in Windows-Dateiversionsfelder")
		}
		values[index] = value
	}
	return values[0], values[1], values[2], nil
}
