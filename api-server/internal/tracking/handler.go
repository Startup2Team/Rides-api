package tracking

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/config"
	"github.com/workspace/ride-platform/internal/driver"
	"github.com/workspace/ride-platform/internal/middleware"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
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
	redis     *goredis.Client
	cfg       *config.Config
	log       zerolog.Logger
}

func NewHandler(hub *Hub, driverSvc *driver.Service, rdb *goredis.Client, cfg *config.Config, log zerolog.Logger) *Handler {
	return &Handler{hub: hub, driverSvc: driverSvc, redis: rdb, cfg: cfg, log: log}
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

	// ── State replay on reconnect ─────────────────────────────────────────
	// After a kill/background/signal-drop the driver reconnects here.
	// We push the current ride state immediately so the app can jump back to
	// the correct screen (navigate → arrived → in_progress) without needing
	// a separate REST call or waiting for the next WS event.
	{
		ctx := r.Context()
		if rideID, rerr := h.redis.Get(ctx, rkeys.K.DriverActiveRide(driverProfile.ID)).Result(); rerr == nil && rideID != "" {
			if state, serr := h.redis.Get(ctx, rkeys.K.RideState(rideID)).Result(); serr == nil && state != "" {
				select {
				case client.Send <- Message{
					Type:   "ride_state",
					RideID: rideID,
					Payload: map[string]interface{}{
						"status":  state,
						"ride_id": rideID,
					},
				}:
				default:
				}
			}
		}
	}

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
				continue
			}
			// Forward real-time position to the customer watching this driver's active ride.
			// Also persist it under ride:<rideID>:driver_location so a reconnecting
			// customer immediately receives the driver's last known position.
			if rideID, rerr := h.redis.Get(r.Context(), rkeys.K.DriverActiveRide(driverProfile.ID)).Result(); rerr == nil && rideID != "" {
				locJSON, _ := json.Marshal(map[string]interface{}{
					"lat": incoming.Lat,
					"lng": incoming.Lng,
				})
				h.redis.Set(r.Context(), rkeys.K.RideDriverLocation(rideID), string(locJSON), 30*time.Minute)
				h.hub.SendToCustomer(rideID, Message{
					Type:   "driver_location",
					RideID: rideID,
					Payload: map[string]interface{}{
						"lat": incoming.Lat,
						"lng": incoming.Lng,
					},
				})
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

	// ── State replay on reconnect ─────────────────────────────────────────
	// On every connect (first connect or reconnect after background/kill) we
	// push the current ride state so the customer app can navigate to the
	// right screen instantly — no polling required.
	// We also include the driver's last known GPS so the map isn't blank.
	{
		ctx := r.Context()
		if state, serr := h.redis.Get(ctx, rkeys.K.RideState(rideID)).Result(); serr == nil && state != "" {
			payload := map[string]interface{}{"status": state}
			if locJSON, lerr := h.redis.Get(ctx, rkeys.K.RideDriverLocation(rideID)).Result(); lerr == nil {
				var loc struct {
					Lat float64 `json:"lat"`
					Lng float64 `json:"lng"`
				}
				if json.Unmarshal([]byte(locJSON), &loc) == nil {
					payload["driver_lat"] = loc.Lat
					payload["driver_lng"] = loc.Lng
				}
			}
			select {
			case client.Send <- Message{Type: "ride_state", RideID: rideID, Payload: payload}:
			default:
			}
		}
	}

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
