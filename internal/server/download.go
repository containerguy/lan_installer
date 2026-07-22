package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// clientDownloadName is the only file this endpoint will ever serve.
//
// The name is fixed rather than taken from the request: a public file endpoint
// that accepts any path is a traversal bug waiting to happen, and the client
// installer is the single file that has to be reachable before a device owns
// any credentials.
const clientDownloadName = "LANReady.exe"

// clientDownloadDir is where the operator places the current client build,
// relative to the server data directory.
const clientDownloadDir = "download"

func (s *Server) clientDownloadPath() string {
	return filepath.Join(s.dataDir, clientDownloadDir, clientDownloadName)
}

// downloadClient serves the portable Windows client so a new PC can obtain it
// before it is enrolled. It is intentionally unauthenticated: enrolling still
// requires a one-time code, and the binary carries no secrets.
func (s *Server) downloadClient(w http.ResponseWriter, r *http.Request) {
	path := s.clientDownloadPath()
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "Es ist derzeit kein Client zum Herunterladen hinterlegt.", http.StatusNotFound)
			return
		}
		http.Error(w, "Der Client konnte nicht gelesen werden.", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "Der hinterlegte Client ist keine reguläre Datei.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	w.Header().Set("Content-Disposition", `attachment; filename="`+clientDownloadName+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, clientDownloadName, info.ModTime(), file)
}

// clientDownloadInfo publishes size and digest so a download can be verified
// without trusting the transfer, and so the page can link the file only when
// one is actually present.
func (s *Server) clientDownloadInfo(w http.ResponseWriter, r *http.Request) {
	path := s.clientDownloadPath()
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	digest, err := fileDigest(path)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"fileName":  clientDownloadName,
		"size":      info.Size(),
		"sha256":    digest,
		"modified":  info.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
		"url":       "/download/" + strings.ToLower(clientDownloadName),
	})
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// downloadPage is a minimal landing page so a LAN guest can find the client
// with a URL alone, including the digest to check against.
func (s *Server) downloadPage(w http.ResponseWriter, r *http.Request) {
	path := s.clientDownloadPath()
	info, statErr := os.Stat(path)
	body := `<!doctype html><html lang="de"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>LANReady herunterladen</title>
<style>
body{font-family:system-ui,sans-serif;background:#11131a;color:#e8eaf0;margin:0;padding:40px 20px;line-height:1.6}
main{max-width:640px;margin:0 auto}
h1{font-size:1.6rem;margin:0 0 8px}
a.button{display:inline-block;background:#5b7cfa;color:#fff;padding:12px 22px;border-radius:10px;text-decoration:none;font-weight:600;margin:18px 0}
code{background:#1c1f2a;padding:2px 6px;border-radius:5px;word-break:break-all;font-size:.85em}
.meta{color:#9aa3b8;font-size:.92em}
ol{padding-left:20px}
</style></head><body><main>
<h1>LANReady-Client</h1>`
	if statErr != nil || !info.Mode().IsRegular() {
		body += `<p>Derzeit ist kein Client hinterlegt. Bitte wende dich an die Administration.</p>`
	} else {
		digest, _ := fileDigest(path)
		body += fmt.Sprintf(`<p class="meta">Portable Anwendung für Windows 11 · %.1f MB</p>
<a class="button" href="/download/lanready.exe">LANReady.exe herunterladen</a>
<ol>
<li>Datei starten. Windows SmartScreen meldet einen unbekannten Herausgeber, weil die Datei nicht Authenticode-signiert ist: <em>Weitere Informationen</em> → <em>Trotzdem ausführen</em>.</li>
<li>Im Client <strong>PC verbinden</strong> wählen und den Enrollment-Code eingeben, den ein Administrator erzeugt.</li>
</ol>
<p class="meta">Prüfsumme zum Vergleich (SHA-256):<br><code>%s</code></p>
<p class="meta">Unter Windows prüfen mit:<br><code>Get-FileHash .\LANReady.exe -Algorithm SHA256</code></p>`,
			float64(info.Size())/1e6, digest)
	}
	body += `</main></body></html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(w, body)
}
