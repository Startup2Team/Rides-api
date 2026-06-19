package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"

	"github.com/jackc/pgx/v5/pgxpool"
	rkeys "github.com/workspace/ride-platform/pkg/redis"
)

const cacheTTL = 10 * time.Second

// Snapshot is the live platform summary returned to the admin dashboard.
// Period-aware fields (revenueInPeriod, ridesInPeriod) are scoped to the
// requested window; all others are point-in-time counts.
type Snapshot struct {
	LiveRides            int     `json:"liveRides"`
	OnlineDrivers        int     `json:"onlineDrivers"`
	OpenTickets          int     `json:"openTickets"`
	RevenueInPeriod      float64 `json:"revenueInPeriod"`
	RidesInPeriod        int     `json:"ridesInPeriod"`
	PendingVerifications int     `json:"pendingVerifications"`
	OpenIncidents        int     `json:"openIncidents"`
}

// Window describes the time range for period-aware metrics.
// If From/To are both set, the exact range is used. Otherwise the window
// is the last Days days. Custom ranges bypass the per-period cache.
type Window struct {
	Days int
	From time.Time
	To   time.Time
}

func (w Window) isCustom() bool { return !w.From.IsZero() && !w.To.IsZero() }

type Service struct {
	db    *pgxpool.Pool
	redis *goredis.Client
	log   zerolog.Logger
}

func NewService(db *pgxpool.Pool, rdb *goredis.Client, log zerolog.Logger) *Service {
	return &Service{db: db, redis: rdb, log: log}
}

// Get returns the cached snapshot or recomputes from DB + Redis.
func (s *Service) Get(ctx context.Context, w Window) (*Snapshot, error) {
	if !w.isCustom() {
		if w.Days < 1 {
			w.Days = 1
		}
		cacheKey := fmt.Sprintf("%s:%d", rkeys.K.DashboardCache(), w.Days)
		if cached, err := s.redis.Get(ctx, cacheKey).Result(); err == nil {
			var snap Snapshot
			if json.Unmarshal([]byte(cached), &snap) == nil {
				return &snap, nil
			}
		}
		snap, err := s.compute(ctx, w)
		if err != nil {
			return nil, err
		}
		if raw, err := json.Marshal(snap); err == nil {
			s.redis.Set(ctx, cacheKey, raw, cacheTTL)
		}
		return snap, nil
	}

	// Custom range: skip cache entirely.
	return s.compute(ctx, w)
}

func (s *Service) compute(ctx context.Context, w Window) (*Snapshot, error) {
	snap := &Snapshot{}

	// liveRides — rides currently active (not terminal)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides
		WHERE status NOT IN ('COMPLETED','CANCELLED')
	`).Scan(&snap.LiveRides)

	// onlineDrivers — drivers marked online
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM driver_profiles
		WHERE is_online = TRUE AND approval_status = 'APPROVED'
	`).Scan(&snap.OnlineDrivers)

	// openTickets — support tickets not resolved/closed
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM support_tickets WHERE status IN ('OPEN','PENDING')
	`).Scan(&snap.OpenTickets)

	if w.isCustom() {
		// revenueInPeriod — completed fares within [from, to]
		_ = s.db.QueryRow(ctx, `
			SELECT COALESCE(SUM(agreed_fare),0)
			FROM rides
			WHERE status = 'COMPLETED'
			  AND completed_at >= $1 AND completed_at < $2
		`, w.From, w.To).Scan(&snap.RevenueInPeriod)

		// ridesInPeriod — rides created within [from, to]
		_ = s.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM rides
			WHERE created_at >= $1 AND created_at < $2
		`, w.From, w.To).Scan(&snap.RidesInPeriod)
	} else {
		days := w.Days
		if days < 1 {
			days = 1
		}
		// revenueInPeriod — completed fares within the last N days
		_ = s.db.QueryRow(ctx, `
			SELECT COALESCE(SUM(agreed_fare),0)
			FROM rides
			WHERE status = 'COMPLETED'
			  AND completed_at >= NOW() - ($1 || ' days')::INTERVAL
		`, days).Scan(&snap.RevenueInPeriod)

		// ridesInPeriod — rides created within the last N days
		_ = s.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM rides
			WHERE created_at >= NOW() - ($1 || ' days')::INTERVAL
		`, days).Scan(&snap.RidesInPeriod)
	}

	// pendingVerifications — drivers awaiting review
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM driver_profiles WHERE approval_status = 'PENDING_REVIEW'
	`).Scan(&snap.PendingVerifications)

	// openIncidents — not resolved (includes Escalated)
	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM safety_incidents WHERE status IN ('OPEN','ACKNOWLEDGED','ESCALATED')
	`).Scan(&snap.OpenIncidents)

	return snap, nil
}

// ── Revenue time series ──────────────────────────────────────────────────────

type RevenuePoint struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

type RevenueSeriesSide struct {
	Label  string         `json:"label"`
	Points []RevenuePoint `json:"points"`
	Total  float64        `json:"total"`
	Peak   *RevenuePoint  `json:"peak,omitempty"`
}

type RevenueSeries struct {
	Bucket   string            `json:"bucket"` // "hour" or "day"
	Current  RevenueSeriesSide `json:"current"`
	Previous RevenueSeriesSide `json:"previous"`
	DeltaPct float64           `json:"deltaPct"`
}

// RevenueSeries returns hourly/daily revenue buckets for the requested window
// plus the equivalent previous window for comparison.
func (s *Service) RevenueSeries(ctx context.Context, w Window) (*RevenueSeries, error) {
	curStart, curEnd, prevStart, prevEnd, bucket, curLabel, prevLabel := resolveWindows(w)

	current, err := s.bucketRevenue(ctx, curStart, curEnd, bucket)
	if err != nil {
		return nil, err
	}
	previous, err := s.bucketRevenue(ctx, prevStart, prevEnd, bucket)
	if err != nil {
		return nil, err
	}

	currentSide := RevenueSeriesSide{Label: curLabel, Points: current, Total: sumPoints(current), Peak: peakPoint(current)}
	previousSide := RevenueSeriesSide{Label: prevLabel, Points: previous, Total: sumPoints(previous), Peak: peakPoint(previous)}

	var delta float64
	if previousSide.Total > 0 {
		delta = ((currentSide.Total - previousSide.Total) / previousSide.Total) * 100
	} else if currentSide.Total > 0 {
		delta = 100
	}

	return &RevenueSeries{
		Bucket:   bucket,
		Current:  currentSide,
		Previous: previousSide,
		DeltaPct: delta,
	}, nil
}

// resolveWindows turns a Window into (curStart, curEnd, prevStart, prevEnd, bucket, labels).
// All times are UTC. Bucket is "hour" for ≤2-day windows, otherwise "day".
func resolveWindows(w Window) (time.Time, time.Time, time.Time, time.Time, string, string, string) {
	now := time.Now().UTC()
	var curStart, curEnd time.Time
	var curLabel, prevLabel string

	if !w.From.IsZero() && !w.To.IsZero() {
		curStart = w.From
		curEnd = w.To
		curLabel = w.From.Format("Jan 2") + " – " + w.To.Add(-time.Nanosecond).Format("Jan 2")
		length := curEnd.Sub(curStart)
		prevLabel = "Previous " + curLabel
		_ = length
	} else {
		days := w.Days
		if days < 1 {
			days = 1
		}
		curEnd = now
		curStart = now.Add(-time.Duration(days) * 24 * time.Hour)
		switch days {
		case 1:
			curLabel, prevLabel = "Today", "Yesterday"
		case 7:
			curLabel, prevLabel = "This Week", "Last Week"
		case 30:
			curLabel, prevLabel = "This Month", "Last Month"
		default:
			curLabel = fmt.Sprintf("Last %d days", days)
			prevLabel = "Previous " + curLabel
		}
	}

	length := curEnd.Sub(curStart)
	prevEnd := curStart
	prevStart := curStart.Add(-length)

	bucket := "day"
	if length <= 48*time.Hour {
		bucket = "hour"
	}
	return curStart, curEnd, prevStart, prevEnd, bucket, curLabel, prevLabel
}

func (s *Service) bucketRevenue(ctx context.Context, start, end time.Time, bucket string) ([]RevenuePoint, error) {
	if !end.After(start) {
		return []RevenuePoint{}, nil
	}
	step := "1 day"
	if bucket == "hour" {
		step = "1 hour"
	}
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		WITH series AS (
			SELECT generate_series(date_trunc('%s', $1::timestamptz),
			                       date_trunc('%s', $2::timestamptz - INTERVAL '1 %s'),
			                       INTERVAL '%s') AS bucket
		)
		SELECT s.bucket,
		       COALESCE(SUM(r.agreed_fare), 0)::float8 AS revenue
		FROM series s
		LEFT JOIN rides r
		  ON date_trunc('%s', r.completed_at) = s.bucket
		 AND r.status = 'COMPLETED'
		 AND r.completed_at >= $1 AND r.completed_at < $2
		GROUP BY s.bucket
		ORDER BY s.bucket
	`, bucket, bucket, bucket, step, bucket), start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RevenuePoint, 0, 32)
	for rows.Next() {
		var p RevenuePoint
		if err := rows.Scan(&p.T, &p.V); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func sumPoints(pts []RevenuePoint) float64 {
	var s float64
	for _, p := range pts {
		s += p.V
	}
	return s
}

func peakPoint(pts []RevenuePoint) *RevenuePoint {
	if len(pts) == 0 {
		return nil
	}
	best := pts[0]
	for _, p := range pts[1:] {
		if p.V > best.V {
			best = p
		}
	}
	if best.V == 0 {
		return nil
	}
	return &RevenuePoint{T: best.T, V: best.V}
}

// ── Rides time series ────────────────────────────────────────────────────────

type RidesPoint struct {
	T         time.Time `json:"t"`
	Completed int       `json:"completed"`
	Cancelled int       `json:"cancelled"`
}

type RidesSeriesSide struct {
	Label          string       `json:"label"`
	Points         []RidesPoint `json:"points"`
	TotalCompleted int          `json:"totalCompleted"`
	TotalCancelled int          `json:"totalCancelled"`
}

type RidesSeries struct {
	Bucket   string          `json:"bucket"`
	Current  RidesSeriesSide `json:"current"`
	Previous RidesSeriesSide `json:"previous"`
	DeltaPct float64         `json:"deltaPct"`
}

// RidesSeries returns hourly/daily ride counts for the requested window
// plus the equivalent previous window.
func (s *Service) RidesSeries(ctx context.Context, w Window) (*RidesSeries, error) {
	curStart, curEnd, prevStart, prevEnd, bucket, curLabel, prevLabel := resolveWindows(w)

	current, err := s.bucketRides(ctx, curStart, curEnd, bucket)
	if err != nil {
		return nil, err
	}
	previous, err := s.bucketRides(ctx, prevStart, prevEnd, bucket)
	if err != nil {
		return nil, err
	}

	currentSide := RidesSeriesSide{Label: curLabel, Points: current,
		TotalCompleted: sumRides(current, true), TotalCancelled: sumRides(current, false)}
	previousSide := RidesSeriesSide{Label: prevLabel, Points: previous,
		TotalCompleted: sumRides(previous, true), TotalCancelled: sumRides(previous, false)}

	var delta float64
	prevTotal := float64(previousSide.TotalCompleted)
	curTotal := float64(currentSide.TotalCompleted)
	if prevTotal > 0 {
		delta = ((curTotal - prevTotal) / prevTotal) * 100
	} else if curTotal > 0 {
		delta = 100
	}

	return &RidesSeries{
		Bucket:   bucket,
		Current:  currentSide,
		Previous: previousSide,
		DeltaPct: delta,
	}, nil
}

func sumRides(pts []RidesPoint, completed bool) int {
	var s int
	for _, p := range pts {
		if completed {
			s += p.Completed
		} else {
			s += p.Cancelled
		}
	}
	return s
}

func (s *Service) bucketRides(ctx context.Context, start, end time.Time, bucket string) ([]RidesPoint, error) {
	if !end.After(start) {
		return []RidesPoint{}, nil
	}
	step := "1 day"
	if bucket == "hour" {
		step = "1 hour"
	}
	rows, err := s.db.Query(ctx, fmt.Sprintf(`
		WITH series AS (
			SELECT generate_series(date_trunc('%s', $1::timestamptz),
			                       date_trunc('%s', $2::timestamptz - INTERVAL '1 %s'),
			                       INTERVAL '%s') AS bucket
		)
		SELECT s.bucket,
		       COUNT(r.id) FILTER (WHERE r.status='COMPLETED') AS completed,
		       COUNT(r.id) FILTER (WHERE r.status='CANCELLED') AS cancelled
		FROM series s
		LEFT JOIN rides r
		  ON date_trunc('%s', r.created_at) = s.bucket
		 AND r.created_at >= $1 AND r.created_at < $2
		GROUP BY s.bucket
		ORDER BY s.bucket
	`, bucket, bucket, bucket, step, bucket), start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RidesPoint, 0, 32)
	for rows.Next() {
		var p RidesPoint
		if err := rows.Scan(&p.T, &p.Completed, &p.Cancelled); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ── Driver status snapshot ───────────────────────────────────────────────────

type DriverStatus struct {
	Online  int `json:"online"`  // online and not currently on a trip
	OnTrip  int `json:"onTrip"`  // currently assigned to an in-flight ride
	Offline int `json:"offline"` // marked offline (among ACTIVE drivers)
}

// In-flight statuses — anything after a driver is matched/assigned but not
// terminal. SEARCHING and MATCHED have no driver yet.
const inFlightStatusList = `('CONFIRMED','DRIVER_EN_ROUTE','DRIVER_ARRIVED','IN_PROGRESS')`

func (s *Service) DriverStatusSnapshot(ctx context.Context) (*DriverStatus, error) {
	out := &DriverStatus{}

	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(DISTINCT driver_id) FROM rides
		WHERE driver_id IS NOT NULL AND status IN `+inFlightStatusList,
	).Scan(&out.OnTrip)

	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM driver_profiles dp
		WHERE dp.is_online = TRUE
		  AND dp.approval_status = 'APPROVED'
		  AND NOT EXISTS (
		    SELECT 1 FROM rides r
		    WHERE r.driver_id = dp.id AND r.status IN `+inFlightStatusList+`
		  )
	`).Scan(&out.Online)

	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM driver_profiles
		WHERE is_online = FALSE AND approval_status = 'APPROVED'
	`).Scan(&out.Offline)

	return out, nil
}

// ── Top drivers ──────────────────────────────────────────────────────────────

type TopDriver struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Rides    int     `json:"rides"`
	IsOnline bool    `json:"isOnline"`
	Earnings float64 `json:"earnings"`
}

// TopDrivers returns the drivers with the most completed rides in the window.
func (s *Service) TopDrivers(ctx context.Context, w Window, limit int) ([]TopDriver, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	curStart, curEnd, _, _, _, _, _ := resolveWindows(w)

	rows, err := s.db.Query(ctx, `
		SELECT dp.id,
		       COALESCE(NULLIF(TRIM(u.full_name), ''), 'Driver') AS name,
		       dp.is_online,
		       COUNT(r.id) FILTER (WHERE r.status='COMPLETED') AS rides,
		       COALESCE(SUM(r.agreed_fare) FILTER (WHERE r.status='COMPLETED'), 0)::float8 AS earnings
		FROM driver_profiles dp
		JOIN users u ON u.id = dp.user_id
		LEFT JOIN rides r ON r.driver_id = dp.id
		  AND r.completed_at >= $1 AND r.completed_at < $2
		WHERE dp.approval_status = 'APPROVED'
		GROUP BY dp.id, u.full_name, dp.is_online
		HAVING COUNT(r.id) FILTER (WHERE r.status='COMPLETED') > 0
		ORDER BY rides DESC, dp.id
		LIMIT $3
	`, curStart, curEnd, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]TopDriver, 0, limit)
	for rows.Next() {
		var d TopDriver
		if err := rows.Scan(&d.ID, &d.Name, &d.IsOnline, &d.Rides, &d.Earnings); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── Live map ─────────────────────────────────────────────────────────────────

type LiveMapDriver struct {
	ID       string  `json:"id"`
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	IsOnline bool    `json:"isOnline"`
	OnTrip   bool    `json:"onTrip"`
}

type LiveMapHeatPoint struct {
	Lat    float64 `json:"lat"`
	Lng    float64 `json:"lng"`
	Weight int     `json:"weight"`
}

type LiveMap struct {
	UpdatedAt     time.Time          `json:"updatedAt"`
	ActiveRides   int                `json:"activeRides"`
	OnlineDrivers int                `json:"onlineDrivers"`
	HotZones      int                `json:"hotZones"`
	Drivers       []LiveMapDriver    `json:"drivers"`
	HeatPoints    []LiveMapHeatPoint `json:"heatPoints"`
}

// LiveMap returns everything needed to render the operations map:
// active-ride / online-driver counts, recent driver positions, and a
// pickup-density heatmap for the last 2 hours.
func (s *Service) LiveMap(ctx context.Context) (*LiveMap, error) {
	out := &LiveMap{
		UpdatedAt:  time.Now().UTC(),
		Drivers:    []LiveMapDriver{},
		HeatPoints: []LiveMapHeatPoint{},
	}

	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM rides
		WHERE status NOT IN ('COMPLETED','CANCELLED','SEARCHING')
	`).Scan(&out.ActiveRides)

	_ = s.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM driver_profiles
		WHERE is_online = TRUE AND approval_status = 'APPROVED'
	`).Scan(&out.OnlineDrivers)

	// Driver positions — last 15 minutes only, capped to 200
	rows, err := s.db.Query(ctx, `
		SELECT dp.id, dp.is_online,
		       ST_Y(dl.location::geometry) AS lat,
		       ST_X(dl.location::geometry) AS lng,
		       EXISTS(
		         SELECT 1 FROM rides r
		         WHERE r.driver_id = dp.id AND r.status IN `+inFlightStatusList+`
		       ) AS on_trip
		FROM driver_profiles dp
		JOIN driver_locations dl ON dl.driver_id = dp.id
		WHERE dp.approval_status = 'APPROVED'
		  AND dl.updated_at > NOW() - INTERVAL '15 minutes'
		LIMIT 200
	`)
	if err == nil {
		for rows.Next() {
			var d LiveMapDriver
			if err := rows.Scan(&d.ID, &d.IsOnline, &d.Lat, &d.Lng, &d.OnTrip); err == nil {
				out.Drivers = append(out.Drivers, d)
			}
		}
		rows.Close()
	}

	// Heat points — last 2 hours of ride origins, ~500m grid
	heatRows, err := s.db.Query(ctx, `
		WITH grid AS (
		  SELECT ST_SnapToGrid(pickup_point::geometry, 0.005) AS cell,
		         pickup_point::geometry AS pt
		  FROM rides
		  WHERE created_at >= NOW() - INTERVAL '2 hours'
		)
		SELECT ST_Y(ST_Centroid(ST_Collect(pt))) AS lat,
		       ST_X(ST_Centroid(ST_Collect(pt))) AS lng,
		       COUNT(*)::int AS weight
		FROM grid
		GROUP BY cell
		ORDER BY weight DESC
		LIMIT 30
	`)
	if err == nil {
		for heatRows.Next() {
			var hp LiveMapHeatPoint
			if err := heatRows.Scan(&hp.Lat, &hp.Lng, &hp.Weight); err == nil {
				out.HeatPoints = append(out.HeatPoints, hp)
			}
		}
		heatRows.Close()
	}

	// Hot zones — count of pickup clusters above threshold
	for _, hp := range out.HeatPoints {
		if hp.Weight >= 3 {
			out.HotZones++
		}
	}

	return out, nil
}

// ── Recent activity ──────────────────────────────────────────────────────────

type RecentActivity struct {
	ID         int64           `json:"id"`
	Type       string          `json:"type"`
	ActorRole  string          `json:"actorRole,omitempty"`
	ActorName  string          `json:"actorName,omitempty"`
	RideID     *string         `json:"rideId,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	OccurredAt time.Time       `json:"occurredAt"`
}

// RecentActivityFilter supports cursor-based pagination + optional event-type filter.
type RecentActivityFilter struct {
	Limit    int    // 1..100, default 10
	BeforeID int64  // 0 = newest; otherwise only return rows with id < BeforeID
	Type     string // "" = all
}

func (s *Service) RecentActivity(ctx context.Context, f RecentActivityFilter) ([]RecentActivity, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	args := []interface{}{limit}
	q := `
		SELECT ae.id, ae.event_type, COALESCE(ae.actor_role,'') AS actor_role,
		       COALESCE(NULLIF(TRIM(u.full_name), ''), '') AS actor_name,
		       ae.ride_id, ae.payload, ae.occurred_at
		FROM analytics_events ae
		LEFT JOIN users u ON u.id = ae.actor_id
		WHERE TRUE`
	if f.BeforeID > 0 {
		args = append(args, f.BeforeID)
		q += fmt.Sprintf(" AND ae.id < $%d", len(args))
	}
	if f.Type != "" {
		args = append(args, f.Type)
		q += fmt.Sprintf(" AND ae.event_type = $%d", len(args))
	}
	q += " ORDER BY ae.occurred_at DESC LIMIT $1"

	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RecentActivity, 0, limit)
	for rows.Next() {
		var a RecentActivity
		var rideID *string
		if err := rows.Scan(&a.ID, &a.Type, &a.ActorRole, &a.ActorName, &rideID, &a.Payload, &a.OccurredAt); err != nil {
			return nil, err
		}
		a.RideID = rideID
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── Alerts ───────────────────────────────────────────────────────────────────

type Alert struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // "incident" | "ticket"
	Tone       string    `json:"tone"` // "danger" | "warn" | "info"
	Title      string    `json:"title"`
	Detail     string    `json:"detail"`
	Severity   string    `json:"severity,omitempty"`
	RideID     *string   `json:"rideId,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
}

// AlertFilter narrows the alert set. Kind = "" returns both kinds.
type AlertFilter struct {
	Limit int
	Kind  string // "" | "incident" | "ticket"
}

func (s *Service) Alerts(ctx context.Context, f AlertFilter) ([]Alert, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	out := make([]Alert, 0, limit)

	includeIncidents := f.Kind == "" || f.Kind == "incident"
	includeTickets := f.Kind == "" || f.Kind == "ticket"

	if includeIncidents {
		incs, err := s.queryIncidents(ctx, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, incs...)
	}

	if includeTickets {
		tickets, err := s.queryTickets(ctx, limit)
		if err != nil {
			// Partial result is OK — return what we have.
			return out, nil
		}
		out = append(out, tickets...)
	}

	// Sort merged list by occurredAt desc, truncate to limit
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].OccurredAt.After(out[j-1].OccurredAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Service) queryIncidents(ctx context.Context, limit int) ([]Alert, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, type, severity, COALESCE(description,'') AS description,
		       COALESCE(location_text,'') AS location_text, ride_id, reported_at
		FROM safety_incidents
		WHERE status IN ('OPEN','ACKNOWLEDGED','ESCALATED')
		ORDER BY reported_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Alert, 0, limit)
	for rows.Next() {
		var id, typ, sev, desc, loc string
		var rideID *string
		var t time.Time
		if err := rows.Scan(&id, &typ, &sev, &desc, &loc, &rideID, &t); err != nil {
			return nil, err
		}
		details := []string{}
		if rideID != nil && *rideID != "" {
			details = append(details, "Ride #"+shortID(*rideID))
		}
		if loc != "" {
			details = append(details, loc)
		} else if desc != "" {
			details = append(details, truncate(desc, 60))
		}
		out = append(out, Alert{
			ID:         id,
			Kind:       "incident",
			Tone:       toneForSeverity(sev),
			Title:      titleForIncidentType(typ),
			Detail:     joinDetails(details),
			Severity:   sev,
			RideID:     rideID,
			OccurredAt: t,
		})
	}
	return out, rows.Err()
}

func (s *Service) queryTickets(ctx context.Context, limit int) ([]Alert, error) {
	rows, err := s.db.Query(ctx, `
		SELECT id::text, subject, type, priority, ride_id, created_at
		FROM support_tickets
		WHERE status IN ('OPEN','PENDING')
		  AND priority IN ('HIGH','URGENT')
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Alert, 0, limit)
	for rows.Next() {
		var id, subject, typ, pri string
		var rideID *string
		var t time.Time
		if err := rows.Scan(&id, &subject, &typ, &pri, &rideID, &t); err != nil {
			return nil, err
		}
		details := []string{truncate(subject, 80)}
		if rideID != nil && *rideID != "" {
			details = append(details, "Trip #"+shortID(*rideID))
		}
		out = append(out, Alert{
			ID:         id,
			Kind:       "ticket",
			Tone:       tonePriorityFallback(pri),
			Title:      titleForTicketType(typ),
			Detail:     joinDetails(details),
			Severity:   pri,
			RideID:     rideID,
			OccurredAt: t,
		})
	}
	return out, rows.Err()
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func joinDetails(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " · "
		}
		out += p
	}
	return out
}

func titleForIncidentType(t string) string {
	switch t {
	case "SOS":
		return "Emergency: SOS triggered"
	case "ASSAULT":
		return "Assault reported"
	case "ACCIDENT":
		return "Accident reported"
	case "HARASSMENT":
		return "Harassment reported"
	case "UNSAFE_DRIVING":
		return "Unsafe driving reported"
	default:
		return "Safety incident"
	}
}

func titleForTicketType(t string) string {
	switch t {
	case "COMPLAINT":
		return "Driver complaint"
	case "PAYMENT":
		return "Payment issue"
	case "ACCOUNT":
		return "Account issue"
	case "TECHNICAL":
		return "Technical issue"
	default:
		return "Support request"
	}
}

func toneForSeverity(sev string) string {
	switch sev {
	case "CRITICAL", "HIGH":
		return "danger"
	case "MEDIUM":
		return "warn"
	default:
		return "info"
	}
}

func tonePriorityFallback(p string) string {
	if p == "URGENT" {
		return "danger"
	}
	return "warn"
}

// warmPeriods are the windows pre-computed at startup and on each poll tick.
var warmPeriods = []int{1, 7, 30}

// InvalidateCache forces a fresh computation on the next request for all
// pre-warmed periods. Custom periods bypass the cache, so nothing to evict.
func (s *Service) InvalidateCache(ctx context.Context) {
	for _, d := range warmPeriods {
		s.redis.Del(ctx, fmt.Sprintf("%s:%d", rkeys.K.DashboardCache(), d))
	}
}

// WarmCache pre-computes and stores snapshots for the standard windows.
func (s *Service) WarmCache(ctx context.Context) {
	for _, d := range warmPeriods {
		snap, err := s.compute(ctx, Window{Days: d})
		if err != nil {
			s.log.Warn().Err(err).Int("days", d).Msg("dashboard: warm cache failed")
			continue
		}
		if raw, err := json.Marshal(snap); err == nil {
			s.redis.Set(ctx, fmt.Sprintf("%s:%d", rkeys.K.DashboardCache(), d), raw, cacheTTL)
		}
	}
	s.log.Info().Msg("dashboard: cache warmed")
}

// PollLoop refreshes the dashboard cache every 10 seconds in the background.
func (s *Service) PollLoop(ctx context.Context) {
	ticker := time.NewTicker(cacheTTL)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, d := range warmPeriods {
				snap, err := s.compute(ctx, Window{Days: d})
				if err != nil {
					s.log.Warn().Err(err).Int("days", d).Msg("dashboard: poll failed")
					continue
				}
				if raw, err := json.Marshal(snap); err == nil {
					s.redis.Set(ctx, fmt.Sprintf("%s:%d", rkeys.K.DashboardCache(), d), raw, cacheTTL)
				}
			}
		}
	}
}
