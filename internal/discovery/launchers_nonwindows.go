//go:build !windows

package discovery

// DiscoverLaunchers only reports installations on Windows; the shipped client
// is Windows-only and this keeps the package buildable for tests elsewhere.
func DiscoverLaunchers() []InstalledLauncher { return nil }
