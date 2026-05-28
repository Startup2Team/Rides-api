package notification

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
)

// Message is a push notification payload.
type Message struct {
	Token string // FCM device token
	Title string
	Body  string
	Data  map[string]string // custom key-value pairs for the mobile app
}

// Service wraps Firebase Cloud Messaging.
type Service struct {
	cfg    *config.Config
	log    zerolog.Logger
	client fcmClient
}

// fcmClient is an interface so we can swap in a mock in tests.
type fcmClient interface {
	Send(ctx context.Context, token, title, body string, data map[string]string) error
}

func New(cfg *config.Config, log zerolog.Logger) *Service {
	var client fcmClient

	if cfg.Firebase.ServiceAccountPath != "" {
		real, err := newFirebaseClient(cfg.Firebase.ServiceAccountPath)
		if err != nil {
			log.Warn().Err(err).Msg("FCM: could not initialise Firebase — push notifications disabled")
			client = &noopClient{log: log}
		} else {
			client = real
		}
	} else {
		log.Warn().Msg("FCM: FIREBASE_SERVICE_ACCOUNT_PATH not set — push notifications disabled")
		client = &noopClient{log: log}
	}

	return &Service{cfg: cfg, log: log, client: client}
}

// SendRideRequest sends a high-priority FCM push to a driver when a ride is matched.
func (s *Service) SendRideRequest(ctx context.Context, fcmToken, rideID, pickupAddress, destAddress string, distanceM float64) error {
	return s.client.Send(ctx, fcmToken,
		"New Ride Request",
		fmt.Sprintf("%.0fm away — %s → %s", distanceM, pickupAddress, destAddress),
		map[string]string{
			"type":           "ride_request",
			"ride_id":        rideID,
			"pickup_address": pickupAddress,
			"dest_address":   destAddress,
		},
	)
}

// SendNegotiationMessage notifies a party that the other party proposed a fare.
func (s *Service) SendNegotiationMessage(ctx context.Context, fcmToken, rideID string, amount float64, proposedBy string) error {
	return s.client.Send(ctx, fcmToken,
		"Counter-Offer",
		fmt.Sprintf("%s proposes RWF %.0f", proposedBy, amount),
		map[string]string{
			"type":        "negotiation_message",
			"ride_id":     rideID,
			"proposed_by": proposedBy,
		},
	)
}

// SendDriverArrived notifies the customer that the driver has arrived.
func (s *Service) SendDriverArrived(ctx context.Context, fcmToken, rideID string) error {
	return s.client.Send(ctx, fcmToken,
		"Driver Arrived",
		"Your driver is at the pickup point.",
		map[string]string{"type": "driver_arrived", "ride_id": rideID},
	)
}

// SendRideCancelled notifies a party that the ride was cancelled.
func (s *Service) SendRideCancelled(ctx context.Context, fcmToken, rideID, reason string) error {
	return s.client.Send(ctx, fcmToken,
		"Ride Cancelled",
		reason,
		map[string]string{"type": "ride_cancelled", "ride_id": rideID, "reason": reason},
	)
}

// SendCancelWarning notifies a customer they're approaching the cancellation limit.
func (s *Service) SendCancelWarning(ctx context.Context, fcmToken string, dailyCount int) error {
	return s.client.Send(ctx, fcmToken,
		"Cancellation Warning",
		fmt.Sprintf("You've cancelled %d rides today. Further cancellations may suspend your booking access.", dailyCount),
		map[string]string{"type": "cancel_warning"},
	)
}

// noopClient is used when FCM is not configured.
type noopClient struct {
	log zerolog.Logger
}

func (n *noopClient) Send(_ context.Context, token, title, body string, data map[string]string) error {
	n.log.Debug().Str("token", token[:min(len(token), 20)]+"...").Str("title", title).Msg("FCM noop: push not sent")
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
