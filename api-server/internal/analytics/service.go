package analytics

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

// Service publishes business events to both Postgres (analytics_events) and
// the Redis Stream (analytics:events). A background consumer reads the stream
// and can forward to external data pipelines.
type Service struct {
	db    *pgxpool.Pool
	redis goredis.UniversalClient
	log   zerolog.Logger
}

func NewService(db *pgxpool.Pool, rdb goredis.UniversalClient, log zerolog.Logger) *Service {
	return &Service{db: db, redis: rdb, log: log}
}

// Publish writes an analytics event to Postgres and Redis Stream.
// This is a best-effort call — failures are logged, never surfaced to callers.
func (s *Service) Publish(ctx context.Context, eventType, actorRole, actorID string, rideID *string, payload map[string]interface{}) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		s.log.Error().Err(err).Str("event_type", eventType).Msg("analytics: marshal payload")
		return
	}

	// Write to DB
	_, err = s.db.Exec(ctx, `
		INSERT INTO analytics_events (event_type, actor_role, actor_id, ride_id, payload, occurred_at)
		VALUES ($1, $2, $3::UUID, $4::UUID, $5, $6)
	`, eventType, actorRole, actorID, rideID, payloadBytes, time.Now().UTC())
	if err != nil {
		s.log.Error().Err(err).Str("event_type", eventType).Msg("analytics: db write")
	}

	// Publish to Redis Stream
	streamPayload, _ := json.Marshal(map[string]interface{}{
		"event_type":  eventType,
		"actor_role":  actorRole,
		"actor_id":    actorID,
		"ride_id":     rideID,
		"payload":     payload,
		"occurred_at": time.Now().UTC().Format(time.RFC3339),
	})

	s.redis.XAdd(ctx, &goredis.XAddArgs{
		Stream: rkeys.K.AnalyticsStream(),
		Values: map[string]interface{}{
			"data": string(streamPayload),
		},
	})
}
