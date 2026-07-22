# ADR 0002: Verschlüsselte WebDAV-Secrets in SQLite

Status: angenommen

## Entscheidung

WebDAV- und Nextcloud-WebDAV-Authentifizierung wird in der dedizierten Tabelle `webdav_source_config` gespeichert. Die allgemeine Tabelle `sources` enthält ausschließlich Name, Typ, Basis-URL und Aktivstatus.

Basic- beziehungsweise Nextcloud-App-Passwörter werden mit XChaCha20-Poly1305 und einem zufälligen 24-Byte-Nonce verschlüsselt. Als Additional Authenticated Data wird die stabile Quellen-ID mit einer Formatversion gebunden. Manipulierte Ciphertexte oder eine Zuordnung zu einer anderen Quelle können dadurch nicht entschlüsselt werden.

Der 256-Bit-Masterschlüssel liegt ausschließlich als Docker Secret in `secrets/webdav-master-key.txt`; er wird nicht in SQLite, Compose oder `.env` gespeichert. Das Passwortfeld wird nie zurück an den Browser gerendert. Ein leeres Passwortfeld behält beim Bearbeiten das vorhandene Ciphertext-Paar.

## Wiederherstellung und Rotation

Datenbank und Masterschlüssel müssen gemeinsam, aber zugriffsgeschützt gesichert werden. Ohne den ursprünglichen Masterschlüssel sind die WebDAV-Passwörter absichtlich nicht wiederherstellbar. Schlüsselrotation benötigt einen späteren atomaren Re-Encryption-Workflow und darf nicht durch bloßes Ersetzen der Secret-Datei erfolgen.
