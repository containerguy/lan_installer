package schemas

import _ "embed"

// EventReleaseEnvelope is the canonical v2 event-release schema embedded into
// the portable client. Keeping the schema beside the checked-in contract avoids
// a second, drifting runtime copy.
//
//go:embed event-release-envelope.schema.json
var EventReleaseEnvelope []byte

// ClientUpdateEnvelope is the canonical v1 client-update schema.
//
//go:embed client-update-envelope.schema.json
var ClientUpdateEnvelope []byte
