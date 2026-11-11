// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime settings. Everything has a sensible default so the
// container runs with zero configuration.
type Config struct {
	Addr           string // HOARD_ADDR: listen address
	DataDir        string // HOARD_DATA_DIR: root of all persistent data
	PublicURL      string // HOARD_PUBLIC_URL: optional canonical base URL used in generated links
	MaxUploadBytes int64  // HOARD_MAX_UPLOAD_MB: per-file upload limit
	BackupKeep     int    // HOARD_BACKUP_KEEP: number of daily DB snapshots to keep (0 disables)
	TrashDays      int    // HOARD_TRASH_DAYS: auto-purge trashed assets after N days (0 disables)

	// Optional first-run bootstrap; otherwise the web UI asks on first visit.
	AdminUser     string // HOARD_ADMIN_USER
	AdminPassword string // HOARD_ADMIN_PASSWORD

	// Optional AI defaults; values saved in the web UI take precedence.
	AIProvider string // HOARD_AI_PROVIDER: "anthropic" | "openai" (LM Studio & friends) | ""
	AIBaseURL  string // HOARD_AI_BASE_URL
	AIModel    string // HOARD_AI_MODEL
	AIKey      string // HOARD_AI_API_KEY (falls back to ANTHROPIC_API_KEY)
}

// Load reads configuration from the environment.
func Load() Config {
	c := Config{
		Addr:           env("HOARD_ADDR", ":8080"),
		DataDir:        env("HOARD_DATA_DIR", "./data"),
		PublicURL:      strings.TrimRight(env("HOARD_PUBLIC_URL", ""), "/"),
		MaxUploadBytes: int64(envInt("HOARD_MAX_UPLOAD_MB", 4096)) << 20,
		BackupKeep:     envInt("HOARD_BACKUP_KEEP", 7),
		TrashDays:      envInt("HOARD_TRASH_DAYS", 30),
		AdminUser:      env("HOARD_ADMIN_USER", ""),
		AdminPassword:  env("HOARD_ADMIN_PASSWORD", ""),
		AIProvider:     env("HOARD_AI_PROVIDER", ""),
		AIBaseURL:      env("HOARD_AI_BASE_URL", ""),
		AIModel:        env("HOARD_AI_MODEL", ""),
		AIKey:          env("HOARD_AI_API_KEY", os.Getenv("ANTHROPIC_API_KEY")),
	}
	return c
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(env(key, "")); err == nil && v >= 0 {
		return v
	}
	return def
}
