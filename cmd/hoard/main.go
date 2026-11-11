// Command hoard runs the self-hosted game-asset database.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/toriath/hoard/internal/ai"
	"github.com/toriath/hoard/internal/config"
	"github.com/toriath/hoard/internal/hoard"
	"github.com/toriath/hoard/internal/server"
	"github.com/toriath/hoard/web"
)

// version is set at build time via -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(healthcheck())
		case "version", "--version", "-v":
			fmt.Println(version)
			return
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "hoard:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("HOARD_LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	svc, err := hoard.Open(cfg.DataDir, log)
	if err != nil {
		return err
	}
	defer svc.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Optional unattended bootstrap of the first account.
	if cfg.AdminUser != "" && cfg.AdminPassword != "" {
		if n, err := svc.UserCount(ctx); err == nil && n == 0 {
			if _, err := svc.CreateUser(ctx, cfg.AdminUser, cfg.AdminPassword); err != nil {
				return fmt.Errorf("bootstrap admin user: %w", err)
			}
			log.Info("created initial user from environment", "username", cfg.AdminUser)
		}
	}

	agent := ai.New(svc, cfg, log)
	go agent.Run(ctx)
	go housekeeping(ctx, svc, cfg, log)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           server.New(svc, agent, cfg, log, version, web.FS()),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No read/write timeouts: uploads and downloads can be very large.
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("hoard is listening", "addr", cfg.Addr, "data", cfg.DataDir, "version", version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// housekeeping runs the periodic maintenance: DB snapshots, expired sessions
// and the trash retention.
func housekeeping(ctx context.Context, svc *hoard.Service, cfg config.Config, log *slog.Logger) {
	tick := func() {
		if err := svc.CleanupSessions(ctx); err != nil {
			log.Warn("session cleanup", "err", err)
		}
		if err := svc.Backup(ctx, cfg.BackupKeep); err != nil {
			log.Warn("backup", "err", err)
		}
		cutoff := time.Now().UTC().AddDate(0, 0, -hoard.ActivityRetentionDays).Format(time.RFC3339)
		if n, err := svc.PruneActivity(ctx, cutoff); err != nil {
			log.Warn("activity cleanup", "err", err)
		} else if n > 0 {
			log.Info("pruned old activity entries", "entries", n)
		}
		if cfg.TrashDays > 0 {
			cutoff := time.Now().UTC().AddDate(0, 0, -cfg.TrashDays).Format(time.RFC3339)
			if n, err := svc.PurgeExpiredTrash(ctx, cutoff); err != nil {
				log.Warn("trash cleanup", "err", err)
			} else if n > 0 {
				log.Info("emptied expired trash", "assets", n)
			}
		}
	}
	tick()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// healthcheck is used by the container HEALTHCHECK (the image has no shell).
func healthcheck() int {
	addr := config.Load().Addr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
