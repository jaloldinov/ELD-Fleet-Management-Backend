// Package web is the browser frontend: plain HTML, CSS and JavaScript (no
// build step), embedded in the API binary and served at /app/.
//
// Same origin as the API, so the browser needs no CORS rules, and the page
// gets its own Content-Security-Policy (middleware.AppSecurityHeaders).
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// Files returns the frontend files, rooted at static/ (index.html, ...).
func Files() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // "static" is embedded above: cannot fail
	}
	return sub
}
