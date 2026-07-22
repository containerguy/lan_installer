//go:build !windows

package main

import (
	"errors"
	"os"
)

func securePrivateKeyFile(_ string, info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("Private-Key-Datei muss regulär und ausschließlich für den Besitzer lesbar sein (0600)")
	}
	return nil
}

func hardenPrivateKeyFile(path string) error { return os.Chmod(path, 0o600) }
