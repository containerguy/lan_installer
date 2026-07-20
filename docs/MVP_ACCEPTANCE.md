# LANReady MVP-Vertrag und Review-Gates

Status: Slice 0 wurde am 16.07.2026 durch den Benutzer freigegeben. Slice 1 (Quellenverwaltung) wurde am 16.07.2026 nach unabhängigem UX- und Engineering-Review ohne offene P0/P1 produktiv ausgerollt. Das Gesamt-MVP ist noch nicht abgeschlossen.

## Verbindliche MVP-Bedeutung

LANReady ist erst ein MVP, wenn die vorgesehenen Kernabläufe Ende-zu-Ende funktionieren, ohne bekannte schwerwiegende Fehler nutzbar sind und Management- sowie Windows-Oberfläche konsistent und ansprechend gestaltet sind. Technische Gerüste, rohe CRUD-Formulare, Adapter-Datensätze oder isolierte Endpunkte erfüllen diese Definition nicht.

Optionale Erweiterungen wie Entra-ID-SSO, SMB/NFS, P2P-Verteilung und differenzielles Chunking dürfen nach dem MVP folgen.

## Release-Abnahme

Ein MVP-Release benötigt mindestens folgende nachgewiesene Abläufe:

1. Ein Admin kann ohne Shell eine Quelle anlegen, Zugangsdaten sicher pflegen, die Verbindung testen und verständliche Fehler beheben.
2. Externe Inhalte werden mit Grenzen und Abbruchmöglichkeit geladen, per SHA-256 geprüft und erst danach atomar in einen inhaltsadressierten Cache übernommen.
3. Spiele, Launcher, Versionen und Abhängigkeiten können über getrennte, verständliche UI-Abläufe angelegt, bearbeitet, deaktiviert und – sofern referenzsicher – gelöscht werden.
4. Ein Event kann nur mit vollständigen, validierten Abhängigkeiten veröffentlicht werden. Veröffentlichte Releases sind unveränderlich, signiert und monoton sequenziert. Ein Rollback aktiviert eine frühere unveränderliche Version als neue Sequenz.
5. Ein neuer Windows-11-PC kann den gestalteten Client portabel oder installiert verwenden, sich als Gerät enrollen, Spiele auswählen, Downloads fortsetzen oder abbrechen und verständliche Fortschritts-/Fehlerzustände sehen.
6. Manuelle und unbeaufsichtigte Launcher-Installation haben explizite Zustimmung, nachvollziehbare Rechteerhöhung, Neustart- und Fehlerbehandlung. Ablehnung ist möglich und erzeugt die vereinbarte dauerhafte Warnung.
7. Steam, EA App und Ubisoft Connect werden jeweils auf einem frischen Windows-11-System mit realem Launcher geprüft. Nicht verifizierte Silent-Optionen fallen auf den interaktiven Ablauf zurück. Launcher-Zugangsdaten werden niemals gespeichert.
8. Clientstatus ist einem authentisierten Gerät zugeordnet. API-Kompatibilität und Update-Metadaten sind versioniert; signierte Clientupdates besitzen Anti-Rollback und atomare Wiederherstellung.
9. Management- und Client-UI bestehen die vereinbarten Browser-/Windows-, Tastatur-, Responsive-, Fehlerzustands- und Usability-Abnahmen.
10. Es gibt keine offenen P0- oder P1-Befunde für die MVP-Kernabläufe; Backup/Restore und Deployment-Rollback wurden praktisch getestet.
11. Ein angemeldeter Benutzer kann im Windows-Client installierte Steam-, EA-App- und Ubisoft-Connect-Spiele suchen. Vor der Synchronisation zeigt der Client mindestens Launcher, externe Spiel-ID, erkannte Version und Installationspfad an. Der Scan aktualisiert ausschließlich das Inventar des enrollten Geräts; er verändert den zentralen Spielekatalog niemals automatisch. Ein Admin kann einen Inventareintrag anschließend ausdrücklich einem vorhandenen Katalogeintrag zuordnen oder als neuen Spiele-/Versionsentwurf übernehmen.

   Der Import gilt nur dann als abgeschlossen, wenn die Zuordnung einen aktuellen Scan referenziert, Launcher/externe ID nicht widerspricht, ein neuer Versionsentwurf bis zur Auswahl von Quelle und Quellpfad deaktiviert bleibt und Viewer/Operator die Adminaktion nicht ausführen können.

## UI-Mindeststandard

- Informationsarchitektur mit Dashboard, Quellen, Launchern, Spielen, Versionen, Events und Clients statt einer einzigen Formularseite.
- Listen-, Detail-, Erstellen- und vorbefüllte Bearbeiten-Abläufe; sichere Deaktivierung/Löschung und Aufheben von Zuordnungen.
- Inline-Validierung in verständlichem Deutsch, Eingaben bleiben bei Fehlern erhalten; keine rohen Datenbankfehler.
- Konsistentes Designsystem mit Navigation, Layout-, Typografie-, Farb-, Fokus-, Formular-, Tabellen-, Karten-, Dialog- und Statuskomponenten.
- Such-, Filter- und Sortierfunktionen, verständliche Größen/Datumswerte, Empty-, Loading-, Success-, Warning- und Error-States.
- Rollen wirken in UI und Handlern: Viewer sieht keine Mutationselemente; Operator darf nur die freigegebenen Ressourcen ändern; direkte unberechtigte Requests werden abgewiesen.
- WCAG-2.2-AA-Grundlagen, Tastaturbedienung, sichtbarer Fokus, ausreichender Kontrast und nutzbare Ansichten bei 360, 768 und 1440 Pixel Breite sowie 200 Prozent Zoom.

## Review-Klassifikation

- P0: Sicherheits-, Datenverlust-, Integritäts- oder Kernablauf-Blocker. Stoppt Deployment und Beginn des nächsten Slices.
- P1: Wesentliche unvollständige Kernfunktion oder erheblicher Bedien-/Qualitätsmangel. Stoppt die Abnahme des betroffenen Slices.
- P2: Begrenzter oder optionaler Mangel. Darf nur dokumentiert mit geplantem Folgeschritt offenbleiben.

## Gates je Slice

- Vor Umsetzung: verifizierte Anforderungen, Happy-/Error-Flows, Daten-/API-Vertrag und testbare Abnahmekriterien.
- Während Umsetzung: Unit-, Integrations- und Negativtests; bei UI zusätzlich Browser-/Accessibility-/Visual-Prüfung.
- Vor Deployment: unabhängiger Review für alle P0-/P1-relevanten Änderungen. Auth, Secrets, Cache, Signer, Publish, Installer, Updates, Migrationen und Rechteerhöhung benötigen zusätzlich Security-/Datenintegritätsreview.
- Nach Deployment: Smoke-/Ende-zu-Ende-Test gegen die produktionsgleiche Umgebung und dokumentierter Rollback.

## Aktuelle Einordnung

Der derzeit ausgerollte Managementserver ist noch kein nach diesem Vertrag abgeschlossenes MVP. Lokal sind der vollständige Quellenablauf, persistente HTTPS/WebDAV-Cache-Aufträge mit Managementstatus und harter Quota, kontrollierte manuelle Cache-Garbage-Collection, Geräte-Enrollment, Browser-Einmalcode-Anmeldung, Launcher-Spieleerkennung, kataloggebundene manuelle Registrierung kopierter Spiele, bestätigte Inventarsynchronisation, Admin-Katalogimport, eine gestaltete native Windows-GUI, der verifizierende SHA-256-CAS-Kern mit authentifizierten Range-Downloads, ein per-user Agentmodus sowie Veröffentlichung und atomare Installation signierter Clientupdates mit Anti-Rollback und Frontend-Health-Wiederherstellung umgesetzt. Der reale Nextcloud-Download, Abbruch/Retry, CAS-Wiederverwendung, SHA-256-Prüfung und das TrueNAS-NFS-Backend wurden Ende-zu-Ende geprüft. Portable ZIP und per-user Installer werden reproduzierbar gebaut, sind aber noch nicht Authenticode-signiert. Es fehlen insbesondere der automatisch aus Katalog/CAS erzeugte Event-Kandidat, die GUI-Installations-/Updateorchestrierung für Spiele und Launcher sowie reale Windows-11-Adapter-, manuelle-EXE-, Task-Scheduler-, Benachrichtigungs- und Self-Update-Tests mit einem öffentlich vertrauenswürdigen, zeitgestempelten Build.
