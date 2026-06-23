package main

import (
	"context"
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
	} else {
		if cfg.Ride.DevSkipGeofence {
			log.Warn().Msg("⚠️  DEV_SKIP_GEOFENCE=true — geofence checks disabled (dev only)")
		}
		if cfg.Driver.DevAutoApprove {
			log.Warn().Msg("⚠️  DEV_AUTO_APPROVE_DRIVERS=true — driver approval skipped (dev only)")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── Database ──────────────────────────────────────────────────────────────
	db, err := pgpkg.New(ctx, cfg.Database.URL)
	if err != nil {
		log.Fatal().Err(err).Msg("postgres: connect")
	}
	defer db.Close()
	log.Info().Msg("postgres: connected")

	// ── Redis ─────────────────────────────────────────────────────────────────
	rdb, err := rdpkg.New(ctx, cfg.Redis.URL)
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
	anaRepo := analytics.NewRepository(db)

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
	hub := tracking.NewHub(log)

	// ── Domain services ───────────────────────────────────────────────────────
	authSvc := auth.NewService(authRepo, rdb, telSvc, cfg, log)
	driverSvc := driver.NewService(driverRepo, rdb, anaSvc, cfg, log)
	walletSvc := wallet.NewService(walletRepo, log, cfg.Payments.Enabled)
	bonusSvc := bonus.NewService(bonusRepo, log)
	pkgSvc := packages.NewService(pkgRepo, log)
	pkgSvc.SetWallet(walletSvc) // wallet deduction on package purchase
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
	dashSvc := dashboard.NewService(db, rdb, log)

	// ── Handlers ──────────────────────────────────────────────────────────────
	custSvc := customer.NewService(custRepo)

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
	ledgerSvc := packages.NewLedgerService(pkgRepo, log)
	devAutoConfirm := cfg.Env != "production" && cfg.Payments.Enabled
	purchaseSvc := packages.NewPurchaseService(pkgRepo, ledgerSvc, momoGateway{paymentSvc}, devAutoConfirm, log)
	pkgH := packages.NewHandler(pkgSvc, auditLog)
	pkgH.SetBonus(bonusSvc)
	pkgH.SetLedger(ledgerSvc)
	pkgH.SetPurchase(purchaseSvc)
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

	consumer := analytics.NewConsumer(rdb, log)
	go consumer.Run(bgCtx)

	// ── Orphaned-ride recovery ────────────────────────────────────────────────
	// After a crash or deploy restart, any ride in SEARCHING or NEGOTIATING
	// has lost its in-memory goroutine/timer. We scan for these on startup and
	// either re-queue the search or cancel + notify so customers aren't left
	// staring at a spinner forever.
	go recoverOrphanedRides(bgCtx, db, rdb, engine, hub, log.With().Str("component", "recovery").Logger())

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

	// ── Router ────────────────────────────────────────────────────────────────
	r := chi.NewRouter()

	// ── CORS ──────────────────────────────────────────────────────────────────
	// Allow the admin Next.js dev server and any configured production origin.
	allowedOrigins := []string{"http://localhost:3000", "http://localhost:3001"}
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
	// NOTE: chimw.RealIP is deliberately NOT used — it rewrites RemoteAddr from the
	// spoofable X-Forwarded-For/X-Real-IP on EVERY request, which would let clients
	// fake their IP for rate-limiting and logging. We use mw.trustedIP() instead,
	// which only trusts those headers from a private/loopback peer (our edge proxy).
	r.Use(chimw.Recoverer)
	r.Use(mw.WithLogger(log))
	r.Use(mw.HTTPLogger(log))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		respond.OK(w, map[string]string{"status": "ok"})
	})

	r.Get("/swagger", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(swaggerHTML))
	})
	r.Get("/swagger/openapi.json", func(w http.ResponseWriter, r *http.Request) {
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
		r.Post("/refresh", authH.Refresh)
		r.With(mw.Authenticate(cfg, rdb)).Post("/logout", authH.Logout)
	})

	// MoMo payment callback — public (called by the provider, not the app).
	r.Post(apiV1Prefix+"/webhooks/momo/callback", pkgH.WebhookMoMo)

	// ── Customer ──────────────────────────────────────────────────────────────
	r.Route(apiV1Prefix+"/customer", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))
		r.Use(mw.RequireNotSuspended())
		r.Use(mw.RequireRole(mw.RoleCustomer, mw.RoleDriverActive, mw.RoleDriverPending))

		r.Get("/profile", custH.GetProfile)
		r.Put("/profile", custH.UpdateProfile)
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
		r.Use(mw.RequireNotSuspended())

		r.Post("/apply", driverH.Apply)

		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(mw.RoleDriverActive, mw.RoleDriverPending))
			r.Get("/profile", driverH.GetProfile)
			r.Put("/profile", driverH.UpdateProfile)
			r.Post("/policy/accept", driverH.AcceptPolicy)
			r.Post("/documents", driverH.UploadDocument)
			r.Get("/documents", driverH.ListDocuments)
		})

		r.Group(func(r chi.Router) {
			r.Use(mw.RequireRole(mw.RoleDriverActive))

			r.Post("/availability", driverH.SetAvailability)
			// 20 req/min = 1 every 3 s. Drivers send every 5–12 s so this is
			// headroom for bursts without blocking normal use. Returns 204 (not
			// 429) so the app doesn't log a red error when a burst is trimmed.
			r.With(mw.UserRateLimit(rdb, "driver_location", 20, time.Minute)).
				Post("/location", driverH.UpdateLocation)

			r.Get("/packages", pkgH.ListPackages)
			r.Get("/campaigns/active", pkgH.ListActiveCampaigns)
			r.Post("/packages/purchase", pkgH.PurchasePackage)
			r.Get("/packages/purchases/{purchaseID}", pkgH.GetPurchaseStatus)
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
		}
		// Authenticated: request an upload target, and (proxy mode) stream bytes.
		r.Group(func(r chi.Router) {
			r.Use(mw.Authenticate(cfg, rdb))
			if uploadH != nil {
				r.Post("/presigned-url", uploadH.PresignedURL)
				r.Put("/objects/*", uploadH.PutObject)
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
	r.Post(apiV1Prefix+"/admin/auth/login", teamH.Login)
	r.Post(apiV1Prefix+"/admin/auth/2fa/verify", teamH.Verify2FA)
	r.Post(apiV1Prefix+"/admin/auth/2fa/backup", teamH.VerifyBackupCode)
	r.Post(apiV1Prefix+"/admin/auth/totp/reset-login", teamH.ResetTOTPLogin)

	// ── Admin (protected) ─────────────────────────────────────────────────────
	r.Route(apiV1Prefix+"/admin", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))
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

			// Drivers
			r.Post("/drivers/send-otp", adminH.SendDriverOTP)
			r.Post("/drivers/verify-otp", adminH.VerifyDriverOTP)
			r.Get("/drivers", adminH.ListDrivers)
			r.Post("/drivers", adminH.CreateDriver)
			r.Get("/drivers/overview", adminH.DriverOverview)
			r.Get("/drivers/{id}", adminH.GetDriver)
			r.Post("/drivers/{id}/force-offline", adminH.ForceDriverOffline)
			r.Patch("/drivers/{id}", adminH.UpdateDriver)
			// r.Delete("/drivers/{id}", adminH.DeleteDriver) REMOVED - suspend/reinstate only
			r.Post("/drivers/{id}/approve", adminH.ApproveDriver)
			r.Post("/drivers/{id}/reject", adminH.RejectDriver)
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
			r.Delete("/team/roles/{roleId}", teamH.DeleteRoleByID)
			r.Post("/team/members/{id}/role", teamH.UpdateRole)
			r.Post("/team/members/{id}/suspend", teamH.Suspend)
			r.Post("/team/members/{id}/reinstate", teamH.Reinstate)
			// r.Post("/team/members/{id}/remove", teamH.Remove) REMOVED - suspend/reinstate only
			r.Post("/team/members/{id}/set-password", teamH.SetPassword)

			// Audit Log
			r.Get("/audit", teamH.ListAuditLog)

			// Packages admin CRUD
			r.Get("/packages", pkgH.AdminListPackages)
			r.Post("/packages", pkgH.AdminCreatePackage)
			r.Patch("/packages/{id}", pkgH.AdminUpdatePackage)
			r.Post("/packages/{id}/toggle", pkgH.AdminTogglePackage)
			r.Delete("/packages/{id}", pkgH.AdminDeletePackage)

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

		_, valid := engine.ValidateAcceptTTL(r.Context(), rideID)
		if !valid {
			respond.Error(w, apperrors.ErrAcceptExpired)
			return
		}

		// Gate: driver must have credits for their vehicle type to accept rides.
		profile, err := driverSvc.GetProfile(r.Context(), claims.UserID)
		if err == nil {
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
		}

		engine.AcceptRide(rideID)
		respond.NoContent(w)
	}
}

func driverDeclineHandler(engine *matching.Engine, driverSvc *driver.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims := mw.GetClaims(r)
		rideID := chi.URLParam(r, "ride_id")
		engine.DeclineRide(rideID)
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
	rdb *goredis.Client,
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
