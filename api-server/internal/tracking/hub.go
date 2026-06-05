package tracking

import (
	"sync"

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
	UserID string
	RideID string // set for customers tracking a specific ride
	Role   string // "DRIVER" | "CUSTOMER"
	Send   chan Message
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
	// drivers: userID → *Client
	drivers map[string]*Client
	// customers: rideID → *Client (one customer per active ride)
	customers map[string]*Client

	mu  sync.RWMutex
	log zerolog.Logger
}

func NewHub(log zerolog.Logger) *Hub {
	return &Hub{
		drivers:   make(map[string]*Client),
		customers: make(map[string]*Client),
		log:       log,
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

// SendToDriver pushes a message to a specific driver's WebSocket.
func (h *Hub) SendToDriver(driverUserID string, msg Message) {
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

// SendToCustomer pushes a message to the customer tracking a specific ride.
func (h *Hub) SendToCustomer(rideID string, msg Message) {
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

// IsDriverConnected returns true if the driver has an active WebSocket.
func (h *Hub) IsDriverConnected(driverUserID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.drivers[driverUserID]
	return ok
}
