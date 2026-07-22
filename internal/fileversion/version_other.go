//go:build !windows

package fileversion

import "errors"

func Read(string) (string, error) {
	return "", errors.New("Windows file version information is unavailable")
}
