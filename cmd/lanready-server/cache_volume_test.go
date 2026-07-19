package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyCacheVolume(t *testing.T) {
	root := t.TempDir()
	if err := verifyCacheVolume(root, ""); err != nil {
		t.Fatalf("optional sentinel rejected: %v", err)
	}
	if err := verifyCacheVolume(root, "volume-1"); err == nil {
		t.Fatal("missing sentinel accepted")
	}
	if err := os.WriteFile(filepath.Join(root, cacheVolumeSentinelName), []byte("volume-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCacheVolume(root, "volume-1"); err != nil {
		t.Fatalf("valid sentinel rejected: %v", err)
	}
	if err := verifyCacheVolume(root, "volume-2"); err == nil {
		t.Fatal("mismatched sentinel accepted")
	}
	if err := os.Remove(filepath.Join(root, cacheVolumeSentinelName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, cacheVolumeSentinelName)); err != nil {
		t.Fatal(err)
	}
	if err := verifyCacheVolume(root, "volume-1"); err == nil {
		t.Fatal("symlink sentinel accepted")
	}
	if err := os.Remove(filepath.Join(root, cacheVolumeSentinelName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, cacheVolumeSentinelName), make([]byte, 257), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCacheVolume(root, "volume-1"); err == nil {
		t.Fatal("oversized sentinel accepted")
	}
}
