package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"draw/internal/api"
	"draw/internal/auth"
	"draw/internal/browser"
	"draw/internal/config"
	"draw/internal/frontier"
	"draw/internal/ingestion"
	"draw/internal/managers"
	"draw/internal/master"
	"draw/internal/model"
	"draw/internal/orchestrator"
	"draw/internal/result"
	"draw/internal/storage"
	"draw/internal/workers"
)

// eventObserverAdapter bridges master.EventObserver (which emits master.Event)
// to storage.EventStore (which persists storage.Event). The master.Event struct
// intentionally omits the storage surrogate key (ID); the adapter leaves it
// as zero and SQLite auto-assigns it on append.
type eventObserverAdapter struct {
	es storage.EventStore
}

func (a *eventObserverAdapter) Emit(ev master.Event) {
	_ = a.es.Append(storage.Event{
		SessionID: ev.SessionID,
		TaskID:    ev.TaskID,
		Kind:      ev.Kind,
		Level:     ev.Level,
		Message:   ev.Message,
		Data:      ev.Data,
		TS:        ev.TS,
	})
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfgPath := strings.TrimSpace(os.Getenv(config.EnvConfigPath))
	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(2)
	}

	dsn := sqliteDSN()
	db, err := storage.Open(dsn)
	if err != nil {
		logger.Error("storage open failed", "err", err)
		os.Exit(2)
	}
	defer db.Close()

	ctx := context.Background()
	if err := storage.Apply(ctx, db); err != nil {
		logger.Error("migrations failed", "err", err)
		os.Exit(2)
	}

	var migrationsApplied int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationsApplied); err != nil {
		logger.Error("count migrations failed", "err", err)
		os.Exit(2)
	}

	es, err := storage.NewSQLiteEvidenceStore(db)
	if err != nil {
		logger.Error("evidence store init failed", "err", err)
		os.Exit(2)
	}

	sqliteSessionStore, err := storage.NewSQLiteSessionStore(db)
	if err != nil {
		logger.Error("session store init failed", "err", err)
		os.Exit(2)
	}
	sqliteTaskStore, err := storage.NewSQLiteTaskStore(db)
	if err != nil {
		logger.Error("task store init failed", "err", err)
		os.Exit(2)
	}
	sqliteEventStore, err := storage.NewSQLiteEventStore(db)
	if err != nil {
		logger.Error("event store init failed", "err", err)
		os.Exit(2)
	}

	// fetch client + http worker
	fc := workers.NewFetchClient(workers.FetchClientOpts{
		UserAgentPool: []string{"draw/1.0 (+https://draw.local)"},
		MaxAttempts:   cfg.Retry.MaxAttempts,
		BaseTimeout:   10 * time.Second,
		MaxRedirects:  5,
	})
	httpWorker := workers.NewHTTPWorker(fc, workers.WorkerOpts{
		UserAgentPool: []string{"draw/1.0 (+https://draw.local)"},
		MaxAttempts:   cfg.Retry.MaxAttempts,
		Timeout:       30 * time.Second,
		MaxRedirects:  5,
	})

	// shared graph
	src, err := storage.NewSQLiteSourceRegistry(db, 0.3)
	if err != nil {
		logger.Error("source registry init failed", "err", err)
		os.Exit(2)
	}
	httpWorker = httpWorker.WithSourceRegistry(src)
	fr := frontier.NewMemoryFrontier(cfg, src)
	rc := orchestrator.NewResourceController(cfg, orchestrator.NewSysSampler())
	sched := orchestrator.NewScheduler(cfg, fr, rc, src, orchestrator.DefaultTaskCapabilities())

	// Phase E: ingestion producer. It owns ONLY the frontier.FrontierSink.Push
	// capability and feeds discovered URLs (from retrieved TaskResult content)
	// into the same MemoryFrontier the Scheduler drains via Next().
	proc := ingestion.NewProcessor(fr, ingestion.WithMaxURLsPerPage(2000))
	var _ master.Ingestion = (*ingestion.Processor)(nil)

	// browser (lazy; fails explicit at Acquire if no Chromium)
	browserTTL := 2 * time.Minute
	crPath := os.Getenv(browser.EnvChromiumPath)
	bmgr := browser.NewBrowserManager(browser.BrowserOpts{
		MaxConcurrency: cfg.Upgrade.BrowserMaxConcurrency,
		DefaultTTL:     browserTTL,
		ChromiumPath:   crPath,
	})

	// managers + router facade
	web := managers.NewWebManager(httpWorker)
	news := managers.NewNewsManager(httpWorker)
	social := managers.NewSocialManager()
	specialized := managers.NewSpecializedManager()
	// authProvider is the single fail-closed SessionAuthProvider (reads
	// DRAW_AUTH_USERS). It is wired into both the Master's Decider (via
	// master.WithBrowserAuth, for canAuthorizeBrowser) and the BrowserAdapter
	// (via WithAuthProvider, for acquireSession) so frontier tasks that carry
	// only a SessionID resolve their user against the same authorization set.
	authProvider := auth.NewSessionAuthFromEnv()
	brow := managers.NewBrowserAdapter(bmgr, browserTTL).
		WithAuthProvider(authProvider).
		WithSessionUser(func(sid model.SessionID) string { return sqliteSessionStore.SessionUserID(string(sid)) })
	router := managers.NewRouterManager(web, news, brow, social, specialized)
	if err := sched.RegisterManager(router, router.Capabilities()); err != nil {
		logger.Error("register manager failed", "err", err)
		os.Exit(2)
	}

	m := master.NewMaster(cfg, sched,
		master.WithSQLiteSessions(sqliteSessionStore),
		master.WithTaskStore(sqliteTaskStore),
		master.WithEventObserver(&eventObserverAdapter{es: sqliteEventStore}),
		master.WithBrowserAuth(authProvider),
		master.WithIngestion(proc),
		master.WithEvidenceStore(es),
		master.WithSourceRegistry(src),
	)

	deps := &api.Deps{
		Master:         m,
		EvidenceStore:  es,
		SourceRegistry: src,
		ReportBuilder:  result.NewReportBuilder(es, src),
		Auth:           auth.LoadConfig(),
	}

	addr := ":8080"
	if v := strings.TrimSpace(os.Getenv("DRAW_HTTP_ADDR")); v != "" {
		addr = v
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cleanupTicker := time.NewTicker(30 * time.Second)
	defer cleanupTicker.Stop()
	go func() {
		for range cleanupTicker.C {
			bmgr.CleanupExpired(time.Now(), browserTTL)
		}
	}()

	logger.Info("rd-engine phase G server",
		"module", "draw",
		"go_version", "1.26.6",
		"addr", addr,
		"max_concurrency", cfg.MaxGlobalConcurrency,
		"admin_enabled", deps.Auth.AdminEnabled(),
		"migrations_applied", migrationsApplied,
		"db", "ok",
	)

	serveErr := api.Serve(ctx, addr, deps, logger)

	_ = bmgr.Close()
	bmgr.CleanupExpired(time.Now(), browserTTL)
	_ = sched.Close()

	if serveErr != nil {
		logger.Error("server failed", "err", serveErr)
		os.Exit(1)
	}
}

func sqliteDSN() string {
	if v := strings.TrimSpace(os.Getenv(storage.EnvSQLiteDSN)); v != "" {
		return v
	}
	dir := filepath.Join(".", "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "file:./draw.db?mode=rwc"
	}
	p := filepath.ToSlash(filepath.Join(dir, "draw.db"))
	return "file:" + p + "?mode=rwc"
}
