package protocol

import (
	"strings"
	"testing"
)

const installDigest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

// payloadWith builds an event payload whose single standalone game carries the
// given payload JSON, so each test varies exactly one thing.
func payloadWith(gamePayloads string) []byte {
	return []byte(strings.ReplaceAll(`{"formatVersion":2,"eventId":"lan","releaseId":"r","sequence":1,
"issuedAt":"2026-07-20T08:00:00Z","validUntil":"2026-07-22T08:00:00Z","minimumClientVersion":"0.2.0",
"artifacts":[{"digest":"DIGEST","size":4096,"mediaType":"application/zip","fileName":"game.zip"}],
"launchers":[],
"games":[{"gameId":"flatout2","name":"FlatOut 2","launcherId":"standalone","externalGameId":"flatout2","version":"V1","required":true,"payloads":`+gamePayloads+`}]}`, "DIGEST", installDigest))
}

func archivePayload(action string) string {
	return `[{"type":"archive","artifacts":["` + installDigest + `"],"actions":[` + strings.ReplaceAll(action, "DIGEST", installDigest) + `]}]`
}

func TestEventInstallActionsAcceptsSupportedArchive(t *testing.T) {
	payload := payloadWith(archivePayload(`{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"user_games","relativePath":"flatout2"}`))
	installs, err := EventInstallActions(payload)
	if err != nil {
		t.Fatalf("supported archive was refused: %v", err)
	}
	if len(installs) != 1 {
		t.Fatalf("installs: %#v", installs)
	}
	got := installs[0]
	if got.GameID != "flatout2" || got.RelativePath != "flatout2" || got.Size != 4096 {
		t.Fatalf("install: %#v", got)
	}
	// The sha256: prefix must be stripped so the digest can be used directly.
	if strings.Contains(got.Digest, ":") || len(got.Digest) != 64 {
		t.Fatalf("digest was not normalised: %q", got.Digest)
	}
}

// A release may only make this client do what it can do safely. Anything else
// must stop the install rather than be skipped, so a partially applied event
// can never look complete.
func TestEventInstallActionsRefusesUnsupportedWork(t *testing.T) {
	for name, payloads := range map[string]string{
		"materialize tree":     archivePayload(`{"adapter":"lanready_tree","operation":"materialize_tree","artifactDigest":"DIGEST","targetRoot":"user_games","relativePath":"x"}`),
		"import library":       archivePayload(`{"adapter":"steam","operation":"import_library","artifactDigest":"DIGEST","targetRoot":"steam_library","relativePath":"x"}`),
		"foreign target root":  archivePayload(`{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"steam_library","relativePath":"x"}`),
		"temp target root":     archivePayload(`{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"temp","relativePath":"x"}`),
		"traversal path":       archivePayload(`{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"user_games","relativePath":"../escape"}`),
		"absolute path":        archivePayload(`{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"user_games","relativePath":"/escape"}`),
		"drive path":           archivePayload(`{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"user_games","relativePath":"C:\\Windows"}`),
		"installer payload":    `[{"type":"installer","artifacts":["` + installDigest + `"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"` + installDigest + `","targetRoot":"user_games","relativePath":"x"}]}]`,
		"two actions":          `[{"type":"archive","artifacts":["` + installDigest + `"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"` + installDigest + `","targetRoot":"user_games","relativePath":"a"},{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"` + installDigest + `","targetRoot":"user_games","relativePath":"b"}]}]`,
		"undeclared artifact":  archivePayload(`{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","targetRoot":"user_games","relativePath":"x"}`),
		"mismatched artifacts": `[{"type":"archive","artifacts":["sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"` + installDigest + `","targetRoot":"user_games","relativePath":"x"}]}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := EventInstallActions(payloadWith(payloads)); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

func TestEventInstallActionsRefusesPayloadOnLauncherGame(t *testing.T) {
	payload := []byte(strings.ReplaceAll(`{"formatVersion":2,"eventId":"lan","releaseId":"r","sequence":1,
"issuedAt":"2026-07-20T08:00:00Z","validUntil":"2026-07-22T08:00:00Z","minimumClientVersion":"0.2.0",
"artifacts":[{"digest":"DIGEST","size":4096,"mediaType":"application/zip","fileName":"game.zip"}],
"launchers":[{"launcherId":"steam","version":"current","required":true,"actions":[{"adapter":"steam","operation":"detect"}]}],
"games":[{"gameId":"cs2","name":"CS2","launcherId":"steam","externalGameId":"730","version":"1","required":true,"payloads":[{"type":"archive","artifacts":["DIGEST"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"user_games","relativePath":"cs2"}]}]}]}`, "DIGEST", installDigest))
	if _, err := EventInstallActions(payload); err == nil {
		t.Fatal("launcher-managed game with a payload was accepted")
	}
}

func TestEventInstallActionsRefusesLauncherInstallAction(t *testing.T) {
	payload := []byte(`{"formatVersion":2,"eventId":"lan","releaseId":"r","sequence":1,
"issuedAt":"2026-07-20T08:00:00Z","validUntil":"2026-07-22T08:00:00Z","minimumClientVersion":"0.2.0",
"artifacts":[],
"launchers":[{"launcherId":"steam","version":"current","required":true,"actions":[{"adapter":"steam","operation":"install_launcher","artifactDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}]}],
"games":[]}`)
	if _, err := EventInstallActions(payload); err == nil {
		t.Fatal("launcher installation was accepted")
	}
}

// Provider-managed releases carry no payloads at all and must stay installable
// without any client-side work.
func TestEventInstallActionsReturnsNothingForProviderManagedRelease(t *testing.T) {
	payload := []byte(`{"formatVersion":2,"eventId":"lan","releaseId":"r","sequence":1,
"issuedAt":"2026-07-20T08:00:00Z","validUntil":"2026-07-22T08:00:00Z","minimumClientVersion":"0.2.0",
"artifacts":[],
"launchers":[{"launcherId":"steam","version":"current","required":true,"actions":[{"adapter":"steam","operation":"detect"}]}],
"games":[{"gameId":"cs2","name":"CS2","launcherId":"steam","externalGameId":"730","version":"1","required":true,"payloads":[]}]}`)
	installs, err := EventInstallActions(payload)
	if err != nil || len(installs) != 0 {
		t.Fatalf("provider release: %#v %v", installs, err)
	}
}

func TestEventInstallActionsRefusesNonArchiveMediaType(t *testing.T) {
	payload := []byte(strings.ReplaceAll(`{"formatVersion":2,"eventId":"lan","releaseId":"r","sequence":1,
"issuedAt":"2026-07-20T08:00:00Z","validUntil":"2026-07-22T08:00:00Z","minimumClientVersion":"0.2.0",
"artifacts":[{"digest":"DIGEST","size":4096,"mediaType":"application/octet-stream","fileName":"game.bin"}],
"launchers":[],
"games":[{"gameId":"flatout2","name":"FlatOut 2","launcherId":"standalone","externalGameId":"flatout2","version":"V1","required":true,"payloads":[{"type":"archive","artifacts":["DIGEST"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"DIGEST","targetRoot":"user_games","relativePath":"flatout2"}]}]}]}`, "DIGEST", installDigest))
	if _, err := EventInstallActions(payload); err == nil {
		t.Fatal("non-archive media type was accepted")
	}
}
