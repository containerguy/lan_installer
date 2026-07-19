package main

import (
	"archive/zip"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	executable := flag.String("exe", "", "path to LANReady.exe")
	readme := flag.String("readme", "", "path to the portable README")
	output := flag.String("out", "", "output ZIP path")
	setup := flag.String("setup", "", "optional setup executable for the external checksum manifest")
	manifest := flag.String("manifest", "", "optional external SHA-256 manifest path")
	flag.Parse()
	if *executable == "" || *readme == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "-exe, -readme and -out are required")
		os.Exit(2)
	}
	if err := build(*output, *executable, *readme); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if (*setup == "") != (*manifest == "") {
		fmt.Fprintln(os.Stderr, "-setup and -manifest must be used together")
		os.Exit(2)
	}
	if *manifest != "" {
		if err := writeManifest(*manifest, *output, *setup); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func writeManifest(output string, artifacts ...string) error {
	sort.Strings(artifacts)
	var content strings.Builder
	for _, artifact := range artifacts {
		value, err := os.ReadFile(artifact)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(value)
		fmt.Fprintf(&content, "%x  %s\n", digest, filepath.Base(artifact))
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".lanready-checksums-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err = temporary.WriteString(content.String()); err == nil {
		err = temporary.Close()
	} else {
		_ = temporary.Close()
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(temporaryPath, 0o644); err != nil {
		return err
	}
	if err = os.Remove(output); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(temporaryPath, output)
}

func build(output, executable, readme string) error {
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".lanready-portable-*.zip")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	archive := zip.NewWriter(temporary)
	executableHash, err := addFile(archive, "LANReady.exe", executable, 0o755)
	if err == nil {
		_, err = addFile(archive, "README.txt", readme, 0o644)
	}
	if err == nil {
		header := deterministicHeader("SHA256SUMS.txt", 0o644)
		var checksum io.Writer
		checksum, err = archive.CreateHeader(header)
		if err == nil {
			_, err = fmt.Fprintf(checksum, "%x  LANReady.exe\n", executableHash)
		}
	}
	closeErr := archive.Close()
	fileCloseErr := temporary.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if fileCloseErr != nil {
		return fileCloseErr
	}
	if err = os.Chmod(temporaryPath, 0o644); err != nil {
		return err
	}
	if err = os.Remove(output); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(temporaryPath, output)
}

func addFile(archive *zip.Writer, name, source string, mode os.FileMode) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	content, err := os.ReadFile(source)
	if err != nil {
		return digest, err
	}
	digest = sha256.Sum256(content)
	writer, err := archive.CreateHeader(deterministicHeader(name, mode))
	if err != nil {
		return digest, err
	}
	_, err = writer.Write(content)
	return digest, err
}

func deterministicHeader(name string, mode os.FileMode) *zip.FileHeader {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(mode)
	header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
	return header
}
