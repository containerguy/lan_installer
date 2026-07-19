package model

import (
	"encoding/json"
	"time"
)

const ManifestVersion = 1

type Envelope struct {
	Manifest  json.RawMessage `json:"manifest"`
	Signature string          `json:"signature"`
}

type EventManifest struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expiresAt"`
	Mirrors   []Mirror  `json:"mirrors"`
	Games     []Game    `json:"games"`
}

type Mirror struct {
	Name     string `json:"name"`
	BaseURL  string `json:"baseUrl"`
	Priority int    `json:"priority"`
	Local    bool   `json:"local,omitempty"`
}

type Game struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Target      string `json:"target"`
	Description string `json:"description,omitempty"`
	Files       []File `json:"files"`
}

type File struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Report struct {
	EventID      string       `json:"eventId"`
	ClientID     string       `json:"clientId"`
	Hostname     string       `json:"hostname"`
	OperatingSys string       `json:"operatingSystem"`
	OSVersion    string       `json:"osVersion,omitempty"`
	ClientMode   string       `json:"clientMode"`
	CreatedAt    time.Time    `json:"createdAt"`
	Ready        bool         `json:"ready"`
	Games        []GameReport `json:"games"`
	Errors       []string     `json:"errors,omitempty"`
	Windows      *WinReport   `json:"windows,omitempty"`
}

type GameReport struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Ready     bool   `json:"ready"`
	Changed   int    `json:"changed"`
	Skipped   int    `json:"skipped"`
	Failed    int    `json:"failed"`
	Bytes     int64  `json:"bytesDownloaded"`
	ErrorText string `json:"error,omitempty"`
}

type WinReport struct {
	Mode           string `json:"mode"`
	MissingUpdates int    `json:"missingUpdates,omitempty"`
	Installed      int    `json:"installed,omitempty"`
	RebootRequired bool   `json:"rebootRequired,omitempty"`
	ErrorText      string `json:"error,omitempty"`
}
