package tracking

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		// Allow all origins — mobile app clients connect from various origins.
		// In production, restrict to your app's domain.
		return true
	},
}

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
	maxMsgSize = 512
)

// Handler manages WebSocket upgrades for drivers and customers.
type Handler struct {
	hub       *Hub
	driverSvc *driver.Service
	cfg       *config.Config
	log       zerolog.Logger
}

func NewHandler(hub *Hub, driverSvc *driver.Service, cfg *config.Config, log zerolog.Logger) *Handler {
	return &Handler{hub: hub, driverSvc: driverSvc, cfg: cfg, log: log}
}

// WS /ws/driver
// Driver connects here when online. Sends location updates; receives ride requests.
func (h *Handler) DriverWS(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}

	// Resolve the driver profile so the hub is keyed by profile_id.
	// rides.driver_id stores the profile_id (set by AssignDriver), so all
	// SendToDriver calls in ride/service and negotiation/service use that same
	// profile_id.  Without this lookup the hub key is the user_id and messages
	// from those services are silently dropped.
	driverProfile, err := h.driverSvc.GetProfile(r.Context(), claims.UserID)
	if err != nil {
		respond.ErrorMsg(w, http.StatusForbidden, "DRIVER_PROFILE_REQUIRED", "active driver profile required")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Error().Err(err).Msg("ws: driver upgrade failed")
		return
	}
	defer conn.Close()

	client := &Client{
		UserID: driverProfile.ID, // profile_id — consistent with rides.driver_id
		Role:   "DRIVER",
		Send:   make(chan Message, 32),
		done:   make(chan struct{}),
	}

	h.hub.RegisterDriver(driverProfile.ID, client)
	defer h.hub.UnregisterDriver(driverProfile.ID)

	// Write pump — sends messages from hub to driver
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case msg, ok := <-client.Send:
				conn.SetWriteDeadline(time.Now().Add(writeWait))
				if !ok {
					conn.WriteMessage(websocket.CloseMessage, []byte{})
					return
				}
				if err := conn.WriteJSON(msg); err != nil {
					return
				}
			case <-ticker.C:
				conn.SetWriteDeadline(time.Now().Add(writeWait))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-client.done:
				return
			}
		}
	}()

	// Read pump — receives location updates from driver
	conn.SetReadLimit(maxMsgSize * 10)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				h.log.Error().Err(err).Str("user_id", claims.UserID).Msg("ws: driver unexpected close")
			}
			break
		}

		var incoming struct {
			Type     string   `json:"type"`
			Lat      float64  `json:"lat"`
			Lng      float64  `json:"lng"`
			SpeedKMH *float64 `json:"speed_kmh"`
			Heading  *float64 `json:"heading"`
		}
		if err := json.Unmarshal(raw, &incoming); err != nil {
			continue
		}

		if incoming.Type == "location_update" {
			update := driver.LocationUpdate{
				Lat:      incoming.Lat,
				Lng:      incoming.Lng,
				SpeedKMH: incoming.SpeedKMH,
				Heading:  incoming.Heading,
			}
			if err := h.driverSvc.UpdateLocation(r.Context(), claims.UserID, update); err != nil {
				h.log.Warn().Err(err).Str("user_id", claims.UserID).Msg("ws: location update rejected")
				client.Send <- Message{Type: "error", Payload: map[string]interface{}{"message": err.Error()}}
			}
		}
	}
}

// WS /ws/customer
// Customer connects after ride reaches CONFIRMED status.
// Receives driver location, arrival, and completion events.
func (h *Handler) CustomerWS(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		respond.Error(w, apperrors.ErrUnauthorized)
		return
	}

	rideID := r.URL.Query().Get("ride_id")
	if rideID == "" {
		respond.ErrorMsg(w, http.StatusBadRequest, "MISSING_RIDE_ID", "ride_id query param required")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Error().Err(err).Msg("ws: customer upgrade failed")
		return
	}
	defer conn.Close()

	client := &Client{
		UserID: claims.UserID,
		RideID: rideID,
		Role:   "CUSTOMER",
		Send:   make(chan Message, 32),
		done:   make(chan struct{}),
	}

	h.hub.RegisterCustomer(rideID, claims.UserID, client)
	defer h.hub.UnregisterCustomer(rideID)

	// Write pump only — customers don't send location data
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Background reader to handle pongs and detect disconnects
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				close(client.done)
				return
			}
		}
	}()

	for {
		select {
		case msg, ok := <-client.Send:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-client.done:
			return
		}
	}
}
