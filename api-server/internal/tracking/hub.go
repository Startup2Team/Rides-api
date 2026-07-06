package tracking

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// wsFanoutChannel is the Redis pub/sub channel every API instance subscribes to.
// A message published here reaches whichever instance actually holds the target
// socket — this is what makes WebSocket delivery work across multiple instances.
const wsFanoutChannel = "ws:fanout"

// fanoutEnvelope wraps a Message with routing info for cross-instance delivery.
// Origin lets an instance skip its own echo (it already delivered locally).
type fanoutEnvelope struct {
	Origin string  `json:"origin"`
	Kind   string  `json:"kind"`   // "driver" | "customer"
	Target string  `json:"target"` // driver userID or ride ID
	Msg    Message `json:"msg"`
}

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

// Hub manages all active WebSocket connections.
// Thread-safe — all access goes through channels or locks.
type Hub struct {
	drivers   map[string]*Client
	customers map[string]*Client

	mu  sync.RWMutex
	log zerolog.Logger

	// redis + instanceID power cross-instance fan-out. redis may be nil in tests
	// or single-box setups without a client — delivery then stays purely local.
	redis      goredis.UniversalClient
	instanceID string
}

// NewHub builds a hub. Pass the shared Redis client to enable cross-instance
// WebSocket delivery; pass nil for a purely local (single-process) hub.
func NewHub(log zerolog.Logger, rdb goredis.UniversalClient) *Hub {
	return &Hub{
		drivers:    make(map[string]*Client),
		customers:  make(map[string]*Client),
		log:        log,
		redis:      rdb,
		instanceID: uuid.NewString(),
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

// SendToDriver pushes a message to a specific driver's WebSocket — on whichever
// instance holds it. Delivers locally if the socket is here, and fans the
// message out over Redis so a sibling instance can deliver it if it isn't.
func (h *Hub) SendToDriver(driverUserID string, msg Message) {
	h.deliverLocalDriver(driverUserID, msg)
	h.publish("driver", driverUserID, msg)
}

// SendToCustomer pushes a message to the customer tracking a specific ride, on
// whichever instance holds the socket (local delivery + Redis fan-out).
func (h *Hub) SendToCustomer(rideID string, msg Message) {
	h.deliverLocalCustomer(rideID, msg)
	h.publish("customer", rideID, msg)
}

func (h *Hub) deliverLocalDriver(driverUserID string, msg Message) {
	h.mu.RLock()
	client, ok := h.drivers[driverUserID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case client.Send <- msg:
	default:
		h.log.Warn().Str("driver_id", driverUserID).Msg("ws: driver send buffer full")
	}
}

func (h *Hub) deliverLocalCustomer(rideID string, msg Message) {
	h.mu.RLock()
	client, ok := h.customers[rideID]
	h.mu.RUnlock()
	if !ok {
		return
	}
	select {
	case client.Send <- msg:
	default:
		h.log.Warn().Str("ride_id", rideID).Msg("ws: customer send buffer full")
	}
}

func (h *Hub) publish(kind, target string, msg Message) {
	if h.redis == nil {
		return
	}
	payload, err := json.Marshal(fanoutEnvelope{Origin: h.instanceID, Kind: kind, Target: target, Msg: msg})
	if err != nil {
		return
	}
	if err := h.redis.Publish(context.Background(), wsFanoutChannel, payload).Err(); err != nil {
		h.log.Warn().Err(err).Str("kind", kind).Msg("ws: fanout publish failed")
	}
}

// Run subscribes to the cross-instance fan-out channel and delivers each message
// to any socket THIS instance holds. Blocks until ctx is cancelled — start it in
// a goroutine once at boot. No-op when redis is nil.
func (h *Hub) Run(ctx context.Context) {
	if h.redis == nil {
		return
	}
	sub := h.redis.Subscribe(ctx, wsFanoutChannel)
	defer sub.Close()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			var env fanoutEnvelope
			if err := json.Unmarshal([]byte(m.Payload), &env); err != nil {
				continue
			}
			if env.Origin == h.instanceID {
				continue
			}
			switch env.Kind {
			case "driver":
				h.deliverLocalDriver(env.Target, env.Msg)
			case "customer":
				h.deliverLocalCustomer(env.Target, env.Msg)
			}
		}
	}
}

// IsDriverConnected returns true if the driver has an active WebSocket.
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
