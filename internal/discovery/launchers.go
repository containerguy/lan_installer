package discovery

// InstalledLauncher reports a launcher application found on the machine.
//
// This is deliberately separate from game discovery: a launcher can be
// installed without any games, and inferring its presence from game finds
// reports a perfectly working installation as missing.
type InstalledLauncher struct {
	// Adapter matches the catalog adapter id (steam, ea_app, ...).
	Adapter string `json:"adapter"`
	// ExecutablePath is the launcher binary that proved the installation.
	ExecutablePath string `json:"executablePath"`
	// Version is only set when it could be read reliably.
	Version string `json:"version,omitempty"`
}
