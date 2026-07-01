package tracking

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// safeClose closes ch exactly once. Safe to call from multiple goroutines.
func safeClose(ch chan struct{}, once *sync.Once) {
	once.Do(func() { close(ch) })
}

// Message is a typed payload sent over WebSocket connections.
type Message struct {
	Type    string                 `json:"type"`
	RideID  string                 `json:"ride_id,omitempty"`
	Payload map[string]interface{} `json:"payload,omitempty"`
}

// Client represents a single WebSocket connection.
type Client struct {
	UserID    string
	RideID    string // set for customers tracking a specific ride
	Role      string // "DRIVER" | "CUSTOMER"
	Send      chan Message
	done      chan struct{}
	closeOnce sync.Once
}

// Done signals the client's goroutines to stop. Safe to call multiple times
// and from multiple goroutines — only the first call closes the channel.
func (c *Client) Done() {
	safeClose(c.done, &c.closeOnce)
}

// Hub manages all active WebSocket connections and propagates broadcasts across
// horizontal API instances using Redis Pub/Sub.
type Hub struct {
	drivers   map[string]*Client
	customers map[string]*Client
	rdb       goredis.UniversalClient
	mu        sync.RWMutex
	log       zerolog.Logger
	pubsub    *goredis.PubSub
}

func NewHub(rdb goredis.UniversalClient, log zerolog.Logger) *Hub {
	h := &Hub{
		drivers:   make(map[string]*Client),
		customers: make(map[string]*Client),
		rdb:       rdb,
		log:       log,
	}
	h.startPubSub()
	return h
}

func (h *Hub) startPubSub() {
	if h.rdb == nil {
		h.log.Warn().Msg("ws pubsub: Redis client is nil, running in local-only mode")
		return
	}

	go func() {
		ctx := context.Background()
		h.pubsub = h.rdb.PSubscribe(ctx, "ws:driver:*", "ws:ride:*")
		ch := h.pubsub.Channel()
		for msg := range ch {
			h.handlePubSubMessage(msg.Channel, msg.Payload)
		}
	}()
}

func (h *Hub) Close() error {
	if h.pubsub != nil {
		return h.pubsub.Close()
	}
	return nil
}

func (h *Hub) handlePubSubMessage(channel, payload string) {
	var msg Message
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		h.log.Error().Err(err).Msg("ws pubsub: failed to unmarshal payload")
		return
	}

	if strings.HasPrefix(channel, "ws:driver:") {
		driverUserID := strings.TrimPrefix(channel, "ws:driver:")
		h.mu.RLock()
		client, ok := h.drivers[driverUserID]
		h.mu.RUnlock()
		if ok {
			select {
			case client.Send <- msg:
			default:
				h.log.Warn().Str("driver_id", driverUserID).Msg("ws: driver send buffer full (pubsub)")
			}
		}
	} else if strings.HasPrefix(channel, "ws:ride:") {
		rideID := strings.TrimPrefix(channel, "ws:ride:")
		h.mu.RLock()
		client, ok := h.customers[rideID]
		h.mu.RUnlock()
		if ok {
			select {
			case client.Send <- msg:
			default:
				h.log.Warn().Str("ride_id", rideID).Msg("ws: customer send buffer full (pubsub)")
			}
		}
	}
}

// RegisterDriver adds a driver WebSocket client to the hub.
func (h *Hub) RegisterDriver(userID string, client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.drivers[userID]; ok {
		existing.Done()
	}
	h.drivers[userID] = client
	h.log.Info().Str("user_id", userID).Msg("ws: driver connected")
}

// UnregisterDriver removes a driver client.
func (h *Hub) UnregisterDriver(userID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.drivers, userID)
	h.log.Info().Str("user_id", userID).Msg("ws: driver disconnected")
}

// RegisterCustomer adds a customer WebSocket client keyed by ride_id.
func (h *Hub) RegisterCustomer(rideID, userID string, client *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.customers[rideID]; ok {
		existing.Done()
	}
	h.customers[rideID] = client
	h.log.Info().Str("ride_id", rideID).Str("user_id", userID).Msg("ws: customer connected")
}

// UnregisterCustomer removes a customer client for a ride.
func (h *Hub) UnregisterCustomer(rideID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.customers, rideID)
}

// SendToDriver pushes a message to the driver (either locally or globally via Redis).
func (h *Hub) SendToDriver(driverUserID string, msg Message) {
	if h.rdb == nil {
		h.mu.RLock()
		client, ok := h.drivers[driverUserID]
		h.mu.RUnlock()
		if ok {
			select {
			case client.Send <- msg:
			default:
				h.log.Warn().Str("driver_id", driverUserID).Msg("ws: driver send buffer full")
			}
		}
		return
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		h.log.Error().Err(err).Msg("ws pubsub: failed to marshal driver message")
		return
	}
	ctx := context.Background()
	h.rdb.Publish(ctx, "ws:driver:"+driverUserID, string(payload))
}

// SendToCustomer pushes a message to the customer (either locally or globally via Redis).
func (h *Hub) SendToCustomer(rideID string, msg Message) {
	if h.rdb == nil {
		h.mu.RLock()
		client, ok := h.customers[rideID]
		h.mu.RUnlock()
		if ok {
			select {
			case client.Send <- msg:
			default:
				h.log.Warn().Str("ride_id", rideID).Msg("ws: customer send buffer full")
			}
		}
		return
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		h.log.Error().Err(err).Msg("ws pubsub: failed to marshal customer message")
		return
	}
	ctx := context.Background()
	h.rdb.Publish(ctx, "ws:ride:"+rideID, string(payload))
}

// IsDriverConnected returns true if the driver has an active WebSocket locally.
func (h *Hub) IsDriverConnected(driverUserID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.drivers[driverUserID]
	return ok
}

// ActiveConnectionsCount returns the current count of local WebSocket connections.
func (h *Hub) ActiveConnectionsCount() (drivers, customers int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.drivers), len(h.customers)
}
