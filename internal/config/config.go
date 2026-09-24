// Package config loads runtime configuration from the environment.
//
// Everything that differs between a laptop and EC2 is an environment
// variable, so the same image runs in both places. Operational settings an
// admin changes at runtime (backup schedule, etc.) live in the database's
// settings table, not here.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is the process configuration.
type Config struct {
	// Addr is the HTTP listen address. LSM_ADDR, default ":8080".
	Addr string
	// DBPath is the SQLite database file. LSM_DB_PATH, default "data/lsm.db".
	DBPath string
	// BackupDir is where scheduled exports are written: a mounted
	// secondary volume in deployment. LSM_BACKUP_DIR, default "backup".
	BackupDir string
	// SeedDir holds demo seed data. LSM_SEED_DIR, default "seed".
	SeedDir string
	// Location is the venue's time zone, used for local wall-clock times
	// such as shift windows. LSM_TZ, default "America/New_York".
	Location *time.Location
}

// Load reads the configuration from the environment.
func Load() (Config, error) {
	c := Config{
		Addr:      env("LSM_ADDR", ":8080"),
		DBPath:    env("LSM_DB_PATH", "data/lsm.db"),
		BackupDir: env("LSM_BACKUP_DIR", "backup"),
		SeedDir:   env("LSM_SEED_DIR", "seed"),
	}
	if c.DBPath == "" {
		return c, fmt.Errorf("config: LSM_DB_PATH is empty")
	}
	loc, err := time.LoadLocation(env("LSM_TZ", "America/New_York"))
	if err != nil {
		return c, fmt.Errorf("config: LSM_TZ: %w", err)
	}
	c.Location = loc
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return def
}
