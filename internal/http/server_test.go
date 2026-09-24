package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"lsm/internal/store"
)

func TestRoutes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "lsm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := store.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewHandler(db, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{}))
	defer srv.Close()

	for _, path := range []string{"/healthz", "/", "/kiosk", "/reception", "/supervisor", "/admin", "/admin.js", "/admin.css", "/unreturned"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, res.StatusCode)
		}
	}
}
