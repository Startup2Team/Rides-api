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

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/admin"
	"github.com/workspace/ride-platform/internal/analytics"
	"github.com/workspace/ride-platform/internal/auth"
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
	"github.com/workspace/ride-platform/internal/reports"
	"github.com/workspace/ride-platform/internal/ride"
	"github.com/workspace/ride-platform/internal/settings"
	"github.com/workspace/ride-platform/internal/team"
	"github.com/workspace/ride-platform/internal/telephony"
	"github.com/workspace/ride-platform/internal/tickets"
	"github.com/workspace/ride-platform/internal/tracking"
	"github.com/workspace/ride-platform/internal/upload"
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
	_ = payment.New(cfg, log)
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
	pkgSvc := packages.NewService(pkgRepo, log)
	// rideSvc needs hub for WS notifications; engine is set after construction
	rideSvc := ride.NewService(rideRepo, rdb, notifySvc, anaSvc, hub, cfg, log)
	// engine needs rideSvc for negotiation timeout; rideSvc needs engine for matching
	engine := matching.NewEngine(rideRepo, driverRepo, rdb, notifySvc, anaSvc, hub, cfg, log, rideSvc)
	negSvc := negotiation.NewService(negRepo, rideRepo, rdb, hub, telSvc, anaSvc, cfg, log)
	rideSvc.SetFareRepository(fareRepo)
	negSvc.SetFareRepository(fareRepo)
	adminSvc := admin.NewService(db, log)
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
	custH := customer.NewHandler(custSvc)
	driverH := driver.NewHandler(driverSvc)
	rideH := ride.NewHandler(rideSvc)
	negH := negotiation.NewHandler(negSvc)
	adminH := admin.NewHandler(adminSvc)
	anaH := analytics.NewHandler(anaRepo)
	trackH := tracking.NewHandler(hub, driverSvc, cfg, log)
	locH := location.NewHandler(locSvc, rideSvc)
	fareH := fare.NewHandler(fareRepo, locSvc)
	pkgH := packages.NewHandler(pkgSvc)
	var uploadH *upload.Handler
	if uh, err := upload.NewHandler(cfg); err != nil {
		log.Warn().Err(err).Msg("upload: storage not configured, presign endpoint disabled")
	} else {
		uploadH = uh
	}

	// New module handlers
	incidentH := incidents.NewHandler(incidentSvc)
	ticketH := tickets.NewHandler(ticketSvc)
	inboxH := inbox.NewHandler(inboxSvc)
	reportH := reports.NewHandler(reportSvc)
	settingsH := settings.NewHandler(settingsSvc)
	teamH := team.NewHandler(teamSvc)
	dashH := dashboard.NewHandler(dashSvc)

	// ── Background goroutines ─────────────────────────────────────────────────
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()

	consumer := analytics.NewConsumer(rdb, log)
	go consumer.Run(bgCtx)

	// Pre-warm landmark route cache
	locSvc.WarmLandmarkRoutes(bgCtx)

	// Pre-warm dashboard cache and start background refresh
	dashSvc.WarmCache(bgCtx)
	go dashSvc.PollLoop(bgCtx)

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
	r.Use(chimw.RealIP)
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
		r.With(mw.OTPRateLimit(rdb, 5, time.Hour)).Post("/register", authH.Register)
		r.Post("/verify-otp", authH.VerifyOTP)
		r.Post("/refresh", authH.Refresh)
		r.With(mw.Authenticate(cfg, rdb)).Post("/logout", authH.Logout)
	})

	// ── Customer ──────────────────────────────────────────────────────────────
	r.Route(apiV1Prefix+"/customer", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))
		r.Use(mw.RequireRole(mw.RoleCustomer, mw.RoleDriverActive, mw.RoleDriverPending))

		r.Get("/profile", custH.GetProfile)
		r.Put("/profile", custH.UpdateProfile)
		r.Post("/location", driver.NearbyDriversHandler(driverSvc))
		r.Get("/fare-estimate", fareH.FareEstimate)

		r.Post("/rides", rideH.CreateRide)
		r.Get("/rides", rideH.ListRides)
		r.Get("/rides/{ride_id}", rideH.GetRide)
		r.Delete("/rides/{ride_id}", rideH.CancelRide)

		r.Post("/rides/{ride_id}/negotiation/propose", negH.Propose("CUSTOMER"))
		r.Post("/rides/{ride_id}/negotiation/accept", negH.Accept("CUSTOMER"))
		r.Post("/rides/{ride_id}/negotiation/decline", negH.Decline("CUSTOMER"))
	})

	// ── Driver ────────────────────────────────────────────────────────────────
	r.Route(apiV1Prefix+"/driver", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))

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
			r.Post("/location", driverH.UpdateLocation)

			r.Get("/packages", pkgH.ListPackages)
			r.Post("/packages/purchase", pkgH.PurchasePackage)
			r.Get("/credits", pkgH.GetCredits)

			r.Get("/rides/active", rideH.GetActiveRideForDriver)
			r.Post("/rides/{ride_id}/accept", driverAcceptHandler(engine, rideRepo, driverSvc, pkgSvc, cfg))
			r.Post("/rides/{ride_id}/decline", driverDeclineHandler(engine, driverSvc))
			r.Get("/rides/{ride_id}", rideH.GetRideForDriver)
			r.Post("/rides/{ride_id}/cancel", driverCancelAfterPickupExpiryHandler(rideSvc))
			r.Post("/rides/{ride_id}/en-route", driverEnRouteHandler(rideSvc))
			r.Post("/rides/{ride_id}/arrive", driverArriveHandler(rideSvc))
			r.Post("/rides/{ride_id}/start", driverStartHandler(rideSvc))
			r.Post("/rides/{ride_id}/complete", driverCompleteHandler(rideSvc))

			r.Post("/rides/{ride_id}/negotiation/propose", negH.Propose("DRIVER"))
			r.Post("/rides/{ride_id}/negotiation/accept", negH.Accept("DRIVER"))
			r.Post("/rides/{ride_id}/negotiation/decline", negH.Decline("DRIVER"))
			r.Post("/rides/{ride_id}/negotiation/lock-fare", negH.LockManualFare)
			r.Post("/rides/{ride_id}/negotiation/initiate-call", negH.InitiateCall)

			r.Get("/earnings/daily", driverH.DailyEarnings)
			r.Get("/earnings/weekly", driverH.WeeklyEarnings)
			r.Get("/stats", driverH.Stats)
		})
	})

	// ── Users (mode switch, saved locations) ──────────────────────────────────
	r.Route(apiV1Prefix+"/users", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))

		r.Patch("/mode", locH.SwitchMode)

		r.Get("/me/saved-locations", locH.ListSavedLocations)
		r.Post("/me/saved-locations", locH.CreateSavedLocation)
		r.Put("/me/saved-locations/{id}", locH.UpdateSavedLocation)
		r.Delete("/me/saved-locations/{id}", locH.DeleteSavedLocation)
	})

	r.Route(apiV1Prefix+"/uploads", func(r chi.Router) {
		r.Use(mw.Authenticate(cfg, rdb))
		if uploadH != nil {
			r.Post("/presigned-url", uploadH.PresignedURL)
		}
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

		// Auth (protected actions)
		r.Post("/auth/logout", teamH.Logout)
		r.Post("/auth/2fa/reissue", teamH.Reissue2FAChallenge)
		r.Post("/auth/totp/reset", teamH.ResetTOTP)

		// Dashboard
		r.Get("/dashboard", dashH.Get)
		r.Get("/dashboard/revenue-series", dashH.RevenueSeries)
		r.Get("/dashboard/rides-series", dashH.RidesSeries)
		r.Get("/dashboard/driver-status", dashH.DriverStatusSnapshot)
		r.Get("/dashboard/top-drivers", dashH.TopDrivers)
		r.Get("/dashboard/recent-activity", dashH.RecentActivity)
		r.Get("/dashboard/alerts", dashH.Alerts)
		r.Get("/dashboard/live-map", dashH.LiveMap)

		// Account (self)
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

		// Drivers
		r.Get("/drivers", adminH.ListDrivers)
		r.Post("/drivers", adminH.CreateDriver)
		r.Get("/drivers/overview", adminH.DriverOverview)
		r.Get("/drivers/{id}", adminH.GetDriver)
		r.Post("/drivers/{id}/force-offline", adminH.ForceDriverOffline)
		r.Patch("/drivers/{id}", adminH.UpdateDriver)
		r.Delete("/drivers/{id}", adminH.DeleteDriver)
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
		r.Get("/pricing", fareH.ListActivePricing)
		r.Get("/pricing/{vehicle_type_code}", fareH.GetActivePricingByType)
		r.Get("/pricing/{vehicle_type_code}/history", fareH.GetPricingHistory)
		r.Post("/pricing/{vehicle_type_code}", fareH.CreatePricing)

		// Pricing
		r.Get("/pricing", fareH.ListActivePricing)
		r.Get("/pricing/{vehicle_type_code}", fareH.GetActivePricingByType)
		r.Get("/pricing/{vehicle_type_code}/history", fareH.GetPricingHistory)
		r.Post("/pricing/{vehicle_type_code}", fareH.CreatePricing)

		// Negotiations
		r.Get("/negotiations/stats", adminH.NegotiationsStats)
		r.Get("/negotiations", adminH.ListNegotiations)
		r.Get("/negotiations/{id}", adminH.GetNegotiation)

		// Revenue
		r.Get("/revenue", adminH.Revenue)
		r.Get("/revenue/kpis", adminH.RevenueKPIs)
		r.Get("/revenue/transactions", adminH.ListTransactions)
		r.Post("/revenue/payouts/disburse", adminH.DisbursePayouts)

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
		r.Delete("/reports/{id}", reportH.Delete)

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
		r.Post("/team/members/{id}/remove", teamH.Remove)
		r.Post("/team/members/{id}/set-password", teamH.SetPassword)
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
	rideSvc.SetPackagesService(pkgSvc)
	adminSvc.SetPackagesService(pkgSvc)

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

const swaggerHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>Taravelis API - Swagger UI</title>
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
