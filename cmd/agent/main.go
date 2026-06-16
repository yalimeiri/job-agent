// Command agent is the Job Search & Auto-Apply Agent entry point. It loads
// config, runs DB migrations, wires the components and runs the cycle once or
// as a daemon, with graceful shutdown on SIGINT/SIGTERM.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/yalimeiri/job-agent/internal/agent"
	"github.com/yalimeiri/job-agent/internal/applier"
	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/matcher"
	"github.com/yalimeiri/job-agent/internal/notifier"
	"github.com/yalimeiri/job-agent/internal/ratelimit"
	"github.com/yalimeiri/job-agent/internal/scraper"
	"github.com/yalimeiri/job-agent/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath    = flag.String("config", "config.yaml", "path to config YAML")
		migrationsDir = flag.String("migrations", "./migrations", "path to migrations directory")
		once          = flag.Bool("once", false, "run a single cycle and exit")
		skipMigrate   = flag.Bool("skip-migrate", false, "do not run migrations on startup")
		dryRun        = flag.Bool("dry-run", false, "force dry-run mode (overrides config)")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *dryRun {
		cfg.Agent.DryRun = true
	}

	log := newLogger(cfg.Agent.LogLevel)
	slog.SetDefault(log)

	// Migrations first: a fatal error on startup is acceptable per the spec.
	if !*skipMigrate {
		log.Info("running migrations", "dir", *migrationsDir)
		if err := store.Migrate(cfg.Database.DSN, *migrationsDir); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.New(ctx, cfg.Database.DSN)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer st.Close()

	if err := syncProfile(ctx, st, cfg.Profile); err != nil {
		log.Warn("profile sync failed", "err", err)
	}

	// Build collaborators.
	deps := scraper.Deps{
		HTTPClient: &http.Client{Timeout: 30 * time.Second},
		Limiters:   ratelimit.NewRegistry(),
		Logger:     log,
	}
	scrapers, err := scraper.NewRegistry().BuildAll(cfg, deps)
	if err != nil {
		return fmt.Errorf("build scrapers: %w", err)
	}
	log.Info("scrapers ready", "count", len(scrapers))

	mt, err := matcher.NewClaude(cfg.Matching, log)
	if err != nil {
		return fmt.Errorf("build matcher: %w", err)
	}

	nt := buildNotifier(cfg, log)

	eng := applier.NewEngine(st, applier.Options{
		ScreenshotDir: "./screenshots",
		Headless:      true,
		Logger:        log,
	})
	defer eng.Close()

	a := agent.New(cfg, agent.Deps{
		Store:    st,
		Scrapers: scrapers,
		Matcher:  mt,
		Applier:  eng,
		Notifier: nt,
		Logger:   log,
	})

	if *once {
		_, err := a.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			return err
		}
		return nil
	}
	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func buildNotifier(cfg *config.Config, log *slog.Logger) notifier.Notifier {
	if cfg.Notifier.Type != "telegram" {
		log.Warn("no telegram notifier configured; notifications disabled")
		return notifier.Nop{}
	}
	tg, err := notifier.NewTelegram(cfg.Notifier.Config, log)
	if err != nil {
		log.Warn("telegram notifier disabled", "err", err)
		return notifier.Nop{}
	}
	return tg
}

// syncProfile pushes config profile values into the DB profile table so the
// applier and matcher share one source of truth.
func syncProfile(ctx context.Context, st store.Store, p config.ProfileConfig) error {
	first, last := splitName(p.FullName)
	fields := []struct{ key, val, typ string }{
		{"full_name", p.FullName, "text"},
		{"first_name", first, "text"},
		{"last_name", last, "text"},
		{"email", p.Email, "text"},
		{"phone", p.Phone, "text"},
		{"linkedin_url", p.LinkedInURL, "text"},
		{"cv_path", p.CVPath, "file"},
	}
	for _, f := range fields {
		if f.val == "" {
			continue
		}
		if err := st.UpsertProfileField(ctx, f.key, f.val, f.typ); err != nil {
			return err
		}
	}
	return nil
}

func splitName(full string) (first, last string) {
	parts := strings.Fields(full)
	switch len(parts) {
	case 0:
		return "", ""
	case 1:
		return parts[0], ""
	default:
		return parts[0], strings.Join(parts[1:], " ")
	}
}
