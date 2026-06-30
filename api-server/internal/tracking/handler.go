package tracking

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"

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

	// Per-connection inbound message throttle (see read pump).
	wsMsgRatePerSec = 5
	wsMsgBurst      = 10
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
	//
	// Use a 5 s deadline so a slow DB query never hangs the WebSocket handshake.
	// A hung handshake causes ngrok to drop the TCP connection mid-SSL, which iOS
	// reports as the misleading OSStatus -9806 error instead of a clean 403.
	profileCtx, profileCancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer profileCancel()
	driverProfile, err := h.driverSvc.GetProfile(profileCtx, claims.UserID)
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
	// ── Presence tied to the socket ───────────────────────────────────────
	// The WebSocket connection is the real "is this driver reachable" signal.
	// On connect, (re)add an online+idle driver to the geo index so a reconnect
	// restores discoverability immediately. On disconnect, remove them so we
	// never offer a ride to a driver whose app is gone. A driver mid-ride is
	// left untouched in both cases (they're already out of the index and must
	// stay assigned).
	h.restoreDriverPresence(r.Context(), driverProfile.ID, driverProfile.TransportType)
	defer func() {
		client.Done()
		h.hub.UnregisterDriver(driverProfile.ID)
		h.clearDriverPresence(context.Background(), driverProfile.ID, driverProfile.TransportType)
	}()

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

	// Per-connection throttle on processed messages. Drivers send a location
	// every 5–12 s, so 5/s sustained with a burst of 10 is generous headroom;
	// anything beyond that (a misbehaving or malicious client flooding frames)
	// is dropped before the expensive Redis geo-index writes, without tearing
	// down the socket.
	msgLimiter := rate.NewLimiter(rate.Limit(wsMsgRatePerSec), wsMsgBurst)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				h.log.Error().Err(err).Str("user_id", claims.UserID).Msg("ws: driver unexpected close")
			}
			break
		}

		if !msgLimiter.Allow() {
			continue // over the per-connection rate — drop this frame
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
			// Apply EMA smoothing (α=0.4) before fan-out to reduce GPS jitter on the
			// customer map without introducing significant lag. The raw coordinates are
			// already persisted by UpdateLocation for geofence checks — we only smooth
			// the customer-facing position here.
			if rideID, rerr := h.redis.Get(r.Context(), rkeys.K.DriverActiveRide(driverProfile.ID)).Result(); rerr == nil && rideID != "" {
				smoothLat, smoothLng := incoming.Lat, incoming.Lng

				const emaAlpha = 0.4
				if prev, perr := h.redis.Get(r.Context(), rkeys.K.DriverSmoothedLocation(driverProfile.ID)).Result(); perr == nil {
					var prevLoc struct {
						Lat float64 `json:"lat"`
						Lng float64 `json:"lng"`
					}
					if json.Unmarshal([]byte(prev), &prevLoc) == nil && (prevLoc.Lat != 0 || prevLoc.Lng != 0) {
						smoothLat = emaAlpha*incoming.Lat + (1-emaAlpha)*prevLoc.Lat
						smoothLng = emaAlpha*incoming.Lng + (1-emaAlpha)*prevLoc.Lng
					}
				}

				smoothJSON, _ := json.Marshal(map[string]interface{}{
					"lat": smoothLat,
					"lng": smoothLng,
				})
				// Store smoothed position (no expiry — overwritten on every update,
				// cleared when driver goes offline).
				h.redis.Set(r.Context(), rkeys.K.DriverSmoothedLocation(driverProfile.ID), string(smoothJSON), 0)

				// Persist smoothed position for reconnecting customers.
				h.redis.Set(r.Context(), rkeys.K.RideDriverLocation(rideID), string(smoothJSON), 30*time.Minute)

				h.hub.SendToCustomer(rideID, Message{
					Type:   "driver_location",
					RideID: rideID,
					Payload: map[string]interface{}{
						"lat": smoothLat,
						"lng": smoothLng,
					},
				})
			}
		}
	}
}

// restoreDriverPresence re-adds an online, idle driver to the geo index from
// their last known location when their WebSocket connects. No-op if the driver
// isn't AVAILABLE or is mid-ride.
func (h *Handler) restoreDriverPresence(ctx context.Context, profileID, vehicleType string) {
	if state, _ := h.redis.Get(ctx, rkeys.K.DriverState(profileID)).Result(); state != "AVAILABLE" {
		return
	}
	if active, _ := h.redis.Get(ctx, rkeys.K.DriverActiveRide(profileID)).Result(); active != "" {
		return
	}
	raw, err := h.redis.Get(ctx, rkeys.K.DriverLocation(profileID)).Result()
	if err != nil {
		return
	}
	var loc struct {
		Lat float64 `json:"lat"`
		Lng float64 `json:"lng"`
	}
	if json.Unmarshal([]byte(raw), &loc) == nil && (loc.Lat != 0 || loc.Lng != 0) {
		h.redis.GeoAdd(ctx, rkeys.K.DriverGeoIndex(vehicleType), &goredis.GeoLocation{
			Name:      profileID,
			Longitude: loc.Lng,
			Latitude:  loc.Lat,
		})
	}
}

// clearDriverPresence removes a driver from the geo index when their WebSocket
// drops, so we never offer a ride to an unreachable app. A driver mid-ride is
// left assigned (and is already out of the index).
func (h *Handler) clearDriverPresence(ctx context.Context, profileID, vehicleType string) {
	if active, _ := h.redis.Get(ctx, rkeys.K.DriverActiveRide(profileID)).Result(); active != "" {
		return
	}
	h.redis.ZRem(ctx, rkeys.K.DriverGeoIndex(vehicleType), profileID)
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

	// Background reader to handle pongs and detect disconnects.
	// Uses client.Done() (sync.Once) so it never panics even if the hub
	// already closed the channel because the customer reconnected.
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				client.Done()
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
