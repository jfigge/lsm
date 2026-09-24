// Package http wires routes and middleware. Screen modes are distinguished
// by URL only (/kiosk, /reception, /supervisor, /admin): no separate build
// or configuration on the night.
package http

import (
	"database/sql"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"lsm/web"
)

// NewHandler returns the root handler for the JSON API and static front end.
func NewHandler(db *sql.DB, log *slog.Logger) http.Handler {
	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		panic(err) // embed layout is fixed at build time
	}
	files := http.FileServerFS(static)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	// Screen entry points. Each is a static page for now; the pages
	// themselves arrive with build steps 3, 6 and 7.
	for _, screen := range []string{"kiosk", "reception", "supervisor", "admin"} {
		page := screen + ".html"
		mux.HandleFunc("GET /"+screen, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, static, page)
		})
	}
	mux.Handle("GET /", files)

	return logRequests(log, mux)
}

func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Info("http", "method", r.Method, "path", r.URL.Path,
			"status", rw.status, "dur", time.Since(start).Round(time.Microsecond))
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
