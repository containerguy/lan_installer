LANReady – portable Windows-Version
===================================

1. Den gesamten Ordner auf die lokale Festplatte entpacken.
2. LANReady.exe starten.
3. In der Managementoberfläche unter "Clients" einen Enrollment-Code erzeugen.
4. Im Client "PC verbinden" wählen und den einmaligen Code eingeben.
5. "Spiele suchen" starten, Funde auswählen und die Synchronisation bestätigen.

Voraussetzung: Windows 11 x64 und Microsoft WebView2 Runtime. Windows 11 bringt
WebView2 normalerweise mit. Falls die Laufzeit fehlt, bietet LANReady den
offiziellen Microsoft-Download an.

Sicherheit:
- Launcher-Passwörter werden weder abgefragt noch gespeichert.
- Der private Geräteschlüssel wird für den angemeldeten Windows-Benutzer mit
  Windows DPAPI geschützt.
- Die persönliche Browser-Anmeldung wird nicht dauerhaft gespeichert.

Die portable Version erzeugt keinen Autostart und installiert keinen Dienst.
Zum Entfernen LANReady beenden und den entpackten Ordner löschen. Das lokale
Geräteprofil liegt unter %AppData%\LANReady\device.json und kann in LANReady
unter "Einstellungen" über "Verbindung trennen" sicher entfernt werden.
