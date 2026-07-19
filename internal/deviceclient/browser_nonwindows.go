//go:build !windows

package deviceclient

import "errors"

func OpenBrowser(string) error {
	return errors.New("automatic browser opening is only available on Windows")
}
