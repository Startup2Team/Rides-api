package dashboard

import (
	"context"
	"encoding/json"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/jackc/pgx/v5/pgxpool"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

const cacheTTL = 10 * time.Second

// Snapshot is the live platform summary returned to the admin dashboard.
type Snapshot struct {
	LiveRides            int     `json:"liveRides"`
	OnlineDrivers        int     `json:"onlineDrivers"`
	OpenTickets          int     `json:"openTickets"`
	RevenueToday         float64 `json:"revenueToday"`
	PendingVerifications int     `json:"pendingVerifications"`
	OpenIncidents        int     `json:"openIncidents"`
}

type Service struct {
	db    *pgxpool.Pool
	redis *goredis.Client
	log   zerolog.Logger
}

func NewService(db *pgxpool.Pool, rdb *goredis.Client, log zerolog.Logger) *Service {
	return &Service{db: db, redis: rdb, log: log}
}

// Get returns the cached snapshot or recomputes from DB + Redis.
func (s *Service) Get(ctx context.Context) (*Snapshot, error) {
	// Try cache first (10s TTL handles polling load)
	cacheKey := rkeys.K.DashboardCache()
	if cached, err := s.redis.Get(ctx, cacheKey).Result(); err == nil {
		var snap Snapshot
		if json.Unmarshal([]byte(cached), &snap) == nil {
			return &snap, nil
		}
	}

	snap, err := s.compute(ctx)
	if err != nil {
		return nil, err
	}

	if raw, err := json.Marshal(snap); err == nil {
		s.redis.Set(ctx, cacheKey, raw, cacheTTL)
	}
	return snap, nil
}

func (s *Service) compute(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{}

	// liveRides — rides currently active (not terminal)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides
		WHERE status NOT IN ('COMPLETED','CANCELLED')
	`).Scan(&snap.LiveRides)

	// onlineDrivers — drivers marked online
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM driver_profiles
		WHERE is_online = TRUE AND approval_status = 'ACTIVE'
	`).Scan(&snap.OnlineDrivers)

	// openTickets — support tickets not resolved/closed
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM support_tickets WHERE status IN ('OPEN','PENDING')
	`).Scan(&snap.OpenTickets)

	// revenueToday — completed ride fares since midnight Kigali (UTC+2)
	_ = s.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(agreed_fare),0)
		FROM rides
		WHERE status = 'COMPLETED'
		  AND completed_at >= DATE_TRUNC('day', NOW() AT TIME ZONE 'Africa/Kigali') AT TIME ZONE 'Africa/Kigali'
	`).Scan(&snap.RevenueToday)

	// pendingVerifications — drivers awaiting review
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM driver_profiles WHERE approval_status = 'PENDING_REVIEW'
	`).Scan(&snap.PendingVerifications)

	// openIncidents — not resolved (includes Escalated)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM safety_incidents WHERE status IN ('OPEN','ACKNOWLEDGED','ESCALATED')
	`).Scan(&snap.OpenIncidents)

	return snap, nil
}

// InvalidateCache forces a fresh computation on the next request.
func (s *Service) InvalidateCache(ctx context.Context) {
	s.redis.Del(ctx, rkeys.K.DashboardCache())
}

// WarmCache pre-computes and stores the snapshot (called at startup).
func (s *Service) WarmCache(ctx context.Context) {
	snap, err := s.compute(ctx)
	if err != nil {
		s.log.Warn().Err(err).Msg("dashboard: warm cache failed")
		return
	}
	if raw, err := json.Marshal(snap); err == nil {
		s.redis.Set(ctx, rkeys.K.DashboardCache(), raw, cacheTTL)
	}
	s.log.Info().Msg("dashboard: cache warmed")
}

// PollLoop refreshes the dashboard cache every 10 seconds in the background.
func (s *Service) PollLoop(ctx context.Context) {
	ticker := time.NewTicker(cacheTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap, err := s.compute(ctx)
			if err != nil {
				s.log.Warn().Err(err).Msg("dashboard: poll failed")
				continue
			}
			if raw, err := json.Marshal(snap); err == nil {
				s.redis.Set(ctx, rkeys.K.DashboardCache(), raw, cacheTTL)
			}
		}
	}
}
