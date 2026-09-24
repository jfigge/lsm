// Command lsm is the Lenovo Staff Manager server: one binary serving the
// JSON API and the static front end, plus the operational subcommands.
//
//	lsm serve     apply pending migrations, then serve HTTP
//	lsm migrate   apply pending migrations and exit
//	lsm export    write a full JSON archive to stdout (or -o FILE)
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"lsm/internal/archive"
	"lsm/internal/config"
	lsmhttp "lsm/internal/http"
	"lsm/internal/store"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(os.Args[1:], log); err != nil {
		fmt.Fprintln(os.Stderr, "lsm:", err)
		os.Exit(1)
	}
}

func run(args []string, log *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("usage: lsm serve|migrate|export [flags]")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "serve":
		return serve(ctx, cfg, log)
	case "migrate":
		return migrate(ctx, cfg, log)
	case "export":
		return export(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func openDB(cfg config.Config) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o750); err != nil {
		return nil, err
	}
	return store.Open(cfg.DBPath)
}

func migrate(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	return applyMigrations(ctx, db, log)
}

func applyMigrations(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	applied, err := store.Migrate(ctx, db)
	for _, m := range applied {
		log.Info("migration applied", "name", m)
	}
	if err != nil {
		return err
	}
	v, err := store.SchemaVersion(ctx, db)
	if err != nil {
		return err
	}
	log.Info("schema up to date", "version", v)
	return nil
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	// The container has no separate migrate step; startup is idempotent.
	if err := applyMigrations(ctx, db, log); err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           lsmhttp.NewHandler(db, log),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "db", cfg.DBPath)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func export(ctx context.Context, cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	out := fs.String("o", "", "write the archive to `file` instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	db, err := openDB(cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	var w io.Writer = os.Stdout
	if *out != "" {
		// Write beside the target and rename, so a crash never leaves a
		// truncated file where a good backup is expected.
		tmp, err := os.CreateTemp(filepath.Dir(*out), ".lsm-export-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if err := archive.Export(ctx, db, tmp); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), *out)
	}
	return archive.Export(ctx, db, w)
}
