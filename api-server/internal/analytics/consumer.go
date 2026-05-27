package analytics

import (
	"context"
	"encoding/json"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

const (
	consumerGroup  = "analytics-consumer"
	consumerName   = "analytics-worker-1"
	blockTimeout   = 5 * time.Second
	batchSize      = 100
)

// Consumer reads from the Redis Stream and can forward events to external
// data pipelines (BigQuery, Kafka, etc.). For v1 it just logs them —
// the analytics_events table is already written synchronously.
type Consumer struct {
	redis *goredis.Client
	log   zerolog.Logger
}

func NewConsumer(rdb *goredis.Client, log zerolog.Logger) *Consumer {
	return &Consumer{redis: rdb, log: log}
}

// Run starts the blocking Redis Stream consumer loop.
// Call as a goroutine: go consumer.Run(ctx).
func (c *Consumer) Run(ctx context.Context) {
	stream := rkeys.K.AnalyticsStream()

	// Create consumer group (idempotent — ignore BUSYGROUP error)
	err := c.redis.XGroupCreateMkStream(ctx, stream, consumerGroup, "0").Err()
	if err != nil && err.Error() != "BUSYGROUP Consumer Group name already exists" {
		c.log.Error().Err(err).Msg("analytics consumer: create group")
		return
	}

	c.log.Info().Str("stream", stream).Msg("analytics consumer: started")

	for {
		select {
		case <-ctx.Done():
			c.log.Info().Msg("analytics consumer: shutting down")
			return
		default:
		}

		msgs, err := c.redis.XReadGroup(ctx, &goredis.XReadGroupArgs{
			Group:    consumerGroup,
			Consumer: consumerName,
			Streams:  []string{stream, ">"},
			Count:    batchSize,
			Block:    blockTimeout,
		}).Result()

		if err != nil {
			if err == goredis.Nil {
				// No new messages — normal timeout
				continue
			}
			if ctx.Err() != nil {
				return
			}
			c.log.Error().Err(err).Msg("analytics consumer: xreadgroup")
			time.Sleep(1 * time.Second)
			continue
		}

		for _, msg := range msgs {
			for _, m := range msg.Messages {
				c.process(ctx, m)
				// Acknowledge
				c.redis.XAck(ctx, stream, consumerGroup, m.ID)
			}
		}
	}
}

func (c *Consumer) process(ctx context.Context, msg goredis.XMessage) {
	dataStr, ok := msg.Values["data"].(string)
	if !ok {
		return
	}

	var event map[string]interface{}
	if err := json.Unmarshal([]byte(dataStr), &event); err != nil {
		c.log.Error().Err(err).Str("msg_id", msg.ID).Msg("analytics consumer: unmarshal")
		return
	}

	c.log.Debug().
		Str("event_type", safeStr(event["event_type"])).
		Str("msg_id", msg.ID).
		Msg("analytics consumer: processed event")

	// TODO: Forward to external pipeline (Kafka, BigQuery, Segment, etc.)
}

func safeStr(v interface{}) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}
