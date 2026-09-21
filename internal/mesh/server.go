package mesh

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// NewHandler serves the peer endpoints, the fleet API and the page.
func NewHandler(src Source, site fs.FS) http.Handler {
	mux := http.NewServeMux()

	// Peers call this every couple of seconds; it stays tiny so it fits in a
	// single packet on any path.
	mux.HandleFunc("GET /mesh/ping", func(w http.ResponseWriter, r *http.Request) {
		local := src.Local()
		writeJSON(w, map[string]any{
			"name":          local.Name,
			"location":      local.Location,
			"uptimeSeconds": int64(time.Since(local.StartedAt).Seconds()),
			"time":          time.Now().UTC().Format(time.RFC3339Nano),
		})
	})
	mux.HandleFunc("GET /mesh/local", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, src.Local())
	})
	mux.HandleFunc("GET /api/mesh", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, src.View(r.Context()))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("GET /", spa(site))
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

// spa serves built assets and falls back to index.html for any other path.
func spa(site fs.FS) http.Handler {
	files := http.FileServerFS(site)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" {
			if st, err := fs.Stat(site, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(site, "index.html")
		if err != nil {
			http.Error(w, "page not built; run the frontend build", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
