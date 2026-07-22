//go:build !windows

package deviceclient

func validateManualExecutableDrive(string) error { return nil }
