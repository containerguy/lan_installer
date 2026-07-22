package discovery

type Installation struct {
	Launcher        string  `json:"launcher"`
	ExternalGameID  string  `json:"externalGameId"`
	DisplayName     string  `json:"displayName"`
	DetectedVersion *string `json:"detectedVersion"`
	VersionSource   string  `json:"versionSource"`
	InstallPath     string  `json:"installPath"`
}

type Result struct {
	Installations []Installation `json:"installations"`
	Warnings      []string       `json:"warnings,omitempty"`
}
