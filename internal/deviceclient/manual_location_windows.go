//go:build windows

package deviceclient

import (
	"errors"

	"golang.org/x/sys/windows"
)

func validateManualExecutableDrive(value string) error {
	root, err := windows.UTF16PtrFromString(value[:3])
	if err != nil {
		return errors.New("manual executable drive is invalid")
	}
	switch windows.GetDriveType(root) {
	case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE, windows.DRIVE_CDROM, windows.DRIVE_RAMDISK:
		return nil
	case windows.DRIVE_REMOTE:
		return errors.New("manual executable must not use a mapped network drive")
	default:
		return errors.New("manual executable drive is unavailable")
	}
}
