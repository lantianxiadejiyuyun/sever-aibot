// Package web embeds the built Vue console. Build it with:
//
//	cd web && npm ci && npm run build
//
// The Go binary then serves the console from a single file.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Handler serves static files and falls back to index.html for client routes.
func Handler() http.Handler {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "web console not built", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if clean == "." || clean == "" {
			clean = "index.html"
		}
		if info, err := fs.Stat(sub, clean); err != nil || info.IsDir() {
			// /assets is also an application route. Never hand directories to
			// FileServer: its redirect would conflict with the SPA inventory.
			clean = "index.html"
		}
		if strings.HasPrefix(clean, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		if clean == "index.html" {
			// Serve index directly: http.FileServer would redirect
			// /index.html to ./, which can loop on the root path.
			data, err := fs.ReadFile(sub, "index.html")
			if err != nil {
				http.Error(w, "web console not built", http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + clean
		files.ServeHTTP(w, r2)
	})
}
