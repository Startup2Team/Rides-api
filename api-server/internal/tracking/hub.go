package tracking

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	rkeys "github.com/workspace/ride-platform/pkg/redis"
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
	admins    map[string]*Client
	rdb       goredis.UniversalClient
	mu        sync.RWMutex
	log       zerolog.Logger
	pubsub    *goredis.PubSub
}

func NewHub(rdb goredis.UniversalClient, log zerolog.Logger) *Hub {
	h := &Hub{
		drivers:   make(map[string]*Client),
		customers: make(map[string]*Client),
		admins:    make(map[string]*Client),
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
	} else if strings.HasPrefix(channel, "ws:broadcast:drivers") {
		h.mu.RLock()
		for _, client := range h.drivers {
			select {
			case client.Send <- msg:
			default:
			}
		}
		h.mu.RUnlock()
	}
}

// wsPresenceTTL outlives the 54s server ping so a live driver is never briefly
// considered absent, while a hard-killed process still expires rather than
// leaving the driver marked present forever.
const wsPresenceTTL = 150 * time.Second

// RegisterDriver adds a driver WebSocket client to the hub.
//
// userID is in fact the driver_profiles.id (see the call site) — kept as the
// parameter name for continuity with the customer methods.
func (h *Hub) RegisterDriver(userID string, client *Client) {
	h.mu.Lock()
	if existing, ok := h.drivers[userID]; ok {
		existing.Done()
	}
	h.drivers[userID] = client
	h.mu.Unlock()

	// Publish presence to Redis so matching on ANY replica can see this driver.
	h.MarkDriverPresent(context.Background(), userID)
	h.log.Info().Str("driver_profile_id", userID).Msg("ws: driver connected")

	// Broadcast presence change to Web Admin in 0ms
	h.BroadcastToAdmins(Message{
		Type: "DRIVER_PRESENCE_CHANGED",
		Payload: map[string]interface{}{
			"driver_id":  userID,
			"is_online":  true,
			"updated_at": time.Now().Format(time.RFC3339),
		},
	})
}

// UnregisterDriver removes a driver client.
func (h *Hub) UnregisterDriver(userID string) {
	h.mu.Lock()
	delete(h.drivers, userID)
	h.mu.Unlock()

	h.rdb.Del(context.Background(), rkeys.K.DriverWSPresence(userID))
	h.log.Info().Str("driver_profile_id", userID).Msg("ws: driver disconnected")

	// Broadcast presence change to Web Admin in 0ms
	h.BroadcastToAdmins(Message{
		Type: "DRIVER_PRESENCE_CHANGED",
		Payload: map[string]interface{}{
			"driver_id":  userID,
			"is_online":  false,
			"updated_at": time.Now().Format(time.RFC3339),
		},
	})
}

func (h *Hub) RegisterAdmin(userID string, client *Client) {
	h.mu.Lock()
	if existing, ok := h.admins[userID]; ok {
		existing.Done()
	}
	h.admins[userID] = client
	h.mu.Unlock()
	h.log.Info().Str("admin_id", userID).Msg("ws: admin connected")
}

func (h *Hub) UnregisterAdmin(userID string) {
	h.mu.Lock()
	delete(h.admins, userID)
	h.mu.Unlock()
	h.log.Info().Str("admin_id", userID).Msg("ws: admin disconnected")
}

func (h *Hub) BroadcastToAdmins(msg Message) {
	h.mu.RLock()
	for _, client := range h.admins {
		select {
		case client.Send <- msg:
		default:
			h.log.Warn().Str("admin_id", client.UserID).Msg("ws: admin broadcast send buffer full")
		}
	}
	h.mu.RUnlock()

	if h.rdb != nil {
		payload, err := json.Marshal(msg)
		if err == nil {
			h.rdb.Publish(context.Background(), "ws:broadcast:admins", string(payload))
		}
	}
}

// MarkDriverPresent refreshes the driver's Redis presence marker. Called on
// connect and on every server ping, so presence survives as long as the socket.
func (h *Hub) MarkDriverPresent(ctx context.Context, driverProfileID string) {
	h.rdb.Set(ctx, rkeys.K.DriverWSPresence(driverProfileID), "1", wsPresenceTTL)
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

// NotifyCustomer is a driver.WSNotifier-compatible wrapper around
// SendToCustomer, expressed in primitive types so package driver can declare
// the interface without importing package tracking (tracking already imports
// driver for the WS handler's profile lookups, so the reverse import would
// cycle).
func (h *Hub) NotifyCustomer(rideID, msgType string, payload map[string]interface{}) {
	h.SendToCustomer(rideID, Message{Type: msgType, RideID: rideID, Payload: payload})
}

// IsDriverConnected reports whether the driver holds a live WebSocket on ANY
// replica.
//
// This used to consult only the local map, which meant matching could not see a
// driver connected to a different API process. With more than one replica that
// silently discarded most of the available supply, and the symptom — "no driver
// found" while drivers were plainly online — would have looked like a matching
// bug rather than a presence bug.
//
// The local map is still checked first: it is authoritative for this process and
// costs nothing, so a Redis blip cannot make a locally-connected driver vanish.
func (h *Hub) IsDriverConnected(driverProfileID string) bool {
	h.mu.RLock()
	_, local := h.drivers[driverProfileID]
	h.mu.RUnlock()
	if local {
		return true
	}
	n, err := h.rdb.Exists(context.Background(), rkeys.K.DriverWSPresence(driverProfileID)).Result()
	return err == nil && n > 0
}

// ActiveConnectionsCount returns the current count of local WebSocket connections.
func (h *Hub) ActiveConnectionsCount() (drivers, customers int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.drivers), len(h.customers)
}

// BroadcastToAllDrivers sends a real-time WebSocket message to all connected drivers.
func (h *Hub) BroadcastToAllDrivers(msg Message) {
	h.mu.RLock()
	for _, client := range h.drivers {
		select {
		case client.Send <- msg:
		default:
			h.log.Warn().Str("driver_id", client.UserID).Msg("ws: driver broadcast send buffer full")
		}
	}
	h.mu.RUnlock()

	if h.rdb != nil {
		payload, err := json.Marshal(msg)
		if err == nil {
			h.rdb.Publish(context.Background(), "ws:broadcast:drivers", string(payload))
		}
	}
}

func (h *Hub) NotifyPackageCatalogUpdated() {
	h.BroadcastToAllDrivers(Message{
		Type: "PACKAGE_CATALOG_UPDATED",
		Payload: map[string]interface{}{
			"updated_at": time.Now().Format(time.RFC3339),
		},
	})
}

func (h *Hub) NotifyDriverCreditsUpdated(driverProfileID string) {
	msg := Message{
		Type: "DRIVER_CREDITS_UPDATED",
		Payload: map[string]interface{}{
			"driver_id":  driverProfileID,
			"updated_at": time.Now().Format(time.RFC3339),
		},
	}
	if driverProfileID != "" {
		h.SendToDriver(driverProfileID, msg)
	} else {
		h.BroadcastToAllDrivers(msg)
	}
}

func (h *Hub) NotifyDriverAccountApproved(driverProfileID string) {
	msg := Message{
		Type: "DRIVER_ACCOUNT_APPROVED",
		Payload: map[string]interface{}{
			"driver_id":  driverProfileID,
			"updated_at": time.Now().Format(time.RFC3339),
		},
	}
	if driverProfileID != "" {
		h.SendToDriver(driverProfileID, msg)
	} else {
		h.BroadcastToAllDrivers(msg)
	}
}
