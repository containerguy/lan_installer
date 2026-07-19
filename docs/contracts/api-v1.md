# LANReady Legacy API v1

Status: nur Kompatibilität für den vorhandenen CLI-Prototyp.

Die Endpunkte `/v1/events/{id}/manifest`, `/v1/reports` und `/content/{path}` verwenden das alte Manifest und globale Bearer-Tokens. Sie erhalten keine neuen Funktionen. Der gestaltete Windows-Client verwendet ausschließlich den implementierbaren Vertrag in `api-v2.md` und lehnt Legacy-Manifeste explizit ab.

v1 bleibt während Entwicklung und Migration verfügbar. Nach erfolgreichem v2-Rollout wird ein konkretes Abschaltdatum veröffentlicht; mindestens ein Clientrelease warnt vorher. Ein v1-Client erhält niemals ein v2-EventRelease unter einem v1-Pfad.
