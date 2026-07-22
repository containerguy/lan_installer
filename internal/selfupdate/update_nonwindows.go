//go:build !windows

package selfupdate

import (
	"errors"

	"github.com/containerguy/lan_installer/internal/deviceclient"
)

func Launch(deviceclient.PreparedUpdate, string, string, string) error {
	return errors.New("client self-update is only supported on Windows")
}

func RunApply(ApplyRequest) error {
	return errors.New("client self-update is only supported on Windows")
}
