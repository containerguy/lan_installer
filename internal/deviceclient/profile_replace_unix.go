//go:build !windows

package deviceclient

import (
	"os"
	"path/filepath"
)

func replaceProfileFile(temporary, target string) error {
	if err := os.Rename(temporary, target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
