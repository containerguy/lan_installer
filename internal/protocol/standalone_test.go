package protocol

import "testing"

func TestStandaloneGameNeedsNoLauncherRelease(t *testing.T) {
	payload := []byte(`{"formatVersion":2,"eventId":"lan-2026","releaseId":"01K0LANREADY00000000000099","sequence":1,"issuedAt":"2026-07-18T08:00:00Z","validUntil":"2026-07-20T08:00:00Z","minimumClientVersion":"1.0.0","artifacts":[{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":42,"mediaType":"application/zip","fileName":"openra.zip"}],"launchers":[],"games":[{"gameId":"open-ra","name":"OpenRA","launcherId":"standalone","externalGameId":"open-ra","version":"2026.1","required":true,"payloads":[{"type":"archive","artifacts":["sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetRoot":"user_games","relativePath":"OpenRA"}]}]}]}`)
	if err := ValidateEventReleaseSemantics(payload); err != nil {
		t.Fatalf("standalone event rejected: %v", err)
	}
}
