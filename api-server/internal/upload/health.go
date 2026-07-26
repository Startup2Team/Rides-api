package upload

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/rs/zerolog"
)

// isNotFound distinguishes "this key does not exist" from every other storage
// failure. Both surface to the client as a 404, but only the latter is an
// incident worth alerting on — conflating them is how a broken bucket would
// look identical to a stale link.
func isNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	var notFound *types.NotFound
	return errors.As(err, &noSuchKey) || errors.As(err, &notFound)
}

// storageProbeInterval is how often the background watcher re-checks the
// bucket. Ten minutes is frequent enough that a revoked credential is caught
// within one coffee break, and slack enough that the alert dedupe window
// (10 min) collapses a sustained outage into roughly one message per cycle.
const storageProbeInterval = 10 * time.Minute

// CheckStorage performs a cheap authenticated round-trip against the bucket.
//
// This exists because of a silent outage: the R2 API token was revoked and
// every presigned upload began failing with 401 — but the PUT goes from the
// phone straight to R2, so the API never saw an error, and nothing in the
// system noticed that driver_documents had stopped growing. HeadBucket uses
// the same credential the presigner signs with, so if this passes, uploads
// can too.
func (h *Handler) CheckStorage(ctx context.Context) error {
	if h == nil {
		return fmt.Errorf("storage is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := h.client.HeadBucket(ctx, &s3.HeadBucketInput{
		Bucket: aws.String(h.cfg.Storage.Bucket),
	})
	if err != nil {
		return fmt.Errorf("bucket %q unreachable: %w", h.cfg.Storage.Bucket, err)
	}
	return nil
}

// WatchStorage probes the bucket on a fixed interval and logs at Error level
// when it is unreachable, which the Telegram hook turns into an alert. It logs
// again at Info on recovery so a resolved incident is visible in the same
// channel. Returns immediately on a nil handler (storage unconfigured).
func (h *Handler) WatchStorage(ctx context.Context, log zerolog.Logger) {
	if h == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(storageProbeInterval)
		defer ticker.Stop()
		healthy := true
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if err := h.CheckStorage(ctx); err != nil {
				healthy = false
				// Error level → Telegram. Deduped by message, so a sustained
				// outage is one alert per cooldown rather than one per tick.
				log.Error().Err(err).
					Str("bucket", h.cfg.Storage.Bucket).
					Msg("storage: bucket unreachable — uploads are failing")
				continue
			}
			if !healthy {
				healthy = true
				log.Info().
					Str("bucket", h.cfg.Storage.Bucket).
					Msg("storage: bucket reachable again — uploads recovered")
			}
		}
	}()
}
