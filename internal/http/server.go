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

	"lsm/internal/signup"
	"lsm/web"
)

// Options configures the handler.
type Options struct {
	// Location is the venue's time zone, for local times such as shift
	// windows.
	Location *time.Location
	// Notifier tells staff a month has opened; nil sends nothing.
	Notifier signup.Notifier
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// NewHandler returns the root handler for the JSON API and static front end.
func NewHandler(db *sql.DB, log *slog.Logger, opt Options) http.Handler {
	static, err := fs.Sub(web.FS, "static")
	if err != nil {
		panic(err) // embed layout is fixed at build time
	}
	files := http.FileServerFS(static)

	if opt.Location == nil {
		opt.Location = time.UTC
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}

	mux := http.NewServeMux()
	a := &api{db: db, log: log, opt: opt, now: opt.Now}
	a.authRoutes(mux)
	a.signupRoutes(mux)
	a.checkinRoutes(mux)
	a.assignRoutes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]string{"code": "not_found", "message": "no such endpoint"}})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	// Screen entry points. Each is a static page for now; the pages
	// themselves arrive with later build steps (SPEC §8).
	for _, screen := range []string{"kiosk", "reception", "unreturned", "sheet", "supervisor", "admin"} {
		page := screen + ".html"
		mux.HandleFunc("GET /"+screen, func(w http.ResponseWriter, r *http.Request) {
			http.ServeFileFS(w, r, static, page)
		})
	}
	// Not "GET /": that would conflict with the method-less /api/
	// catch-all, which answers unknown API paths with JSON.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		files.ServeHTTP(w, r)
	})

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
