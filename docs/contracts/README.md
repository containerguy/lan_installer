# LANReady v2-Vertragsindex

- `api-v2.md`: HTTP-, Auth-, Bootstrap-, Enrollment-, Source-, Event-, Artifact- und Updatevertrag.
- `domain-model.md`: Domänenobjekte, CAS und Prozess-/Vertrauensgrenzen.
- `roles.md`: Admin-/Operator-/Viewer-Rechte.
- `windows-components.md`: portable/installierte Windows-Komponenten, UX, Fehler und Code-Signing.
- `contract-tests.md`: verbindliche Integrations- und Negativtestmatrix.
- `api-v1.md`: Legacy-Grenze; neue v2-Clients verwenden sie nicht.
- `schemas/event-release-envelope.schema.json`: Envelope und EventRelease-Payload mit diskriminierten Actions.
- `schemas/client-update-envelope.schema.json`: signiertes Clientupdate mit digestgebundenem Same-Origin-Pfad.
- `schemas/device-api.schema.json`: Bootstrap-, 426- und Enrollment-Responses.
- `vectors/signatures-v1.json`: öffentliche, deterministische Golden-Vektoren; niemals Produktionskeys.

Maschinenprüfbare Referenzimplementierung und Tests liegen in `internal/protocol`.
