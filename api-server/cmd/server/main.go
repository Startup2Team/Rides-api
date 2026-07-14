package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	chiCors "github.com/go-chi/cors"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/admin"
	"github.com/workspace/ride-platform/internal/analytics"
	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/bonus"
	"github.com/workspace/ride-platform/internal/customer"
	"github.com/workspace/ride-platform/internal/dashboard"
	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/internal/fare"
	"github.com/workspace/ride-platform/internal/finance"
	"github.com/workspace/ride-platform/internal/inbox"
	"github.com/workspace/ride-platform/internal/incidents"
	"github.com/workspace/ride-platform/internal/location"
	"github.com/workspace/ride-platform/internal/matching"
	mw "github.com/workspace/ride-platform/internal/middleware"
	"github.com/workspace/ride-platform/internal/negotiation"
	"github.com/workspace/ride-platform/internal/notification"
	"github.com/workspace/ride-platform/internal/packages"
	"github.com/workspace/ride-platform/internal/payment"
	"github.com/workspace/ride-platform/internal/rating"
	"github.com/workspace/ride-platform/internal/reports"
	"github.com/workspace/ride-platform/internal/ride"
	"github.com/workspace/ride-platform/internal/settings"
	"github.com/workspace/ride-platform/internal/team"
	"github.com/workspace/ride-platform/internal/telephony"
	"github.com/workspace/ride-platform/internal/tickets"
	"github.com/workspace/ride-platform/internal/tracking"
	"github.com/workspace/ride-platform/internal/upload"
	"github.com/workspace/ride-platform/internal/wallet"
	"github.com/workspace/ride-platform/pkg/adminrole"
	"github.com/workspace/ride-platform/pkg/audit"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
	"github.com/workspace/ride-platform/pkg/logger"
	pgpkg "github.com/workspace/ride-platform/pkg/postgres"
	rdpkg "github.com/workspace/ride-platform/pkg/redis"
	"github.com/workspace/ride-platform/pkg/respond"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config load:", err)
		os.Exit(1)
	}

	log := logger.New(cfg.Env)
	log.Info().Str("env", cfg.Env).Str("port", cfg.Port).Msg("ride-platform: starting")

	// ── Production safety guards ──────────────────────────────────────────────
	// Catch dev-only flags that must never be true in production.
	// A misconfigured deploy would silently bypass geofences or skip driver approval.
	if cfg.Env == "production" {
		if cfg.Ride.DevSkipGeofence {
			log.Fatal().Msg("FATAL: DEV_SKIP_GEOFENCE=true in production — refusing to start")
		}
		if cfg.Driver.DevAutoApprove {
			log.Fatal().Msg("FATAL: DEV_AUTO_APPROVE_DRIVERS=true in production — refusing to start")
		}
		if cfg.JWT.AccessSecret == "local_dev_access_secret_change_before_any_shared_environment_0123456789" {
			log.Fatal().Msg("FATAL: Default development JWT_ACCESS_SECRET detected in production — refusing to start")
		}
		if cfg.JWT.RefreshSecret == "local_dev_refresh_secret_change_before_any_shared_environment_0123456789" {
			log.Fatal().Msg("FATAL: Default development JWT_REFRESH_SECRET detected in production — refusing to start")
		}
		if cfg.JWT.AdminAccessSecret == cfg.JWT.AccessSecret {
			log.Fatal().Msg("FATAL: JWT_ADMIN_ACCESS_SECRET must be different from JWT_ACCESS_SECRET in production — refusing to start")
		}
		if cfg.JWT.AdminAccessSecret == "local_dev_access_secret_change_before_any_shared_environment_0123456789" {
			log.Fatal().Msg("FATAL: Default development JWT_ADMIN_ACCESS_SECRET detected in production — refusing to start")
		}
		if len(cfg.JWT.AccessSecret) < 32 {
			log.Fatal().Msg("FATAL: JWT_ACCESS_SECRET is too short (< 32 chars) in production — refusing to start")
		}
		if len(cfg.JWT.RefreshSecret) < 32 {
			log.Fatal().Msg("FATAL: JWT_REFRESH_SECRET is too short (< 32 chars) in production — refusing to start")
		}
		if len(cfg.JWT.AdminAccessSecret) < 32 {
			log.Fatal().Msg("FATAL: JWT_ADMIN_ACCESS_SECRET is too short (< 32 chars) in production — refusing to start")
		}
		if cfg.JWT.AccessExpiryMinutes > 60 {
			log.Fatal().Msg("FATAL: JWT_ACCESS_EXPIRY_MINUTES is > 60 minutes in production — refusing to start")
		}
		if os.Getenv("TOTP_ENCRYPTION_KEY") == "" || os.Getenv("TOTP_ENCRYPTION_KEY") == "default-dev-totp-encryption-key-do-not-use-in-prod" {
			log.Fatal().Msg("FATAL: TOTP_ENCRYPTION_KEY is empty or using development default in production — refusing to start")
		}
		if cfg.Payments.Enabled && cfg.MoMo.APIKey == "" {
			log.Fatal().Msg("FATAL: Payments are enabled in production but MOMO_API_KEY is not set — refusing to start")
		}
		hasMomoCreds := cfg.MoMo.APIKey != "" || cfg.MoMo.SubscriptionKey != "" || cfg.MoMo.APIUser != ""
		if (cfg.Payments.Enabled || hasMomoCreds) && cfg.Payments.WebhookSecret == "" {
			log.Fatal().Msg("FATAL: Payments are enabled or MoMo credentials are configured in production but PAYMENTS_WEBHOOK_SECRET is not set — refusing to start")
		}
		if cfg.Payments.Enabled && cfg.MoMo.APIKey == "" {
			log.Fatal().Msg("FATAL: Payments are enabled in production but MOMO_API_KEY is not set — refusing to start")
		}
	} else {
		if cfg.Ride.DevSkipGeofence {
			log.Warn().Msg("⚠️  DEV_SKIP_GEOFENCE=true — geofence checks disabled (dev only)")
		}
		if cfg.Driver.DevAutoApprove {
			log.Warn().Msg("⚠️  DEV_AUTO_APPROVE_DRIVERS=true — driver approval skipped (dev only)")
		}
		if os.Getenv("JWT_ADMIN_ACCESS_SECRET") == "" {
			log.Warn().Msg("⚠️  JWT_ADMIN_ACCESS_SECRET is not set, falling back to JWT_ACCESS_SECRET (dev only)")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── Database ──────────────────────────────────────────────────────────────
	db, err := pgpkg.New(ctx, cfg.Database.URL, cfg.Database.MaxConns, cfg.Database.MinConns)
	if err != nil {
		log.Fatal().Err(err).Msg("postgres: connect")
	}
	defer db.Close()
	log.Info().Msg("postgres: connected")

	dbRead, err := pgpkg.New(ctx, cfg.Database.ReadURL, cfg.Database.MaxConns, cfg.Database.MinConns)
	if err != nil {
		log.Fatal().Err(err).Msg("postgres read-replica: connect")
	}
	defer dbRead.Close()
	log.Info().Msg("postgres read-replica: connected")

	// ── Redis ─────────────────────────────────────────────────────────────────
	rdb, err := rdpkg.New(ctx, cfg.Redis.URL, cfg.Redis.ClusterMode)
	if err != nil {
		log.Fatal().Err(err).Msg("redis: connect")
	}
	defer rdb.Close()
	log.Info().Msg("redis: connected")

	// ── Migrations ────────────────────────────────────────────────────────────
	if err := runMigrations(cfg.Database.URL); err != nil {
		log.Fatal().Err(err).Msg("migrations: failed")
	}
	log.Info().Msg("migrations: up to date")

	// ── Core services ─────────────────────────────────────────────────────────
	telSvc := telephony.New(cfg, log)
	notifySvc := notification.New(cfg, log)
	paymentSvc := payment.New(cfg, log)
	anaSvc := analytics.NewService(db, rdb, log)
	anaRepo := analytics.NewRepository(dbRead)

	// ── Repositories ──────────────────────────────────────────────────────────
	authRepo := auth.NewRepository(db)
	custRepo := customer.NewRepository(db)
	driverRepo := driver.NewRepository(db)
	rideRepo := ride.NewRepository(db)
	negRepo := negotiation.NewRepository(db)
	fareRepo := fare.NewRepository(db)
	pkgRepo := packages.NewRepository(db)
	walletRepo := wallet.NewRepository(db)
	bonusRepo := bonus.NewRepository(db)

	// ── New module repositories ───────────────────────────────────────────────
	incidentRepo := incidents.NewRepository(db)
	ticketRepo := tickets.NewRepository(db)
	inboxRepo := inbox.NewRepository(db)
	reportRepo := reports.NewRepository(db)
	settingsRepo := settings.NewRepository(db)
	teamRepo := team.NewRepository(db)

	// ── WebSocket hub ─────────────────────────────────────────────────────────
	// Redis-backed so WebSocket delivery works across multiple API instances.
	hub := tracking.NewHub(rdb, log) // starts its Redis pub/sub subscriber internally

	// ── Domain services ───────────────────────────────────────────────────────
	authSvc := auth.NewService(authRepo, rdb, telSvc, cfg, log)
	driverSvc := driver.NewService(driverRepo, rdb, anaSvc, cfg, log)
	walletSvc := wallet.NewService(walletRepo, log, cfg.Payments.Enabled)
	bonusSvc := bonus.NewService(bonusRepo, log)
	pkgSvc := packages.NewService(pkgRepo, log)
	pkgSvc.SetWallet(walletSvc) // wallet deduction on package purchase
	// Note: the go-online credit gate is wired to the v4 ledger (ledgerSvc) below,
	// once it is constructed — NOT to pkgSvc, whose HasCredits reads the legacy
	// driver_ride_credits table that the v4 cutover no longer populates.
	// rideSvc needs hub for WS notifications; engine is set after construction
	rideSvc := ride.NewService(rideRepo, rdb, notifySvc, anaSvc, hub, cfg, log)
	// engine needs rideSvc for negotiation timeout; rideSvc needs engine for matching
	engine := matching.NewEngine(rideRepo, driverRepo, rdb, notifySvc, anaSvc, hub, cfg, log, rideSvc)
	negSvc := negotiation.NewService(negRepo, rideRepo, rdb, hub, telSvc, anaSvc, cfg, log)
	rideSvc.SetFareRepository(fareRepo)
	negSvc.SetFareRepository(fareRepo)
	auditLog := audit.New(db)
	// Wire the timeout manager so negotiation activity resets the 5-minute clock.
	negSvc.SetTimeoutManager(rideSvc)
	notifRepo := notification.NewRepository(db)
	notifySvc.SetRepository(notifRepo)
	adminSvc := admin.NewService(db, log)
	adminSvc.SetRedis(rdb)
	locSvc := location.NewService(db, rdb, cfg, log)

	// ── New module services ───────────────────────────────────────────────────
	incidentSvc := incidents.NewService(incidentRepo)
	ticketSvc := tickets.NewService(ticketRepo)
	inboxSvc := inbox.NewService(inboxRepo)
	reportSvc := reports.NewService(reportRepo)
	settingsSvc := settings.NewService(settingsRepo)
	teamSvc := team.NewService(teamRepo, cfg, rdb)
	dashSvc := dashboard.NewService(dbRead, rdb, log)

	financeRepo := finance.NewRepository(db)
	financeSvc := finance.NewService(financeRepo)
	financeH := finance.NewHandler(financeSvc)

	// ── Handlers ──────────────────────────────────────────────────────────────
	custSvc := customer.NewService(custRepo, log)

	authH := auth.NewHandler(authSvc, cfg.Env)
	authH.SetDriverService(driverSvc) // force-offline driver on logout
	custH := customer.NewHandler(custSvc)
	driverH := driver.NewHandler(driverSvc)
	rideH := ride.NewHandler(rideSvc)
	negH := negotiation.NewHandler(negSvc)
	adminH := admin.NewHandler(adminSvc, authSvc, auditLog, cfg.Env)
	anaH := analytics.NewHandler(anaRepo)
	trackH := tracking.NewHandler(hub, driverSvc, rdb, cfg, log)
	locH := location.NewHandler(locSvc, rideSvc)
	fareH := fare.NewHandler(fareRepo, locSvc)
	ledgerSvc := packages.NewLedgerService(pkgRepo, log) // v4 entitlement ledger
	// Go-online credit gate reads the same v4 ledger that ride deduction debits,
	// so a driver with a real v4 package isn't wrongly blocked with NO_CREDITS.
	driverSvc.SetCreditChecker(ledgerSvc)
	// Auto-confirm purchases ONLY when real payments are off in a non-prod env
	// (mobile dev without MoMo creds). With PAYMENTS_ENABLED=true the real
	// gateway + reconcile path runs even locally, so the MTN sandbox flow can be
	// exercised end-to-end before production. Never auto-confirms in prod.
	devAutoConfirm := cfg.Env != "production" && !cfg.Payments.Enabled
	purchaseSvc := packages.NewPurchaseService(pkgRepo, ledgerSvc, momoGateway{paymentSvc}, devAutoConfirm, log)
	pkgH := packages.NewHandler(pkgSvc, auditLog, cfg)
	pkgH.SetBonus(bonusSvc)       // auto-grant purchase bonuses
	pkgH.SetLedger(ledgerSvc)     // v4 entitlements
	pkgH.SetPurchase(purchaseSvc) // v4 purchase + MoMo
	bonusH := bonus.NewHandler(bonusSvc)
	walletH := wallet.NewHandler(walletSvc)
	var uploadH *upload.Handler
	if uh, err := upload.NewHandler(cfg); err != nil {
		log.Warn().Err(err).Msg("upload: storage not configured, presign endpoint disabled")
	} else {
		uploadH = uh
	}

	ratingRepo := rating.NewRepository(db)
	ratingH := rating.NewHandler(ratingRepo, log)
	notifH := notification.NewHandler(notifRepo)

	// New module handlers
	incidentH := incidents.NewHandler(incidentSvc)
	ticketH := tickets.NewHandler(ticketSvc)
	inboxH := inbox.NewHandler(inboxSvc)
	reportH := reports.NewHandler(reportSvc)
	settingsH := settings.NewHandler(settingsSvc)
	teamH := team.NewHandler(teamSvc, auditLog)
	dashH := dashboard.NewHandler(dashSvc)

	// ── Background goroutines ─────────────────────────────────────────────────
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	consumer := analytics.NewConsumer(rdb, cfg.Analytics.BatchSize, log)
	go consumer.Run(bgCtx)

	// ── Orphaned-ride recovery ────────────────────────────────────────────────
	// After a crash or deploy restart, any ride in SEARCHING or NEGOTIATING
	// has lost its in-memory goroutine/timer. We scan for these on startup and
	// either re-queue the search or cancel + notify so customers aren't left
	// staring at a spinner forever.
	go recoverOrphanedRides(bgCtx, db, rdb, engine, hub, log.With().Str("component", "recovery").Logger())

	// Daily driver document-expiry notifications (license/insurance/authorization)
	// — push + in-app, at the 30/14/7/3/1/0/-1 day marks.
	driverSvc.SetExpiryNotifier(notifySvc)
	go driverSvc.RunDocumentExpiryNotifier(bgCtx)

	// Pre-warm landmark route cache
	locSvc.WarmLandmarkRoutes(bgCtx)

	// Pre-warm dashboard cache and start background refresh
	dashSvc.WarmCache(bgCtx)
	go dashSvc.PollLoop(bgCtx)

	// ── Dead-man finalizer ────────────────────────────────────────────────────
	// Auto-complete rides stuck IN_PROGRESS past RIDE_MAX_IN_PROGRESS_MINUTES so
	// a driver who started a trip then went offline doesn't leave a ghost ride
	// (and isn't locked ON_TRIP forever). Runs every 5 minutes.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-bgCtx.Done():
				return
			case <-ticker.C:
				if n, err := rideSvc.FinalizeStaleInProgressRides(bgCtx); err != nil {
					log.Error().Err(err).Msg("finalizer: failed to scan stale in-progress rides")
				} else if n > 0 {
					log.Warn().Int("count", n).Msg("finalizer: auto-finalized stale in-progress rides")
				}
			}
		}
	}()

	// Expire lapsed ride credits — sweep the ledger hourly (and once on boot).
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		sweep := func() {
			if n, err := ledgerSvc.SweepExpired(bgCtx); err != nil {
				log.Error().Err(err).Msg("credit-expiry: sweep failed")
			} else if n > 0 {
				log.Info().Int("adjusted", n).Msg("credit-expiry: expired lapsed credits")
			}
		}
		sweep()
		for {
			select {
			case <-bgCtx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()

	// ── MoMo reconciliation ───────────────────────────────────────────────────
	// Live mobile-money settlement is driven by polling the provider, not the
	// inbound webhook (MTN does not sign callbacks the way our guard expects).
	// Every 30s we sweep PENDING MoMo charges: confirm the paid ones, fail the
	// rejected/expired ones. No-op when no live charges are outstanding.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-bgCtx.Done():
				return
			case <-ticker.C:
				if n, err := purchaseSvc.ReconcilePending(bgCtx); err != nil {
					log.Error().Err(err).Msg("momo-reconcile: sweep failed")
				} else if n > 0 {
					log.Info().Int("settled", n).Msg("momo-reconcile: settled pending charges")
				}
			}
		}
	}()

	// ── Router ────────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	// ── CORS ──────────────────────────────────────────────────────────────────
	// Allow the admin Next.js dev server in non-production, and any configured production origin.
	allowedOrigins := []string{}
	if cfg.Env != "production" {
		allowedOrigins = append(allowedOrigins, "http://localhost:3000", "http://localhost:3001")
	}
	if origin := cfg.AdminOrigin; origin != "" {
		allowedOrigins = append(allowedOrigins, origin)
	}
	r.Use(chiCors.Handler(chiCors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(mw.SecurityHeaders(cfg.Env))
	// Global request-body cap (memory-exhaustion guard), except large-upload
	// routes which set their own higher per-handler limit.
	r.Use(mw.SkipPaths(mw.BodyLimit(cfg.Security.MaxRequestBodyBytes), apiV1Prefix+"/uploads/objects/"))
	r.Use(mw.SkipPaths(
		mw.IPRateLimit(cfg, rdb, "global", cfg.Security.GlobalRateLimitPerMin, time.Minute),
		"/health", "/metrics", apiV1Prefix+"/ws/",
	))
	r.Use(mw.MetricsMiddleware)
	// Global per-IP rate limit (application-layer DDoS/abuse backstop). Skip health
	// checks and the long-lived WebSocket upgrades — reconnect storms behind
	// carrier-grade NAT would otherwise drain a bucket shared by many real users.
	r.Use(mw.SkipPaths(
		mw.IPRateLimit(cfg, rdb, "global", cfg.Security.GlobalRateLimitPerMin, time.Minute),
		"/health", apiV1Prefix+"/ws/",
	))
	r.Use(mw.WithLogger(log))
	r.Use(mw.HTTPLogger(log))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		respond.OK(w, map[string]string{"status": "ok"})
	})

	r.Get("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

		total, routes := mw.GlobalMetrics.GetMetrics()
		fmt.Fprintf(w, "# HELP http_requests_total Total number of HTTP requests\n")
		fmt.Fprintf(w, "# TYPE http_requests_total counter\n")
		fmt.Fprintf(w, "http_requests_total %d\n\n", total)

		fmt.Fprintf(w, "# HELP http_request_duration_ms_total Total HTTP request duration in milliseconds\n")
		fmt.Fprintf(w, "# TYPE http_request_duration_ms_total counter\n")
		for route, data := range routes {
			fmt.Fprintf(w, "http_request_duration_ms_total{route=%q} %d\n", route, data.Duration)
			fmt.Fprintf(w, "http_request_count_total{route=%q} %d\n", route, data.Count)
		}
		fmt.Fprintf(w, "\n")

		drivers, customers := hub.ActiveConnectionsCount()
		fmt.Fprintf(w, "# HELP ws_active_connections Active WebSocket connections\n")
		fmt.Fprintf(w, "# TYPE ws_active_connections gauge\n")
		fmt.Fprintf(w, "ws_active_connections{role=\"driver\"} %d\n", drivers)
		fmt.Fprintf(w, "ws_active_connections{role=\"customer\"} %d\n", customers)
		fmt.Fprintf(w, "\n")

		dbStat := db.Stat()
		fmt.Fprintf(w, "# HELP db_pool_max_connections Maximum DB connections allowed in write pool\n")
		fmt.Fprintf(w, "# TYPE db_pool_max_connections gauge\n")
		fmt.Fprintf(w, "db_pool_max_connections %d\n", dbStat.MaxConns())

		fmt.Fprintf(w, "# HELP db_pool_active_connections Current active DB connections in write pool\n")
		fmt.Fprintf(w, "# TYPE db_pool_active_connections gauge\n")
		fmt.Fprintf(w, "db_pool_active_connections %d\n", dbStat.AcquiredConns())

		fmt.Fprintf(w, "# HELP db_pool_idle_connections Current idle DB connections in write pool\n")
		fmt.Fprintf(w, "# TYPE db_pool_idle_connections gauge\n")
		fmt.Fprintf(w, "db_pool_idle_connections %d\n", dbStat.IdleConns())

		fmt.Fprintf(w, "# HELP db_pool_total_connections Total DB connections in write pool\n")
		fmt.Fprintf(w, "# TYPE db_pool_total_connections gauge\n")
		fmt.Fprintf(w, "db_pool_total_connections %d\n", dbStat.TotalConns())
		fmt.Fprintf(w, "\n")

		dbReadStat := dbRead.Stat()
		fmt.Fprintf(w, "# HELP db_replica_pool_max_connections Maximum DB connections allowed in replica pool\n")
		fmt.Fprintf(w, "# TYPE db_replica_pool_max_connections gauge\n")
		fmt.Fprintf(w, "db_replica_pool_max_connections %d\n", dbReadStat.MaxConns())

		fmt.Fprintf(w, "# HELP db_replica_pool_active_connections Current active DB connections in replica pool\n")
		fmt.Fprintf(w, "# TYPE db_replica_pool_active_connections gauge\n")
		fmt.Fprintf(w, "db_replica_pool_active_connections %d\n", dbReadStat.AcquiredConns())

		fmt.Fprintf(w, "# HELP db_replica_pool_idle_connections Current idle DB connections in replica pool\n")
		fmt.Fprintf(w, "# TYPE db_replica_pool_idle_connections gauge\n")
		fmt.Fprintf(w, "db_replica_pool_idle_connections %d\n", dbReadStat.IdleConns())

		fmt.Fprintf(w, "# HELP db_replica_pool_total_connections Total DB connections in replica pool\n")
		fmt.Fprintf(w, "# TYPE db_replica_pool_total_connections gauge\n")
		fmt.Fprintf(w, "db_replica_pool_total_connections %d\n", dbReadStat.TotalConns())
		fmt.Fprintf(w, "\n")

		if client, ok := rdb.(*goredis.Client); ok {
			redisPool := client.PoolStats()
			if redisPool != nil {
				fmt.Fprintf(w, "# HELP redis_pool_hits Total number of times a connection was acquired from the pool\n")
				fmt.Fprintf(w, "# TYPE redis_pool_hits counter\n")
				fmt.Fprintf(w, "redis_pool_hits %d\n", redisPool.Hits)

				fmt.Fprintf(w, "# HELP redis_pool_misses Total number of times a connection could not be acquired from the pool\n")
				fmt.Fprintf(w, "# TYPE redis_pool_misses counter\n")
				fmt.Fprintf(w, "redis_pool_misses %d\n", redisPool.Misses)

				fmt.Fprintf(w, "# HELP redis_pool_timeouts Total number of connection timeouts\n")
				fmt.Fprintf(w, "# TYPE redis_pool_timeouts counter\n")
				fmt.Fprintf(w, "redis_pool_timeouts %d\n", redisPool.Timeouts)

				fmt.Fprintf(w, "# HELP redis_pool_total_connections Total number of connections in the Redis pool\n")
				fmt.Fprintf(w, "# TYPE redis_pool_total_connections gauge\n")
				fmt.Fprintf(w, "redis_pool_total_connections %d\n", redisPool.TotalConns)

				fmt.Fprintf(w, "# HELP redis_pool_idle_connections Number of idle connections in the Redis pool\n")
				fmt.Fprintf(w, "# TYPE redis_pool_idle_connections gauge\n")
				fmt.Fprintf(w, "redis_pool_idle_connections %d\n", redisPool.IdleConns)
			}
		}

		// 5. MTN Reconcile queue depth metric
		if depth, err := purchaseSvc.PendingMoMoQueueDepth(r.Context()); err == nil {
			fmt.Fprintf(w, "# HELP mtn_reconcile_queue_depth Count of pending mobile money purchases awaiting reconciliation\n")
			fmt.Fprintf(w, "# TYPE mtn_reconcile_queue_depth gauge\n")
			fmt.Fprintf(w, "mtn_reconcile_queue_depth %d\n", depth)
		}
	})

	// API docs — gated: 404 when disabled (default in prod), optional Basic auth
	// so the API surface isn't world-readable. Set SWAGGER_ENABLED / SWAGGER_BASIC_AUTH.
	swaggerGate := mw.SwaggerGate(cfg.Security.SwaggerEnabled, cfg.Security.SwaggerBasicAuth)
	// Swagger UI needs to load its CSS/JS (from unpkg) and run scripts, which the
	// API-wide strict CSP (default-src 'none'; sandbox) forbids. Override the CSP
	// for just this page so the docs render, while every other route stays locked.
	swaggerCSP := "default-src 'self'; script-src 'self' 'unsafe-inline' https://unpkg.com; " +
		"style-src 'self' 'unsafe-inline' https://unpkg.com; img-src 'self' data: https:; " +
		"font-src 'self' data: https://unpkg.com; connect-src 'self'"
	r.With(swaggerGate).Get("/swagger", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", swaggerCSP)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(swaggerHTML))
	})
	r.With(swaggerGate).Get("/swagger/openapi.json", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "config/openapi.json")
	})
	r.Get(apiV1Prefix+"/pricing", fareH.ListPublicPricing)

	// ── Public contact form ───────────────────────────────────────────────────
	r.Post(apiV1Prefix+"/contact", inboxH.Submit)
	r.Get(apiV1Prefix+"/media/documents/{filename}", adminH.ServeDriverMedia)

	// ── Public auth ───────────────────────────────────────────────────────────
	r.Route(apiV1Prefix+"/auth", func(r chi.Router) {
		r.With(mw.OTPRateLimit(rdb, "otp_send", 5, time.Hour)).Post("/register", authH.Register)
		// verify-otp is brute-forceable (6-digit code) — cap attempts per phone too.
		r.With(mw.OTPRateLimit(rdb, "otp_verify", 10, 15*time.Minute)).Post("/verify-otp", authH.VerifyOTP)
		r.With(mw.IPRateLimit(cfg, rdb, "auth_refresh", cfg.Security.AuthRefreshRateLimit, 15*time.Minute)).Post("/refresh", authH.Refresh)
		r.With(mw.Authenticate(cfg, rdb)).Post("/logout", authH.Logout)
		r.With(mw.Authenticate(cfg, rdb)).Post("/ws-ticket", authH.WSTicket)
		r.With(mw.Authenticate(cfg, rdb), mw.OTPRateLimit(rdb, "delete_account", 3, 24*time.Hour)).Delete("/account", authH.DeleteAccount)
	})

	// MoMo payment callback — public (called by the provider, not the app), so it
	// is gated by a shared secret instead of a user session. Without this any
	// caller could POST a fake "SUCCESS" and have credits granted for free.
	webhookSecretRequired := cfg.Payments.Enabled || cfg.MoMo.APIKey != "" || cfg.MoMo.SubscriptionKey != "" || cfg.MoMo.APIUser != ""
	if cfg.Env == "production" && cfg.Payments.WebhookSecret == "" {
		log.Warn().Msg("MOMO_WEBHOOK_SECRET is unset in production — the MoMo callback is UNAUTHENTICATED; set it before enabling payments")
	}
	r.With(
		mw.IPRateLimit(cfg, rdb, "momo_webhook", cfg.Security.MomoWebhookRateLimit, time.Minute),
		momoWebhookAuth(cfg.Payments.WebhookSecret, webhookSecretRequired),
	).Post(apiV1Prefix+"/webhooks/momo/callback", pkgH.WebhookMoMo)

	// ── Customer ──────────────────────────────────────────────────────────────
	r.Route(apiV1Prefix+"/customer", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))
		// Per-user backstop (keyed on JWT user_id, NOT IP): immune to carrier NAT.
		// Generous — hot endpoints keep their own tighter per-user limits below this.
		r.Use(mw.UserRateLimit429(rdb, "customer_group", 600, time.Minute))
		r.Use(mw.RequireNotSuspended())
		r.Use(mw.RequireRole(mw.RoleCustomer, mw.RoleDriverActive, mw.RoleDriverPending))

		r.Get("/profile", custH.GetProfile)
		r.Put("/profile", custH.UpdateProfile)
		r.Get("/level", custH.GetLevel) // loyalty / gamification
		// Phone-number change (OTP-verified). Tight per-user limit on the request
		// leg so it can't be abused to spam SMS to arbitrary numbers.
		r.With(mw.UserRateLimit(rdb, "phone_change_req", 5, 10*time.Minute)).
			Post("/phone/change/request", authH.RequestPhoneChange)
		r.Post("/phone/change/verify", authH.VerifyPhoneChange)
		r.Post("/location", driver.NearbyDriversHandler(driverSvc))
		r.Get("/fare-estimate", fareH.FareEstimate)

		r.Post("/rides", rideH.CreateRide)
		r.Get("/rides", rideH.ListRides)
		r.Get("/rides/active", rideH.GetActiveRideForCustomer)
		r.Get("/rides/{ride_id}", rideH.GetRide)
		r.Delete("/rides/{ride_id}", rideH.CancelRide)

		r.Post("/rides/{ride_id}/negotiation/propose", negH.Propose("CUSTOMER"))
		r.Post("/rides/{ride_id}/negotiation/accept", negH.Accept("CUSTOMER"))
		r.Post("/rides/{ride_id}/negotiation/decline", negH.Decline("CUSTOMER"))
		r.Post("/rides/{ride_id}/negotiation/message", negH.SendMessage("CUSTOMER"))
		r.Get("/rides/{ride_id}/negotiation/history", negH.GetHistory("CUSTOMER"))

		r.Post("/rides/{ride_id}/rate", ratingH.SubmitRating)
		r.Get("/rides/{ride_id}/rating", ratingH.GetRideRating)

		r.Post("/support/tickets", ticketH.SubmitTicket)
	})

	// ── Driver ────────────────────────────────────────────────────────────────
	r.Route(apiV1Prefix+"/driver", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))
		// Per-user backstop (keyed on JWT user_id, NOT IP): immune to carrier NAT.
		// Generous — hot endpoints (e.g. location) keep their own tighter limits.
		r.Use(mw.UserRateLimit429(rdb, "driver_group", 600, time.Minute))
		r.Use(mw.RequireNotSuspended())

		r.Post("/apply", driverH.Apply)

		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(mw.RoleDriverActive, mw.RoleDriverPending))
			r.Get("/profile", driverH.GetProfile)
			r.Put("/profile", driverH.UpdateProfile)
			r.Post("/policy/accept", driverH.AcceptPolicy)
			// One-call bootstrap: profile + active vehicle + ride flag + doc alerts.
			r.Get("/session", driverH.GetSession)

			// Multi-vehicle management + switching. Activation enforces the
			// business rules (approved driver, no active ride) in the service.
			r.Get("/vehicles", driverH.ListVehicles)
			r.Post("/vehicles", driverH.CreateVehicle)
			r.Patch("/vehicles/{id}", driverH.UpdateVehicle)
			r.Delete("/vehicles/{id}", driverH.DeleteVehicle)
			r.Post("/vehicles/{id}/activate", driverH.ActivateVehicle)
		})

		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(mw.RoleDriverActive, mw.RoleDriverPending, mw.RoleCustomer))
			r.Post("/documents", driverH.UploadDocument)
			r.Get("/documents", driverH.ListDocuments)
		})

		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(mw.RoleDriverActive))

			r.Post("/availability", driverH.SetAvailability)
			// 20 req/min = 1 every 3 s. Drivers send every 5–12 s so this is
			// headroom for bursts without blocking normal use. Returns 204 (not
			// 429) so the app doesn't log a red error when a burst is trimmed.
			r.With(mw.UserRateLimit(rdb, "driver_location", cfg.Security.DriverLocationRateLimit, time.Minute)).
				Post("/location", driverH.UpdateLocation)
			r.With(mw.UserRateLimit(rdb, "driver_location_batch", cfg.Security.DriverLocationRateLimit, time.Minute)).
				Post("/locations", driverH.UpdateLocationsBatch)
			r.With(mw.UserRateLimit(rdb, "driver_location", 20, time.Minute)).
				Post("/location", driverH.UpdateLocation)

			// Demand heatmap — where riders are requesting, so a driver can
			// reposition. Read-only; per-user limit keeps polling reasonable.
			r.With(mw.UserRateLimit(rdb, "driver_demand_heatmap", 30, time.Minute)).
				Get("/demand-heatmap", driverH.DemandHeatmap)

			r.Get("/packages", pkgH.ListPackages)
			r.Get("/campaigns/active", pkgH.ListActiveCampaigns)
			// Cap purchase attempts per driver so a loop can't spam MoMo prompts
			// (each one pushes a PIN request to the payer's phone).
			r.With(mw.UserRateLimit(rdb, "pkg_purchase", 10, time.Minute)).
				Post("/packages/purchase", pkgH.PurchasePackage)
			r.Get("/packages/purchases/{purchaseID}", pkgH.GetPurchaseStatus)
			// Manual-payment flow: where to pay, and submit proof for admin review.
			r.Get("/packages/payment-info", pkgH.ManualPaymentInfo)
			r.With(mw.UserRateLimit(rdb, "pkg_proof", 12, time.Minute)).
				Post("/packages/purchases/{purchaseID}/proof", pkgH.SubmitPaymentProof)
			r.Get("/packages/history", pkgH.PurchaseHistory)
			r.Get("/credits", pkgH.GetCredits)
			r.Get("/entitlements", pkgH.GetEntitlements)
			r.Get("/bonuses", bonusH.DriverGrants)
			r.Get("/bonuses/tiers", bonusH.ListActiveTiers)

			r.Get("/rides/active", rideH.GetActiveRideForDriver)
			r.Post("/rides/{ride_id}/accept", driverAcceptHandler(engine, rideRepo, driverSvc, ledgerSvc, cfg))
			r.Post("/rides/{ride_id}/decline", driverDeclineHandler(engine, driverSvc))
			r.Get("/rides/{ride_id}", rideH.GetRideForDriver)
			r.Post("/rides/{ride_id}/cancel", driverCancelHandler(rideSvc))
			r.Post("/rides/{ride_id}/en-route", driverEnRouteHandler(rideSvc))
			r.Post("/rides/{ride_id}/arrive", driverArriveHandler(rideSvc))
			r.Post("/rides/{ride_id}/start", driverStartHandler(rideSvc))
			r.Post("/rides/{ride_id}/complete", driverCompleteHandler(rideSvc))

			r.Post("/rides/{ride_id}/negotiation/propose", negH.Propose("DRIVER"))
			r.Post("/rides/{ride_id}/negotiation/accept", negH.Accept("DRIVER"))
			r.Post("/rides/{ride_id}/negotiation/decline", negH.Decline("DRIVER"))
			r.Post("/rides/{ride_id}/negotiation/message", negH.SendMessage("DRIVER"))
			r.Get("/rides/{ride_id}/negotiation/history", negH.GetHistory("DRIVER"))
			r.Post("/rides/{ride_id}/negotiation/lock-fare", negH.LockManualFare)
			r.Post("/rides/{ride_id}/negotiation/initiate-call", negH.InitiateCall)

			r.Post("/rides/{ride_id}/rate", ratingH.SubmitRating)
			r.Get("/rides/{ride_id}/rating", ratingH.GetRideRating)

			r.Get("/earnings/daily", driverH.DailyEarnings)
			r.Get("/earnings/weekly", driverH.WeeklyEarnings)
			r.Get("/stats", driverH.Stats)
		})
	})

	// ── Users (mode switch, saved locations, notifications) ──────────────────
	r.Route(apiV1Prefix+"/users", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))

		r.Patch("/mode", locH.SwitchMode)

		r.Get("/me/ratings", ratingH.MyRatings)

		r.Get("/me/saved-locations", locH.ListSavedLocations)
		r.Post("/me/saved-locations", locH.CreateSavedLocation)
		r.Put("/me/saved-locations/{id}", locH.UpdateSavedLocation)
		r.Delete("/me/saved-locations/{id}", locH.DeleteSavedLocation)

		r.Get("/me/notifications", notifH.List)
		r.Get("/me/notifications/unread-count", notifH.UnreadCount)
		r.Patch("/me/notifications/{id}/read", notifH.MarkRead)
		r.Post("/me/notifications/mark-all-read", notifH.MarkAllRead)
	})

	// ── Wallet (customer + driver — same wallet per user_id) ─────────────────
	r.Route(apiV1Prefix+"/wallet", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))

		r.Get("/", walletH.GetWallet)
		r.Get("/transactions", walletH.GetTransactions)
		r.Post("/top-up", walletH.TopUp)
		r.Post("/withdraw", walletH.Withdraw)
	})

	r.Route(apiV1Prefix+"/uploads", func(r chi.Router) {
		// Public object serving (proxy/MinIO mode) so the mobile app and admin
		// panel can render document images via a plain URL. No-op on S3/R2.
		if uploadH != nil {
			r.Get("/objects/*", uploadH.GetObject)
			// Proxy-mode PUT mirrors a presigned S3 URL — the random object key is
			// the credential, so mobile can stream bytes without a bearer token.
			r.Put("/objects/*", uploadH.PutObject)
		}
		// Authenticated: request an upload target.
		r.Group(func(r chi.Router) {
			r.Use(mw.Authenticate(cfg, rdb))
			if uploadH != nil {
				r.Post("/presigned-url", uploadH.PresignedURL)
			}
		})
	})

	// ── Locations (route cache, landmarks, suggestions) ───────────────────────
	r.Route(apiV1Prefix+"/locations", func(r chi.Router) {
		r.Get("/landmarks", locH.GetLandmarks) // public — no auth needed

		r.Group(func(r chi.Router) {
			r.Use(mw.Authenticate(cfg, rdb))
			r.Get("/suggestions", locH.GetSuggestions)
			r.Get("/route", locH.GetRoute)
			r.Post("/route", locH.UpsertRoute)
		})
	})

	// ── Active ride (reconnect recovery) ─────────────────────────────────────
	r.With(mw.Authenticate(cfg, rdb)).Get(apiV1Prefix+"/rides/active", locH.GetActiveRide)

	// ── Admin auth (public — no admin JWT required) ───────────────────────────
	// Rate limit administrative endpoints to prevent brute-force credential stuffing.
	r.With(mw.IPRateLimit(cfg, rdb, "admin_login", cfg.Security.AdminLoginRateLimit, 5*time.Minute)).Post(apiV1Prefix+"/admin/auth/login", teamH.Login)
	r.With(mw.IPRateLimit(cfg, rdb, "admin_2fa", cfg.Security.AdminLoginRateLimit, 5*time.Minute)).Post(apiV1Prefix+"/admin/auth/2fa/verify", teamH.Verify2FA)
	r.With(mw.IPRateLimit(cfg, rdb, "admin_2fa_backup", cfg.Security.AdminLoginRateLimit, 5*time.Minute)).Post(apiV1Prefix+"/admin/auth/2fa/backup", teamH.VerifyBackupCode)
	r.With(mw.IPRateLimit(cfg, rdb, "admin_totp_reset", cfg.Security.AdminLoginRateLimit, 5*time.Minute)).Post(apiV1Prefix+"/admin/auth/totp/reset-login", teamH.ResetTOTPLogin)
	r.With(mw.IPRateLimit(cfg, rdb, "admin_login", 5, 5*time.Minute)).Post(apiV1Prefix+"/admin/auth/login", teamH.Login)
	r.With(mw.IPRateLimit(cfg, rdb, "admin_2fa", 5, 5*time.Minute)).Post(apiV1Prefix+"/admin/auth/2fa/verify", teamH.Verify2FA)
	r.With(mw.IPRateLimit(cfg, rdb, "admin_2fa_backup", 5, 5*time.Minute)).Post(apiV1Prefix+"/admin/auth/2fa/backup", teamH.VerifyBackupCode)
	r.With(mw.IPRateLimit(cfg, rdb, "admin_totp_reset", 5, 5*time.Minute)).Post(apiV1Prefix+"/admin/auth/totp/reset-login", teamH.ResetTOTPLogin)

	// ── Admin (protected) ─────────────────────────────────────────────────────
	r.Route(apiV1Prefix+"/admin", func(r chi.Router) {
		r.Use(mw.AuthenticateAdmin(cfg, rdb))
		r.Use(mw.RequireRole(mw.RoleAdmin))

		// Auth (protected actions) - My Account (unrestricted admin roles)
		r.Post("/auth/logout", teamH.Logout)
		r.Post("/auth/2fa/reissue", teamH.Reissue2FAChallenge)
		r.Post("/auth/totp/reset", teamH.ResetTOTP)

		// Account (self) - My Account (unrestricted admin roles)
		r.Get("/account", teamH.GetAccount)
		r.Get("/account/me", teamH.GetAccount)
		r.Put("/account", teamH.UpdateAccount)
		r.Patch("/account/me", teamH.UpdateAccount)
		r.Post("/account/password", teamH.ChangePassword)
		r.Post("/account/change-password", teamH.ChangePassword)
		r.Get("/account/sessions", teamH.GetSessions)
		r.Delete("/account/sessions/{sessionId}", teamH.RevokeSession)
		r.Get("/account/2fa/setup", teamH.Setup2FA)
		r.Post("/account/2fa/enable", teamH.Enable2FA)
		r.Post("/account/2fa/disable", teamH.Disable2FA)

		// --- Operations Bucket (Super Admin, Operations Manager, Support Staff) ---
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireAdminRole(adminrole.SuperAdmin, adminrole.OpsManager, adminrole.SupportStaff))

			// Dashboard
			r.Get("/dashboard", dashH.Get)
			r.Get("/dashboard/revenue-series", dashH.RevenueSeries)
			r.Get("/dashboard/rides-series", dashH.RidesSeries)
			r.Get("/dashboard/driver-status", dashH.DriverStatusSnapshot)
			r.Get("/dashboard/top-drivers", dashH.TopDrivers)
			r.Get("/dashboard/recent-activity", dashH.RecentActivity)
			r.Get("/dashboard/alerts", dashH.Alerts)
			r.Get("/dashboard/live-map", dashH.LiveMap)
			r.Get("/launch-readiness", adminH.LaunchReadiness)

			// Drivers
			r.Post("/drivers/send-otp", adminH.SendDriverOTP)
			r.Post("/drivers/verify-otp", adminH.VerifyDriverOTP)
			r.Get("/drivers", adminH.ListDrivers)
			r.Post("/drivers", adminH.CreateDriver)
			r.Get("/drivers/overview", adminH.DriverOverview)
			r.Get("/drivers/{id}", adminH.GetDriver)
			r.Get("/drivers/{id}/referrals", adminH.GetDriverReferrals)
			r.Post("/drivers/{id}/force-offline", adminH.ForceDriverOffline)
			r.Patch("/drivers/{id}", adminH.UpdateDriver)
			// r.Delete("/drivers/{id}", adminH.DeleteDriver) REMOVED - suspend/reinstate only
			r.Post("/drivers/{id}/approve", adminH.ApproveDriver)
			r.Post("/drivers/{id}/reject", adminH.RejectDriver)
			r.Post("/drivers/{id}/request-more-info", adminH.RequestDriverMoreInfo)
			r.Post("/drivers/{id}/suspend", adminH.SuspendDriver)
			r.Post("/drivers/{id}/reinstate", adminH.ReinstateDriver)
			r.Patch("/drivers/{id}/verify", adminH.VerifyDriver)
			r.Patch("/drivers/{id}/status", adminH.UpdateDriverStatus)
			r.Post("/drivers/{id}/documents", adminH.UploadDriverDocument)
			r.Post("/uploads/file", adminH.UploadDriverFile)
			if uploadH != nil {
				r.Post("/uploads/presigned-url", uploadH.PresignedURL)
			}

			// Customers
			r.Get("/customers", adminH.ListCustomers)
			r.Get("/customers/overview", adminH.CustomerOverview)
			r.Get("/customers/{id}", adminH.GetCustomer)
			r.Patch("/customers/{id}", adminH.UpdateCustomer)
			r.Patch("/customers/{id}/ban", adminH.BanCustomer)
			r.Post("/customers/{id}/suspend", adminH.SuspendUser)
			r.Post("/customers/{id}/reinstate", adminH.ReinstateUser)

			// Users (backwards compat)
			r.Get("/users", adminH.ListUsers)
			r.Post("/users/{id}/suspend", adminH.SuspendUser)

			// Safety flags
			r.Get("/flags/gps-anomalies", adminH.GPSAnomalies)
			r.Get("/flags/device-collisions", adminH.DeviceCollisions)

			// Live rides
			r.Get("/rides/live/stats", adminH.LiveRidesStats)
			r.Get("/rides/live", adminH.ListLiveRides)
			r.Get("/rides/live/{id}", adminH.GetLiveRide)
			r.Post("/rides/live/{id}/intervene", adminH.InterveneRide)

			// All rides (history)
			r.Get("/rides", adminH.ListRides)
			r.Get("/rides/{id}", adminH.GetRide)

			// Negotiations
			r.Get("/negotiations/stats", adminH.NegotiationsStats)
			r.Get("/negotiations", adminH.ListNegotiations)
			r.Get("/negotiations/{id}", adminH.GetNegotiation)

			// Safety incidents
			r.Get("/incidents/stats", incidentH.Stats)
			r.Get("/incidents", incidentH.List)
			r.Post("/incidents", incidentH.Create)
			r.Get("/incidents/{id}", incidentH.Get)
			r.Patch("/incidents/{id}/status", incidentH.UpdateStatus)
			r.Post("/incidents/{id}/acknowledge", incidentH.Acknowledge)
			r.Post("/incidents/{id}/escalate", incidentH.Escalate)
			r.Post("/incidents/{id}/resolve", incidentH.Resolve)
			r.Post("/incidents/{id}/message", incidentH.Message)

			// Support tickets
			r.Get("/support/tickets/stats", ticketH.Stats)
			r.Get("/support/tickets", ticketH.List)
			r.Post("/support/tickets", ticketH.Create)
			r.Get("/support/tickets/{id}", ticketH.Get)
			r.Post("/support/tickets/{id}/reply", ticketH.Reply)
			r.Post("/support/tickets/{id}/assign", ticketH.Assign)
			r.Post("/support/tickets/{id}/resolve", ticketH.Resolve)
			r.Patch("/support/tickets/{id}", ticketH.Patch)
			// Keep old paths for compatibility
			r.Get("/tickets", ticketH.List)
			r.Post("/tickets", ticketH.Create)
			r.Get("/tickets/{id}", ticketH.Get)
			r.Post("/tickets/{id}/reply", ticketH.Reply)
			r.Post("/tickets/{id}/assign", ticketH.Assign)
			r.Post("/tickets/{id}/resolve", ticketH.Resolve)

			// Inbox
			r.Get("/inbox/stats", inboxH.Stats)
			r.Get("/inbox", inboxH.List)
			r.Get("/inbox/{id}", inboxH.Get)
			r.Post("/inbox/{id}/reply", inboxH.Reply)
			r.Patch("/inbox/{id}", inboxH.UpdateStatus)
			r.Delete("/inbox/{id}", inboxH.Delete)
			r.Post("/inbox/{id}/archive", inboxH.Archive)
			r.Post("/inbox/{id}/spam", inboxH.Spam)

			// Account Assist tools (clear OTP lockouts, clear GPS flags, etc.)
			r.Post("/customers/{id}/clear-otp-lockout", adminH.ClearOTPLockout)
			r.Post("/drivers/{id}/clear-gps-flags", adminH.ClearGPSFlags)
			r.Post("/users/{id}/clear-device-collision", adminH.ClearDeviceCollisionFlag)
			r.Get("/users/{id}/timeline", adminH.GetAccountTimeline)
		})

		// --- Finance Bucket (Super Admin, Finance Manager, Analytics Staff) ---
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireAdminRole(adminrole.SuperAdmin, adminrole.FinanceManager, adminrole.AnalyticsStaff))

			// Revenue (read)
			r.Get("/revenue", adminH.Revenue)
			r.Get("/revenue/kpis", adminH.RevenueKPIs)
			r.Get("/revenue/transactions", adminH.ListTransactions)

			// Analytics
			r.Get("/analytics/overview", anaH.Overview)
			r.Get("/analytics/rides/daily", anaH.DailyRides)
			r.Get("/analytics/rides/weekly", anaH.WeeklyRides)
			r.Get("/analytics/revenue/breakdown", anaH.RevenueBreakdown)
			r.Get("/analytics/drivers/performance", anaH.DriverPerformance)
			r.Get("/analytics/negotiation/stats", anaH.NegotiationStats)
			r.Get("/analytics/heatmap", anaH.Heatmap)
			r.Get("/analytics/heatmap/zones", anaH.HeatmapZones)
			r.Get("/analytics/cancellations", anaH.Cancellations)
			r.Get("/analytics/funnel", anaH.Funnel)
			r.Get("/analytics/vehicle-mix", anaH.VehicleMix)
			r.Get("/analytics/activity-heatmap", anaH.ActivityHeatmap)
			r.Get("/analytics/satisfaction", anaH.Satisfaction)

			// Reports
			r.Get("/reports/stats", reportH.Stats)
			r.Get("/reports", reportH.List)
			r.Post("/reports", reportH.Generate)
			r.Post("/reports/generate", reportH.Generate)
			r.Get("/reports/scheduled", reportH.ListScheduled)
			r.Post("/reports/scheduled", reportH.CreateScheduled)
			r.Post("/reports/scheduled/{id}/toggle", reportH.ToggleScheduled)
			r.Get("/reports/{id}", reportH.Get)
			r.Get("/reports/{id}/download", reportH.Download)
			r.Delete("/reports/{id}", reportH.Delete)

			// Finance Reporting & Ledger
			r.Get("/finance/ledger", financeH.GetGeneralLedger)
			r.Get("/finance/trial-balance", financeH.GetTrialBalance)
			r.Get("/finance/balance-sheet", financeH.GetBalanceSheet)
			r.Get("/finance/export", financeH.ExportFinanceReport)
			r.Get("/finance/staff-analytics", financeH.GetStaffAnalytics)
		})

		// --- Finance Write Bucket (Super Admin, Finance Manager) ---
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireAdminRole(adminrole.SuperAdmin, adminrole.FinanceManager))

			r.Post("/revenue/payouts/disburse", adminH.DisbursePayouts)
		})

		// --- Super Admin Only ---
		r.Group(func(r chi.Router) {
			r.Use(mw.RequireAdminRole(adminrole.SuperAdmin))

			// Pricing
			r.Get("/pricing", fareH.ListActivePricing)
			r.Get("/pricing/{vehicle_type_code}", fareH.GetActivePricingByType)
			r.Get("/pricing/{vehicle_type_code}/history", fareH.GetPricingHistory)
			r.Post("/pricing/{vehicle_type_code}", fareH.CreatePricing)

			// Settings
			r.Get("/settings", settingsH.GetAll)
			r.Put("/settings/commission", settingsH.UpdateCommission)
			r.Put("/settings/negotiation", settingsH.UpdateNegotiation)
			r.Put("/settings/fares", settingsH.UpdateFares)
			r.Put("/settings/integrations", settingsH.UpdateIntegrations)
			r.Put("/settings/notifications", settingsH.UpdateNotifications)
			r.Post("/settings/regions", settingsH.CreateRegion)
			r.Put("/settings/regions/{id}", settingsH.UpdateRegion)
			r.Patch("/settings/regions/{id}", settingsH.UpdateRegion)
			r.Delete("/settings/regions/{id}", settingsH.DeleteRegion)

			// Team / admin accounts
			r.Get("/team", teamH.List)
			r.Post("/team/invite", teamH.Invite)
			r.Get("/team/roles", teamH.ListRoles)
			r.Post("/team/roles", teamH.CreateRole)
			r.Patch("/team/roles/{roleId}", teamH.UpdateRoleByID)
			r.Patch("/team/roles/{roleId}/permissions", teamH.UpdateRolePermissions)
			r.Delete("/team/roles/{roleId}", teamH.DeleteRoleByID)
			r.Post("/team/members/{id}/role", teamH.UpdateRole)
			r.Post("/team/members/{id}/suspend", teamH.Suspend)
			r.Post("/team/members/{id}/reinstate", teamH.Reinstate)
			r.Post("/team/members/{id}/remove", teamH.Remove)
			r.Post("/team/members/{id}/resend-invite", teamH.ResendInvite)
			r.Post("/team/members/{id}/reset-2fa", teamH.ResetMember2FA)
			r.Get("/team/members/{id}/activity", teamH.GetMemberActivity)
			r.Post("/team/members/{id}/set-password", teamH.SetPassword)
			r.Post("/team/members/{id}/welcome-email", teamH.SendWelcomeEmail)
			r.Post("/team/roles/{roleId}/permissions", teamH.UpdateRolePermissions)
			// r.Post("/team/members/{id}/remove", teamH.Remove) REMOVED - suspend/reinstate only
			r.Post("/team/members/{id}/set-password", teamH.SetPassword)
			r.Get("/team/members/{id}/activity", teamH.MemberActivity)
			r.Post("/team/members/{id}/resend-invite", teamH.ResendInvite)
			r.Post("/team/members/{id}/reset-2fa", teamH.ResetMember2FA)

			// Audit Log
			r.Get("/audit", teamH.ListAuditLog)

			// Packages admin CRUD
			r.Get("/packages", pkgH.AdminListPackages)
			r.Get("/packages-purchases", pkgH.AdminListPurchases)
			r.Get("/packages/{id}/subscribers", pkgH.AdminListPackageSubscribers)
			// Entitlements — admin-wide balances + manual grant
			r.Get("/entitlements", pkgH.AdminListEntitlements)
			r.Post("/entitlements/grant", pkgH.AdminGrantEntitlement)
			r.Post("/packages", pkgH.AdminCreatePackage)
			r.Patch("/packages/{id}", pkgH.AdminUpdatePackage)
			r.Post("/packages/{id}/toggle", pkgH.AdminTogglePackage)
			r.Delete("/packages/{id}", pkgH.AdminDeletePackage)

			// Money actions on purchases — restricted to finance roles, rate-limited
			// per admin (a compromised admin token can't mass-grant credits), and
			// audit-logged in the handlers. Manual settlement of a PENDING purchase,
			// and admin-recorded purchases on a driver's behalf (cash / bank / MoMo).
			r.With(
				mw.RequireAdminRole(adminrole.SuperAdmin, adminrole.FinanceManager),
				mw.UserRateLimit(rdb, "admin_pkg_money", 30, time.Minute),
			).Post("/packages-purchases", pkgH.AdminCreatePurchase)
			r.With(
				mw.RequireAdminRole(adminrole.SuperAdmin, adminrole.FinanceManager),
				mw.UserRateLimit(rdb, "admin_pkg_money", 30, time.Minute),
			).Post("/packages-purchases/{id}/confirm", pkgH.AdminConfirmPurchase)

			// Campaigns admin CRUD
			r.Get("/campaigns", pkgH.AdminListCampaigns)
			r.Post("/campaigns", pkgH.AdminCreateCampaign)
			r.Patch("/campaigns/{id}", pkgH.AdminUpdateCampaign)
			r.Delete("/campaigns/{id}", pkgH.AdminDeleteCampaign)

			// Entitlements admin (ledger-backed)
			r.Get("/entitlements", pkgH.AdminListEntitlements)
			r.With(
				mw.RequireAdminRole(adminrole.SuperAdmin, adminrole.FinanceManager),
				mw.UserRateLimit(rdb, "admin_entitlement_grant", 30, time.Minute),
			).Post("/entitlements/grant", pkgH.AdminGrantEntitlement)
			r.With(
				mw.RequireAdminRole(adminrole.SuperAdmin, adminrole.FinanceManager),
				mw.UserRateLimit(rdb, "admin_entitlement_revoke", 30, time.Minute),
			).Post("/entitlements/revoke", pkgH.AdminRevokeEntitlement)

			// Bonuses — admin CRUD for bonus tiers
			r.Get("/bonuses/tiers", bonusH.AdminListTiers)
			r.Post("/bonuses/tiers", bonusH.AdminCreateTier)
			r.Delete("/bonuses/tiers/{tierID}", bonusH.AdminDeactivateTier)
			r.Put("/bonuses/tiers/{tierID}/activate", bonusH.AdminActivateTier)
		})
	})

	// ── WebSocket ─────────────────────────────────────────────────────────────
	// Mobile uses EXPO_PUBLIC_WS_BASE_URL = ws://host/api/v1, so paths must be
	// /api/v1/ws/driver and /api/v1/ws/customer.
	r.Route(apiV1Prefix+"/ws", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))
		r.Get("/driver", trackH.DriverWS)
		r.Get("/customer", trackH.CustomerWS)
	})

	// Wire matching engine into ride service
	rideSvc.SetMatchingEngine(engine)
	rideSvc.SetRouteFareRecorder(locSvc)
	rideSvc.SetPackagesService(ledgerSvc)  // v4: charge/refund via the ledger
	adminSvc.SetPackagesService(ledgerSvc) // v4: free trial grant via the ledger
	adminSvc.SetBonusService(bonusSvc)

	// ── HTTP server ───────────────────────────────────────────────────────────
	// WriteTimeout must be 0 when serving WebSockets — a global write timeout
	// closes long-lived /ws/driver and /ws/customer connections mid-ride.
	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Info().Str("addr", srv.Addr).Msg("http: listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("http: server error")
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down gracefully")
	bgCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("http: forced shutdown")
	}
	log.Info().Msg("bye")
}

// ── Migration runner ──────────────────────────────────────────────────────

// momoGateway adapts *payment.Service to the packages.MoMoGateway interface so
// the purchase flow can request a mobile-money charge without importing payment
// types. Credentials come from the env (MOMO_*), so it works once those are set.
type momoGateway struct{ p *payment.Service }

func (g momoGateway) RequestPayment(ctx context.Context, provider, phone string, amountRWF int, externalRef string) (string, string, error) {
	res, err := g.p.RequestPayment(ctx, payment.Provider(provider), phone, phone, float64(amountRWF), externalRef)
	if err != nil {
		return "", "", err
	}
	return res.TransactionID, res.Status, nil
}

func (g momoGateway) QueryStatus(ctx context.Context, provider, externalRef string) (string, error) {
	return g.p.QueryStatus(ctx, payment.Provider(provider), externalRef)
}

// momoWebhookAuth gates the public MoMo callback with a shared secret. When the
// secret is empty (dev), it is a pass-through (unless required). When set, callers
// must present a matching X-Webhook-Secret header (constant-time compared) or get 401.
// MTN reconcile polling is the primary authoritative settlement path; this webhook is secondary.
func momoWebhookAuth(secret string, required bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if required && secret == "" {
				respond.ErrorMsg(w, http.StatusServiceUnavailable, "WEBHOOK_MISCONFIGURED", "momo webhook secret is required but not configured")
				return
			}
			if secret != "" {
				got := r.Header.Get("X-Webhook-Secret")
				if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func runMigrations(databaseURL string) error {
	migrateURL := strings.NewReplacer(
		"postgresql://", "pgx5://",
		"postgres://", "pgx5://",
	).Replace(databaseURL)

	m, err := migrate.New("file://migrations", migrateURL)
	if err != nil {
		return fmt.Errorf("migrate.New: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("migrate.Up: %w", err)
	}
	return nil
}

// ── Inline driver ride-action handlers ───────────────────────────────────

// creditChecker is the subset of packages.Service the accept handler needs.
type creditChecker interface {
	HasCredits(ctx context.Context, driverUserID, vehicleType string) (bool, error)
	GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error
}

func driverAcceptHandler(engine *matching.Engine, rideRepo *ride.Repository, driverSvc *driver.Service, credits creditChecker, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := mw.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")

		pendingDriverID, valid := engine.ValidateAcceptTTL(r.Context(), rideID)
		if !valid {
			respond.Error(w, apperrors.ErrAcceptExpired)
			return
		}

		// Identity check: this ride must currently be offered to THIS driver.
		// Without it, any authenticated driver who learns a ride_id with a live
		// offer could accept it (and be assigned the ride). We need the profile
		// anyway for the credit gate, so a load failure is a hard reject here.
		profile, err := driverSvc.GetProfile(r.Context(), claims.UserID)
		if err != nil {
			respond.Error(w, apperrors.ErrAcceptExpired)
			return
		}
		if profile.ID != pendingDriverID {
			respond.Error(w, apperrors.New(http.StatusForbidden, "NOT_YOUR_OFFER", "this ride is not currently offered to you"))
			return
		}

		// Gate: driver must have credits for their vehicle type to accept rides.
		hasCredits, credErr := credits.HasCredits(r.Context(), claims.UserID, profile.TransportType)
		if credErr == nil && !hasCredits && cfg.Env != "production" {
			// Dev/local: auto-grant free trial (same as admin approval flow).
			_ = credits.GrantFreeTrialIfEligible(r.Context(), claims.UserID, profile.TransportType)
			hasCredits, _ = credits.HasCredits(r.Context(), claims.UserID, profile.TransportType)
		}
		if credErr == nil && !hasCredits {
			respond.Error(w, apperrors.New(http.StatusPaymentRequired, "NO_CREDITS", "Buy a package to keep riding."))
			return
		}

		// Signal the matching loop with our identity; it only honors the accept
		// if we are the candidate it is currently offering (closes the TOCTOU
		// where the offer rotates between the check above and this signal).
		engine.AcceptRide(rideID, profile.ID)
		respond.NoContent(w)
	}
}

func driverDeclineHandler(engine *matching.Engine, driverSvc *driver.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := mw.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")
		// Only signal the decline with a verified identity, so one driver can't
		// decline an offer that's currently extended to another driver.
		if profile, err := driverSvc.GetProfile(r.Context(), claims.UserID); err == nil {
			engine.DeclineRide(rideID, profile.ID)
		}
		_ = driverSvc.RecordDecline(r.Context(), claims.UserID)
		respond.NoContent(w)
	}
}

// driverCancelHandler handles POST /driver/rides/:id/cancel.
// Replaces the old pickup-expiry-only handler. Drivers may now cancel from
// CONFIRMED, DRIVER_EN_ROUTE, or DRIVER_ARRIVED (before or after expiry).
// The customer is notified via WebSocket immediately.
func driverCancelHandler(rideSvc *ride.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := mw.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")
		var body struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Reason == "" {
			body.Reason = "driver cancelled"
		}
		if err := rideSvc.DriverCancelRide(r.Context(), rideID, claims.UserID, body.Reason); err != nil {
			respond.Error(w, err)
			return
		}
		respond.NoContent(w)
	}
}

// driverCancelAfterPickupExpiryHandler remains for backward-compatibility
// admin/ops tooling. Mobile apps use /cancel instead.
func driverCancelAfterPickupExpiryHandler(rideSvc *ride.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := mw.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")
		if err := rideSvc.CancelAfterPickupExpiry(r.Context(), rideID, claims.UserID); err != nil {
			respond.Error(w, err)
			return
		}
		respond.NoContent(w)
	}
}

func driverEnRouteHandler(rideSvc *ride.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := mw.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")
		if err := rideSvc.SetEnRoute(r.Context(), rideID, claims.UserID); err != nil {
			respond.Error(w, err)
			return
		}
		respond.NoContent(w)
	}
}

func driverArriveHandler(rideSvc *ride.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := mw.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")
		if err := rideSvc.SetDriverArrived(r.Context(), rideID, claims.UserID); err != nil {
			respond.Error(w, err)
			return
		}
		respond.NoContent(w)
	}
}

func driverStartHandler(rideSvc *ride.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := mw.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")
		if err := rideSvc.StartRide(r.Context(), rideID, claims.UserID); err != nil {
			respond.Error(w, err)
			return
		}
		respond.NoContent(w)
	}
}

func driverCompleteHandler(rideSvc *ride.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := mw.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")

		var body struct {
			DestLat     *float64 `json:"dest_lat"`
			DestLng     *float64 `json:"dest_lng"`
			DestAddress *string  `json:"dest_address"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		var finalDest *geo.Point
		if body.DestLat != nil || body.DestLng != nil {
			if body.DestLat == nil || body.DestLng == nil {
				respond.Error(w, apperrors.ErrBadRequest)
				return
			}
			finalDest = &geo.Point{Lat: *body.DestLat, Lng: *body.DestLng}
		}

		if err := rideSvc.CompleteRide(r.Context(), rideID, claims.UserID, finalDest, body.DestAddress); err != nil {
			respond.Error(w, err)
			return
		}
		respond.NoContent(w)
	}
}

const apiV1Prefix = "/api/v1"

// recoverOrphanedRides runs once at startup to handle rides whose in-memory
// goroutines or timers were lost due to a server crash or deploy restart.
//
//   - SEARCHING → re-queue the matching search so the customer isn't left spinning.
//   - NEGOTIATING → cancel the ride and notify; the negotiation state is too
//     ephemeral to safely replay (agreed-fare context is gone).
//
// Rides in other non-terminal states (CONFIRMED, DRIVER_EN_ROUTE, etc.) are
// left untouched — they are handled by driver/customer re-connection or admin.
func recoverOrphanedRides(
	ctx context.Context,
	pool *pgxpool.Pool,
	rdb goredis.UniversalClient,
	engine *matching.Engine,
	hub *tracking.Hub,
	log zerolog.Logger,
) {
	// Small delay so all services are fully wired before we start re-queueing.
	time.Sleep(2 * time.Second)

	type orphan struct {
		id            string
		status        string
		customerID    string
		transportType string
		pickupLat     float64
		pickupLng     float64
	}

	rows, err := pool.Query(ctx, `
		SELECT id, status, customer_id, transport_type,
		       ST_Y(pickup_point::geometry) AS pickup_lat,
		       ST_X(pickup_point::geometry) AS pickup_lng
		FROM rides
		WHERE status IN ('SEARCHING','NEGOTIATING')
		  AND created_at > NOW() - INTERVAL '2 hours'
	`)
	if err != nil {
		log.Error().Err(err).Msg("recovery: failed to query orphaned rides")
		return
	}
	defer rows.Close()

	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.status, &o.customerID, &o.transportType, &o.pickupLat, &o.pickupLng); err != nil {
			continue
		}
		orphans = append(orphans, o)
	}
	if rows.Err() != nil {
		log.Error().Err(rows.Err()).Msg("recovery: row scan error")
	}

	if len(orphans) == 0 {
		log.Info().Msg("recovery: no orphaned rides found")
		return
	}

	log.Warn().Int("count", len(orphans)).Msg("recovery: orphaned rides found — processing")

	for _, o := range orphans {
		switch o.status {
		case "SEARCHING":
			// Re-queue matching — customer is still waiting.
			log.Info().Str("ride_id", o.id).Msg("recovery: re-queuing SEARCHING ride")
			engine.StartSearch(o.id, geo.Point{Lat: o.pickupLat, Lng: o.pickupLng}, o.transportType)

		case "NEGOTIATING":
			// Negotiation timers are in-memory; we can't safely resume. Cancel and
			// tell the customer to request a new ride.
			log.Info().Str("ride_id", o.id).Msg("recovery: cancelling orphaned NEGOTIATING ride")
			_, _ = pool.Exec(ctx,
				`UPDATE rides SET status = 'CANCELLED', cancelled_by = 'SYSTEM', cancel_reason = 'server_restart', updated_at = NOW() WHERE id = $1 AND status = 'NEGOTIATING'`,
				o.id,
			)
			rdb.Del(ctx, "ride:state:"+o.id)
			rdb.Del(ctx, "customer:active_ride:"+o.customerID)
			hub.SendToCustomer(o.id, tracking.Message{
				Type:   "ride_cancelled",
				RideID: o.id,
				Payload: map[string]interface{}{
					"reason": "Server restarted during negotiation. Please request a new ride.",
				},
			})
		}
	}
}

const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Rides API - Swagger UI</title>
  <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  <style>
    html { box-sizing: border-box; overflow-y: scroll; }
    *, *:before, *:after { box-sizing: inherit; }
    body { margin:0; background: #fafafa; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" charset="UTF-8"></script>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-standalone-preset.js" charset="UTF-8"></script>
  <script>
    window.onload = function() {
      SwaggerUIBundle({
        url: "/swagger/openapi.json",
        dom_id: '#swagger-ui',
        deepLinking: true,
        presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePreset],
        layout: "BaseLayout"
      });
    };
  </script>
</body>
</html>`
