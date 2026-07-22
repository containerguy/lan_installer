package webadmin

import (
	_ "embed"
	"net/http"
)

//go:embed assets/management.css
var managementCSS []byte

//go:embed assets/sources.js
var sourcesJS []byte

//go:embed assets/catalog.js
var catalogJS []byte

//go:embed assets/catalog_cache_helpers.js
var catalogCacheHelpersJS []byte

//go:embed assets/catalog_assignment_helpers.js
var catalogAssignmentHelpersJS []byte

//go:embed assets/clients.js
var clientsJS []byte

func managementCSSAsset(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(managementCSS)
}

func sourcesJSAsset(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(sourcesJS)
}

func catalogJSAsset(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(catalogJS)
}

func catalogCacheHelpersJSAsset(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(catalogCacheHelpersJS)
}

func catalogAssignmentHelpersJSAsset(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(catalogAssignmentHelpersJS)
}

func clientsJSAsset(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(clientsJS)
}
