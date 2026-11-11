// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"net/netip"
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

	// TrustedProxies (HOARD_TRUSTED_PROXIES, comma-separated IPs and CIDRs):
	// reverse proxies whose X-Forwarded-For header is believed when throttling
	// sign-ins. Empty trusts no one, so the header is ignored.
	TrustedProxies []netip.Prefix

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
	var bad []string
	c.TrustedProxies, bad = ParseProxies(env("HOARD_TRUSTED_PROXIES", ""))
	for _, b := range bad {
		fmt.Fprintf(os.Stderr, "hoard: ignoring invalid HOARD_TRUSTED_PROXIES entry %q\n", b)
	}
	return c
}

// ParseProxies parses a comma-separated list of IP addresses and CIDR ranges.
// Entries that are neither are returned in bad.
func ParseProxies(list string) (ok []netip.Prefix, bad []string) {
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if p, err := netip.ParsePrefix(e); err == nil {
			ok = append(ok, netip.PrefixFrom(p.Addr().Unmap(), max(p.Bits()-bitsOffset(p.Addr()), 0)).Masked())
		} else if a, err := netip.ParseAddr(e); err == nil {
			a = a.Unmap()
			ok = append(ok, netip.PrefixFrom(a, a.BitLen()))
		} else {
			bad = append(bad, e)
		}
	}
	return ok, bad
}

// bitsOffset is 96 for an IPv4-mapped IPv6 prefix such as ::ffff:10.0.0.0/104,
// which Unmap turns into an IPv4 prefix, and 0 otherwise.
func bitsOffset(a netip.Addr) int {
	if a.Is4In6() {
		return 96
	}
	return 0
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
