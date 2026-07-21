package protocol

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const validEventRelease = `{
  "formatVersion":2,
  "eventId":"kellerlan-2026",
  "releaseId":"01K0LANREADY00000000000002",
  "sequence":4,
  "issuedAt":"2026-07-16T08:00:00Z",
  "validUntil":"2026-07-20T08:00:00Z",
  "minimumClientVersion":"0.3.0",
  "artifacts":[{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","size":1024,"mediaType":"application/octet-stream","fileName":"payload.zip"}],
  "launchers":[{"launcherId":"steam","version":"1.0","required":true,"actions":[{"adapter":"steam","operation":"install_launcher","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}],
  "games":[{"gameId":"cs2","name":"Counter-Strike 2","launcherId":"steam","externalGameId":"730","version":"2026.07.16","required":true,"payloads":[{"type":"archive","artifacts":["sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],"actions":[{"adapter":"lanready_archive","operation":"extract_archive","artifactDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","targetRoot":"steam_library","relativePath":"steamapps/common/Counter-Strike Global Offensive"}]}]}]
}`

func compileSchema(t *testing.T, name, fragment string) *jsonschema.Schema {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("../../docs/contracts/schemas", name))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	schema, err := compiler.Compile("file://" + filepath.ToSlash(path) + fragment)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func decodeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestEventReleaseSchemaGoldenAndActions(t *testing.T) {
	schema := compileSchema(t, "event-release-envelope.schema.json", "#/$defs/eventRelease")
	if err := schema.Validate(decodeJSON(t, []byte(validEventRelease))); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	var invalid map[string]any
	if err := json.Unmarshal([]byte(validEventRelease), &invalid); err != nil {
		t.Fatal(err)
	}
	launcher := invalid["launchers"].([]any)[0].(map[string]any)
	action := launcher["actions"].([]any)[0].(map[string]any)
	delete(action, "artifactDigest")
	if err := schema.Validate(invalid); err == nil {
		t.Fatal("install_launcher without artifactDigest accepted")
	}
}

func TestProviderManagedGameNeedsNoLANReadyArtifact(t *testing.T) {
	payload := []byte(`{"formatVersion":2,"eventId":"lan-2026","releaseId":"release-0123456789abcdef0123456789abcdef","sequence":1,"issuedAt":"2026-07-20T08:00:00Z","validUntil":"2026-08-20T08:00:00Z","minimumClientVersion":"0.2.0","artifacts":[],"launchers":[{"launcherId":"steam","version":"current","required":true,"actions":[{"adapter":"steam","operation":"detect"}]}],"games":[{"gameId":"cs2","name":"Counter-Strike 2","launcherId":"steam","externalGameId":"730","version":"latest","required":true,"payloads":[]}]}`)
	if err := compileSchema(t, "event-release-envelope.schema.json", "#/$defs/eventRelease").Validate(decodeJSON(t, payload)); err != nil {
		t.Fatalf("provider-managed payload rejected by schema: %v", err)
	}
	if err := ValidateEventReleaseSemantics(payload); err != nil {
		t.Fatalf("provider-managed payload rejected semantically: %v", err)
	}
}

func TestProviderManagedGameRejectsLegacyMinimumClient(t *testing.T) {
	payload := []byte(`{"minimumClientVersion":"0.1.0","artifacts":[],"launchers":[{"launcherId":"steam","actions":[{"adapter":"steam","operation":"detect"}]}],"games":[{"gameId":"cs2","launcherId":"steam","payloads":[]}]}`)
	if err := ValidateEventReleaseSemantics(payload); err == nil {
		t.Fatal("provider-managed release accepted legacy minimum client")
	}
}

func TestStandaloneGameWithoutPayloadIsRejected(t *testing.T) {
	payload := []byte(`{"formatVersion":2,"eventId":"lan-2026","releaseId":"release-0123456789abcdef0123456789abcdef","sequence":1,"issuedAt":"2026-07-20T08:00:00Z","validUntil":"2026-08-20T08:00:00Z","minimumClientVersion":"0.2.0","artifacts":[],"launchers":[],"games":[{"gameId":"flatout-2","name":"FlatOut 2","launcherId":"standalone","externalGameId":"flatout-2","version":"1","required":true,"payloads":[]}]}`)
	if err := ValidateEventReleaseSemantics(payload); err == nil {
		t.Fatal("standalone game without installable payload was accepted")
	}
}

func TestGoldenPayloadSchemas(t *testing.T) {
	v := loadVectors(t)
	eventPayload, err := base64.RawURLEncoding.DecodeString(v.EventRelease.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = compileSchema(t, "event-release-envelope.schema.json", "#/$defs/eventRelease").Validate(decodeJSON(t, eventPayload)); err != nil {
		t.Fatalf("event golden payload: %v", err)
	}
	updatePayload, err := base64.RawURLEncoding.DecodeString(v.ClientUpdate.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = compileSchema(t, "client-update-envelope.schema.json", "#/$defs/clientUpdate").Validate(decodeJSON(t, updatePayload)); err != nil {
		t.Fatalf("update golden payload: %v", err)
	}
	if err = ValidateClientUpdateSemantics(updatePayload); err != nil {
		t.Fatalf("update semantic validation: %v", err)
	}
}

func TestClientUpdateDigestPathMismatch(t *testing.T) {
	payload := []byte(`{"artifactPath":"/v2/client/artifacts/sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
	if err := ValidateClientUpdateSemantics(payload); err == nil {
		t.Fatal("mismatched update path accepted")
	}
}

func TestEventReleaseSemanticReferences(t *testing.T) {
	if err := ValidateEventReleaseSemantics([]byte(validEventRelease)); err != nil {
		t.Fatalf("valid release rejected: %v", err)
	}
	cases := map[string]func(map[string]any){
		"missing launcher": func(v map[string]any) { v["launchers"] = []any{} },
		"missing artifact": func(v map[string]any) {
			game := v["games"].([]any)[0].(map[string]any)
			payload := game["payloads"].([]any)[0].(map[string]any)
			payload["artifacts"] = []any{"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
		},
		"mismatched adapter": func(v map[string]any) {
			launcher := v["launchers"].([]any)[0].(map[string]any)
			action := launcher["actions"].([]any)[0].(map[string]any)
			action["adapter"] = "ea_app"
		},
		"game launcher binding mismatch": func(v map[string]any) {
			game := v["games"].([]any)[0].(map[string]any)
			payload := game["payloads"].([]any)[0].(map[string]any)
			payload["type"] = "launcher_library"
			action := payload["actions"].([]any)[0].(map[string]any)
			action["operation"] = "import_library"
			action["adapter"] = "ea_app"
			action["targetRoot"] = "ea_library"
		},
		"operation payload mismatch": func(v map[string]any) {
			game := v["games"].([]any)[0].(map[string]any)
			payload := game["payloads"].([]any)[0].(map[string]any)
			action := payload["actions"].([]any)[0].(map[string]any)
			action["operation"] = "materialize_tree"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal([]byte(validEventRelease), &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			payload, _ := json.Marshal(value)
			if err := ValidateEventReleaseSemantics(payload); err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
}

func TestDeviceBootstrapAndEnrollmentSchemas(t *testing.T) {
	cases := []struct {
		name     string
		fragment string
		raw      string
	}{
		{"bootstrap active", "#/$defs/bootstrap", `{"serverTime":"2026-07-16T08:00:00Z","apiVersion":2,"capabilities":["range-download","status-v1","client-update-v1"],"activeEvent":{"eventId":"kellerlan-2026","releaseUrl":"/v2/events/kellerlan-2026/release"},"clientUpdate":{"required":false,"releaseUrl":"/v2/client/releases/latest?channel=stable"}}`},
		{"bootstrap no event", "#/$defs/bootstrap", `{"serverTime":"2026-07-16T08:00:00Z","apiVersion":2,"capabilities":["range-download","status-v1","client-update-v1"],"activeEvent":null,"clientUpdate":{"required":false,"releaseUrl":"/v2/client/releases/latest?channel=stable"}}`},
		{"upgrade required", "#/$defs/upgradeRequired", `{"code":"client_update_required","message":"Clientupdate erforderlich","requestId":"01K0REQUEST000000000000001","clientUpdate":{"required":true,"releaseUrl":"/v2/client/releases/latest?channel=stable"}}`},
		{"enrollment code", "#/$defs/enrollmentCode", `{"id":"01K0ENROLLMENT00000000001","code":"K7M4P-9Q2XR-T6V8W-3Y5ZA-BCDEFG","expiresAt":"2026-07-16T08:10:00Z","maxUses":1}`},
		{"enroll request", "#/$defs/enrollRequest", `{"code":"K7M4P-9Q2XR-T6V8W-3Y5ZA-BCDEFG","publicKey":"A6EHv_POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg","deviceName":"GAMING-PC-01","windowsVersion":"11.0.26100","clientVersion":"0.3.0"}`},
		{"enroll response", "#/$defs/enrollResponse", `{"deviceId":"6ba7b810-9dad-11d1-80b4-00c04fd430c8","serverTime":"2026-07-16T08:00:00Z","apiVersion":2,"capabilities":["range-download","status-v1","client-update-v1"],"bootstrapUrl":"/v2/device/bootstrap"}`},
		{"standalone games", "#/$defs/standaloneGamesResponse", `{"games":[{"id":4,"slug":"open-ra","name":"OpenRA","externalGameId":"open-ra","versions":["2026.1"]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := compileSchema(t, "device-api.schema.json", tc.fragment).Validate(decodeJSON(t, []byte(tc.raw))); err != nil {
				t.Fatal(err)
			}
		})
	}
	invalid426 := decodeJSON(t, []byte(`{"code":"client_update_required","message":"Update","requestId":"01K0REQUEST000000000000001","clientUpdate":{"required":true,"releaseUrl":"https://evil.example/update.exe"},"activeEvent":{"eventId":"x","releaseUrl":"/v2/events/x/release"}}`))
	if err := compileSchema(t, "device-api.schema.json", "#/$defs/upgradeRequired").Validate(invalid426); err == nil {
		t.Fatal("426 with arbitrary URL or active event accepted")
	}
	invalidEnroll := decodeJSON(t, []byte(`{"code":"K7M4-P9Q2","publicKey":"short","deviceName":"PC","windowsVersion":"11","clientVersion":"0.3.0"}`))
	if err := compileSchema(t, "device-api.schema.json", "#/$defs/enrollRequest").Validate(invalidEnroll); err == nil {
		t.Fatal("low-entropy code or invalid Ed25519 key accepted")
	}
}
