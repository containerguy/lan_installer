package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuildReplacesArchiveWithExactPortablePayload(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	executable := filepath.Join(directory, "client.exe")
	readme := filepath.Join(directory, "readme.txt")
	output := filepath.Join(directory, "portable.zip")
	if err := os.WriteFile(executable, []byte("binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(readme, []byte("instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("stale release"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := build(output, executable, readme); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.OpenReader(output)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	var names []string
	for _, file := range archive.File {
		names = append(names, file.Name)
	}
	if want := []string{"LANReady.exe", "README.txt", "SHA256SUMS.txt"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("archive entries = %v; want %v", names, want)
	}
	setup := filepath.Join(directory, "setup.exe")
	manifest := filepath.Join(directory, "SHA256SUMS.txt")
	if err = os.WriteFile(setup, []byte("setup"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err = writeManifest(manifest, output, setup); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(manifest)
	if err != nil || len(content) == 0 {
		t.Fatalf("external manifest = %q, %v", content, err)
	}
}
