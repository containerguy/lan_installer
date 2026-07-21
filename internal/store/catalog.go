package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

type Launcher struct {
	ID, Revision        int64
	Slug, Name, Adapter string
	Enabled             bool
}
type Source struct {
	ID, Revision, LastTestLatencyMS int64
	Name, Kind, BaseURL             string
	Enabled, AuthConfigured         bool
	LastTestState, LastTestedAt     string
}
type WebDAVConfig struct {
	SourceID                      int64
	AuthType, Username            string
	SecretNonce, SecretCiphertext []byte
}
type Game struct {
	ID, Revision, LauncherID                                  int64
	Slug, Name, LauncherName, LauncherAdapter, ExternalGameID string
	Enabled                                                   bool
}
type StandaloneGame struct {
	ID             int64    `json:"id"`
	Slug           string   `json:"slug"`
	Name           string   `json:"name"`
	ExternalGameID string   `json:"externalGameId"`
	Versions       []string `json:"versions"`
}
type LauncherVersion struct {
	ID, Revision, LauncherID       int64
	LauncherName, Version          string
	SourceID                       int64
	SourceName, SourcePath, SHA256 string
	SilentArgs                     []string
	SizeBytes                      int64
	Enabled, SilentArgsVerified    bool
}
type GameVersion struct {
	ID, Revision, GameID                             int64
	GameName, LauncherName, LauncherAdapter, Version string
	SourceID                                         int64
	SourceName, SourcePath, SHA256                   string
	SizeBytes                                        int64
	Enabled                                          bool
}
type Event struct {
	ID, Revision                         int64
	Slug, Name, StartsAt, EndsAt, Status string
}
type EventGame struct {
	EventID, GameVersionID, Revision int64
	EventName, GameName, Version     string
	Required                         bool
}

// EventReleaseGame is the immutable catalog input used to create an event
// release. SourceID == 0 means that the launcher provider is responsible for
// installing and updating the game; no LANReady package is implied.
type EventReleaseGame struct {
	EventID, GameVersionID, SourceID                       int64
	EventSlug, EventName, EventStatus                      string
	GameSlug, GameName, ExternalGameID                     string
	LauncherSlug, LauncherAdapter, Version                 string
	SourcePath, SHA256, ArtifactContentType                string
	SizeBytes                                              int64
	Required, GameEnabled, VersionEnabled, LauncherEnabled bool
}

func (s *Store) EventReleaseGames(ctx context.Context, eventID int64) ([]EventReleaseGame, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id,e.slug,e.name,e.status,eg.game_version_id,g.slug,g.name,g.external_game_id,l.slug,l.adapter,gv.version,COALESCE(gv.source_id,0),gv.source_path,COALESCE(gv.sha256,''),COALESCE(gv.size_bytes,0),COALESCE(a.content_type,''),eg.required,g.enabled,gv.enabled,l.enabled FROM events e JOIN event_games eg ON eg.event_id=e.id JOIN game_versions gv ON gv.id=eg.game_version_id JOIN games g ON g.id=gv.game_id JOIN launchers l ON l.id=g.launcher_id LEFT JOIN artifact_blobs a ON a.digest=gv.sha256 AND a.size_bytes=gv.size_bytes WHERE e.id=? ORDER BY g.name,gv.version`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]EventReleaseGame, 0)
	for rows.Next() {
		var value EventReleaseGame
		if err = rows.Scan(&value.EventID, &value.EventSlug, &value.EventName, &value.EventStatus, &value.GameVersionID, &value.GameSlug, &value.GameName, &value.ExternalGameID, &value.LauncherSlug, &value.LauncherAdapter, &value.Version, &value.SourceID, &value.SourcePath, &value.SHA256, &value.SizeBytes, &value.ArtifactContentType, &value.Required, &value.GameEnabled, &value.VersionEnabled, &value.LauncherEnabled); err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) Launchers(ctx context.Context) ([]Launcher, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,revision,slug,name,adapter,enabled FROM launchers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = make([]Launcher, 0)
	for rows.Next() {
		var v Launcher
		if err := rows.Scan(&v.ID, &v.Revision, &v.Slug, &v.Name, &v.Adapter, &v.Enabled); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Sources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id,s.revision,s.name,s.kind,s.base_url,s.enabled,EXISTS(SELECT 1 FROM webdav_source_config w WHERE w.source_id=s.id AND w.auth_type='basic' AND length(w.secret_ciphertext)>0),s.last_test_state,COALESCE(s.last_tested_at,''),COALESCE(s.last_test_latency_ms,0) FROM sources s ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var v Source
		if err := rows.Scan(&v.ID, &v.Revision, &v.Name, &v.Kind, &v.BaseURL, &v.Enabled, &v.AuthConfigured, &v.LastTestState, &v.LastTestedAt, &v.LastTestLatencyMS); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Games(ctx context.Context) ([]Game, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id,g.revision,g.launcher_id,g.slug,g.name,l.name,l.adapter,g.external_game_id,g.enabled FROM games g JOIN launchers l ON l.id=g.launcher_id ORDER BY g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = make([]Game, 0)
	for rows.Next() {
		var v Game
		if err := rows.Scan(&v.ID, &v.Revision, &v.LauncherID, &v.Slug, &v.Name, &v.LauncherName, &v.LauncherAdapter, &v.ExternalGameID, &v.Enabled); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) StandaloneGames(ctx context.Context) ([]StandaloneGame, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT g.id,g.slug,g.name,g.external_game_id,COALESCE(v.version,'') FROM games g JOIN launchers l ON l.id=g.launcher_id LEFT JOIN game_versions v ON v.game_id=g.id AND v.enabled=1 WHERE l.adapter='standalone' AND l.enabled=1 AND g.enabled=1 ORDER BY g.name,v.version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]StandaloneGame, 0)
	byID := make(map[int64]int)
	for rows.Next() {
		var gameID int64
		var game StandaloneGame
		var version string
		if err = rows.Scan(&gameID, &game.Slug, &game.Name, &game.ExternalGameID, &version); err != nil {
			return nil, err
		}
		game.ID = gameID
		index, exists := byID[game.ID]
		if !exists {
			index = len(out)
			byID[game.ID] = index
			out = append(out, game)
		}
		if version != "" {
			out[index].Versions = append(out[index].Versions, version)
		}
	}
	return out, rows.Err()
}
func (s *Store) LauncherVersions(ctx context.Context) ([]LauncherVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.id,v.revision,v.launcher_id,l.name,v.version,v.source_id,s.name,v.source_path,COALESCE(v.sha256,''),COALESCE(v.size_bytes,0),v.silent_args_json,v.silent_args_verified,v.enabled FROM launcher_versions v JOIN launchers l ON l.id=v.launcher_id JOIN sources s ON s.id=v.source_id ORDER BY l.name,v.version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = make([]LauncherVersion, 0)
	for rows.Next() {
		var v LauncherVersion
		var silentArgsJSON string
		if err := rows.Scan(&v.ID, &v.Revision, &v.LauncherID, &v.LauncherName, &v.Version, &v.SourceID, &v.SourceName, &v.SourcePath, &v.SHA256, &v.SizeBytes, &silentArgsJSON, &v.SilentArgsVerified, &v.Enabled); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(silentArgsJSON), &v.SilentArgs); err != nil {
			return nil, errors.New("invalid stored silent argument list")
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) GameVersions(ctx context.Context) ([]GameVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.id,v.revision,v.game_id,g.name,l.name,l.adapter,v.version,COALESCE(v.source_id,0),COALESCE(s.name,''),v.source_path,COALESCE(v.sha256,''),COALESCE(v.size_bytes,0),v.enabled FROM game_versions v JOIN games g ON g.id=v.game_id JOIN launchers l ON l.id=g.launcher_id LEFT JOIN sources s ON s.id=v.source_id ORDER BY g.name,v.version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = make([]GameVersion, 0)
	for rows.Next() {
		var v GameVersion
		if err := rows.Scan(&v.ID, &v.Revision, &v.GameID, &v.GameName, &v.LauncherName, &v.LauncherAdapter, &v.Version, &v.SourceID, &v.SourceName, &v.SourcePath, &v.SHA256, &v.SizeBytes, &v.Enabled); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Events(ctx context.Context) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,revision,slug,name,COALESCE(starts_at,''),COALESCE(ends_at,''),status FROM events ORDER BY starts_at DESC,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = make([]Event, 0)
	for rows.Next() {
		var v Event
		if err := rows.Scan(&v.ID, &v.Revision, &v.Slug, &v.Name, &v.StartsAt, &v.EndsAt, &v.Status); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) EventByID(ctx context.Context, id int64) (Event, error) {
	var value Event
	err := s.db.QueryRowContext(ctx, `SELECT id,revision,slug,name,COALESCE(starts_at,''),COALESCE(ends_at,''),status FROM events WHERE id=?`, id).Scan(&value.ID, &value.Revision, &value.Slug, &value.Name, &value.StartsAt, &value.EndsAt, &value.Status)
	return value, err
}
func (s *Store) EventGames(ctx context.Context) ([]EventGame, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT eg.event_id,eg.game_version_id,eg.revision,e.name,g.name,gv.version,eg.required FROM event_games eg JOIN events e ON e.id=eg.event_id JOIN game_versions gv ON gv.id=eg.game_version_id JOIN games g ON g.id=gv.game_id ORDER BY e.name,g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out = make([]EventGame, 0)
	for rows.Next() {
		var v EventGame
		if err := rows.Scan(&v.EventID, &v.GameVersionID, &v.Revision, &v.EventName, &v.GameName, &v.Version, &v.Required); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func blank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func (s *Store) WebDAVConfig(ctx context.Context, sourceID int64) (WebDAVConfig, error) {
	var v WebDAVConfig
	err := s.db.QueryRowContext(ctx, `SELECT source_id,auth_type,username,COALESCE(secret_nonce,X''),COALESCE(secret_ciphertext,X'') FROM webdav_source_config WHERE source_id=?`, sourceID).Scan(&v.SourceID, &v.AuthType, &v.Username, &v.SecretNonce, &v.SecretCiphertext)
	return v, err
}
