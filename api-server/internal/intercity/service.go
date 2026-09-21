package intercity

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/internal/packages"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/geo"
)

// Service enforces the rules the Repository deliberately does not: the ones a
// CHECK constraint cannot see (they need the parent trip, the customer's other
// bookings, or the driver's credit balance) and the ones that are policy rather
// than data integrity.
//
// Its SQL lives here rather than in repository.go for one reason only: that
// file is owned by the seat-inventory work in flight. Every statement below
// still follows the house rule — the OWNERSHIP PREDICATE IS IN THE WHERE
// CLAUSE, never a SELECT followed by a check (ride/repository.go:370-383) —
// and should move into the repository once the two strands merge.
type Service struct {
	repo *Repository

	// ledger is *packages.LedgerService CONCRETELY, never an interface.
	// packages.Service.HasCredits has an identical signature but reads the
	// ORPHANED driver_ride_credits table, so an interface here would silently
	// accept the wrong credit system — a mistake this codebase has already
	// made once (see cmd/server/main.go:250).
	ledger *packages.LedgerService

	ws     TripBroadcaster
	notify PassengerNotifier
	log    zerolog.Logger
}

// TripBroadcaster fans a message out to everyone watching a trip. Expressed in
// primitive types so package tracking can satisfy it without this package
// importing tracking (mirrors driver.WSNotifier).
type TripBroadcaster interface {
	NotifyTrip(tripID, msgType string, payload map[string]interface{})
}

// PassengerNotifier is the FCM seam. A no-show is driver-asserted and cannot be
// verified server-side (§9), so the passenger must always learn of it — that is
// what makes the mark disputable.
type PassengerNotifier interface {
	SendToAllDevices(ctx context.Context, userID, title, body, nType string, data map[string]string)
}

func NewService(repo *Repository, ledger *packages.LedgerService, log zerolog.Logger) *Service {
	return &Service{repo: repo, ledger: ledger, log: log}
}

func (s *Service) SetBroadcaster(b TripBroadcaster) { s.ws = b }
func (s *Service) SetNotifier(n PassengerNotifier)  { s.notify = n }
func (s *Service) broadcast(tripID, t string, p map[string]interface{}) {
	if s.ws != nil {
		s.ws.NotifyTrip(tripID, t, p)
	}
}

// ── Policy constants ─────────────────────────────────────────────────────────

const (
	// maxSeatsPerBookingAbs and the halving rule below implement
	// LEAST(8, GREATEST(1, total_seats / 2)): no single account may take a
	// whole vehicle. The DB CHECK is only the structural bound (1..30) because
	// a CHECK has no access to the parent trip's total_seats.
	maxSeatsPerBookingAbs = 8

	// maxActiveBookingsPerCustomer caps concurrent live bookings across trips.
	// Holds are free (there is no payment step), so without this one account
	// can blanket a corridor and strand every departure on it.
	maxActiveBookingsPerCustomer = 2

	// maxOpenTripsPerDriver rate-limits publishing. A plausible trip at an
	// attractive price is a phone-harvesting instrument (§9); capping open
	// trips bounds how many a single driver can run at once.
	maxOpenTripsPerDriver = 5

	// maxDepartHorizon matches the Phase-1 scope: dates beyond +7 days are out.
	maxDepartHorizon = 7 * 24 * time.Hour
	// minDepartLead keeps a published trip bookable for at least one hold TTL.
	minDepartLead = 15 * time.Minute

	// lockoutLead mirrors availability.LockoutLead as a Go duration. Both
	// describe the same instant: depart_at - 30min, when the manifest unmasks
	// and the driver leaves city dispatch.
	lockoutLead = 30 * time.Minute

	// manifestPhoneRetention is how long after completion the driver keeps any
	// phone access at all.
	manifestPhoneRetention = 24 * time.Hour
)

// Free-text length caps. Driver-authored strings are shown to passengers, so
// they are advertising space unless bounded.
const (
	maxPlaceNameLen    = 80
	maxStagingAddrLen  = 200
	maxCancelReasonLen = 200
)

// ── Errors ───────────────────────────────────────────────────────────────────

var (
	// ErrSeatCap is the per-booking proportional cap.
	ErrSeatCap = apperrors.New(http.StatusUnprocessableEntity, "SEAT_CAP_EXCEEDED",
		"that is more seats than one booking may take on this vehicle")
	// ErrTooManyBookings is the anti-abuse concurrency cap.
	ErrTooManyBookings = apperrors.New(http.StatusConflict, "TOO_MANY_BOOKINGS",
		"you already have the maximum number of active intercity bookings")
	// ErrNoCredits gates publishing. 402: the driver must top up.
	ErrNoCredits = apperrors.New(http.StatusPaymentRequired, "NO_CREDITS",
		"you need ride credits to publish an intercity trip")
	// ErrSeatsUnavailableHTTP is the single, deliberately vague answer to every
	// failed seat acquisition. Distinguishing the causes would leak a rival
	// driver's inventory and credit balance.
	ErrSeatsUnavailableHTTP = apperrors.New(http.StatusConflict, "SEATS_UNAVAILABLE",
		"those seats are no longer available")
	// ErrDuplicateDeparture is uq_intercity_trip_driver_slot surfacing as a 409
	// instead of a 500.
	ErrDuplicateDeparture = apperrors.New(http.StatusConflict, "DUPLICATE_DEPARTURE",
		"you already have a trip departing at that time")
	// ErrTripLimit is the open-trip publishing cap.
	ErrTripLimit = apperrors.New(http.StatusConflict, "TRIP_LIMIT",
		"you already have the maximum number of open trips")
	// ErrVehicleNotIntercityEligible refuses a publish from a vehicle too small
	// to run a corridor (MinIntercityVehicleSeats). 422 and not 400: the
	// request is well-formed, the vehicle is simply the wrong one, and the
	// message says so — retrying changes nothing, registering a bigger vehicle
	// does.
	ErrVehicleNotIntercityEligible = apperrors.Newf(http.StatusUnprocessableEntity,
		"VEHICLE_NOT_INTERCITY_ELIGIBLE",
		"intercity trips need a vehicle seating at least %d passengers — a cab, Hilux, Hiace or bus. "+
			"A moto or tuk-tuk cannot be used for intercity travel.", MinIntercityVehicleSeats)
	// ErrTripFrozen is the PATCH whitelist refusing a frozen field.
	ErrTripFrozen = apperrors.New(http.StatusConflict, "TRIP_FROZEN",
		"this trip has passengers: only the boarding point and extra seats can change")
	// ErrTripNotFoundHTTP maps a missing/foreign trip to 404. Missing and
	// not-yours are indistinguishable so the endpoint cannot probe for ids.
	ErrTripNotFoundHTTP = apperrors.New(http.StatusNotFound, "TRIP_NOT_FOUND", "trip not found")
	// ErrBookingNotFoundHTTP is the same for bookings.
	ErrBookingNotFoundHTTP = apperrors.New(http.StatusNotFound, "BOOKING_NOT_FOUND", "booking not found")
)

func validationErr(format string, args ...interface{}) error {
	return apperrors.Newf(http.StatusBadRequest, "VALIDATION", format, args...)
}

// ── Pure policy helpers (unit-tested directly) ───────────────────────────────

// maxSeatsPerBooking is LEAST(8, GREATEST(1, total_seats / 2)).
//
// The cap is proportional, not absolute: 4 seats is a whole cab but a quarter
// of a Coaster, and a family of five on an 18-seater is an ordinary booking.
// The 8 ceiling stops one account from taking half a bus.
func maxSeatsPerBooking(totalSeats int) int {
	half := totalSeats / 2
	if half < 1 {
		half = 1
	}
	if half > maxSeatsPerBookingAbs {
		half = maxSeatsPerBookingAbs
	}
	return half
}

// sellableOccupancy is the value handed to Repository.Hold as `sellable`.
//
// READ THIS BEFORE CHANGING IT. acquireSeatsSQL asserts
// `booked_seats + held_seats + $2 <= $3`, i.e. $3 is an absolute cap on the
// trip's OCCUPANCY, not on the seats remaining. The design prose (§11 D3)
// writes the clamp as `min(total - booked - held, balance)`, which is the
// remaining-seats form of the same idea; substituting that directly into an
// occupancy predicate double-counts the seats already sold and would refuse
// every booking after the first. The occupancy form of "a driver may not sell
// more seats than their credit balance can cover at departure" is
// min(total_seats, balance), and that is what this returns.
//
// The result is NEVER returned to a caller: publishing the clamped number is an
// exact live oracle of a rival driver's credit balance, and it would let one
// account blackhole a low-balance driver's vehicle. A clamp failure returns the
// same generic SEATS_UNAVAILABLE as a genuine sell-out.
func sellableOccupancy(totalSeats, driverBalance int) int {
	if driverBalance < totalSeats {
		return driverBalance
	}
	return totalSeats
}

var (
	// phoneRun matches any run of 7+ digits once separators are stripped: a
	// Rwandan mobile is 10 digits, so this catches "0788 123 456" and
	// "+250-788-123456" while leaving prices, seat counts and years alone.
	phoneRun = regexp.MustCompile(`\d{7,}`)
	phoneSep = regexp.MustCompile(`[\s\-().+/]`)
	// urlish matches a scheme, a www host, a bare domain, or an @handle.
	urlish = regexp.MustCompile(`(?i)(https?://|www\.|\.(com|net|org|rw|io|co|me|app|biz|info)\b|@)`)
)

// validateFreeText bounds a driver-authored string and rejects contact details.
//
// Without this, `staging_address` becomes advertising space — "cheaper if you
// call me on 078…" — and every passenger the platform acquires can be pulled
// off it at zero cost, taking the trip's accountability and the credit charge
// with them.
func validateFreeText(field, value string, maxLen int) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", validationErr("%s is required", field)
	}
	if len([]rune(v)) > maxLen {
		return "", validationErr("%s must be at most %d characters", field, maxLen)
	}
	if phoneRun.MatchString(phoneSep.ReplaceAllString(v, "")) {
		return "", validationErr("%s cannot contain a phone number", field)
	}
	if urlish.MatchString(v) {
		return "", validationErr("%s cannot contain a link or contact handle", field)
	}
	return v, nil
}

// maskPhone renders +250788123456 as "07•• ••• •56".
//
// Enough for a driver to confirm they are talking to the right passenger at the
// gate; not enough to build a marketable list of verified numbers of people
// known to be travelling on a given date.
func maskPhone(phone string) string {
	var digits []rune
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			digits = append(digits, r)
		}
	}
	// Normalise to the 10-digit Rwandan local form (0 + 9 digits) when we can.
	local := string(digits)
	if len(local) > 10 {
		local = "0" + local[len(local)-9:]
	}
	if len(local) < 4 {
		return "" // unparseable: show nothing rather than something guessable
	}
	return local[:2] + "•• ••• •" + local[len(local)-2:]
}

// phoneTier is the manifest PII ladder (§9).
type phoneTier int

const (
	phoneMasked phoneTier = iota // at booking: first name, seat count, masked phone
	phoneFull                    // from depart_at-30min while BOARDING: CONFIRMED only
	phoneNone                    // after completed_at + 24h: nothing
)

// manifestPhoneTier decides the tier from the SERVER's clock and the trip's own
// state — never from a client hint.
func manifestPhoneTier(now time.Time, departAt time.Time, completedAt *time.Time, tripStatus string) phoneTier {
	if completedAt != nil && now.After(completedAt.Add(manifestPhoneRetention)) {
		return phoneNone
	}
	if tripStatus == TripBoarding && !now.Before(departAt.Add(-lockoutLead)) {
		return phoneFull
	}
	return phoneMasked
}

// manifestPhone applies the tier to one row. Full numbers are released for
// CONFIRMED bookings only: a passenger who has already boarded needs no call,
// and one who cancelled is no longer the driver's business.
func manifestPhone(tier phoneTier, bookingStatus, phone string) string {
	switch {
	case tier == phoneNone:
		return ""
	case tier == phoneFull && bookingStatus == BookingConfirmed:
		return phone
	default:
		return maskPhone(phone)
	}
}

// firstName takes the leading token of a full name, so the manifest identifies
// a passenger to their driver without publishing their full identity.
func firstName(full string) string {
	f := strings.TrimSpace(strings.SplitN(strings.TrimSpace(full), " ", 2)[0])
	if f == "" {
		return "Passenger"
	}
	return f
}

// ── Read models ──────────────────────────────────────────────────────────────

// TripView is what a customer sees. It carries SeatsAvailable
// (total - booked - held) and never the credit-clamped sellable number, and it
// never carries a driver phone: driver contact belongs only to a confirmed
// passenger, at the gate.
type TripView struct {
	ID                 string     `json:"id"`
	Corridor           string     `json:"corridor"`
	OriginName         string     `json:"origin_name"`
	DestinationName    string     `json:"destination_name"`
	StagingAddress     string     `json:"staging_address"`
	StagingLat         *float64   `json:"staging_lat,omitempty"`
	StagingLng         *float64   `json:"staging_lng,omitempty"`
	DepartAt           time.Time  `json:"depart_at"`
	PricePerSeatRWF    int        `json:"price_per_seat_rwf"`
	TotalSeats         int        `json:"total_seats"`
	SeatsAvailable     int        `json:"seats_available"`
	MaxSeatsPerBooking int        `json:"max_seats_per_booking"`
	Status             string     `json:"status"`
	DriverFirstName    string     `json:"driver_first_name"`
	OperatorName       string     `json:"operator_name"`
	VehicleType        string     `json:"vehicle_type"`
	VehiclePlate       string     `json:"vehicle_plate"`
	CancelReason       *string    `json:"cancel_reason,omitempty"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}

// BookingView is a passenger's own booking.
type BookingView struct {
	ID              string     `json:"id"`
	TripID          string     `json:"trip_id"`
	Seats           int        `json:"seats"`
	Status          string     `json:"status"`
	PricePerSeatRWF int        `json:"price_per_seat_rwf"`
	TotalRWF        int        `json:"total_rwf"`
	HoldExpiresAt   *time.Time `json:"hold_expires_at,omitempty"`
	Trip            *TripView  `json:"trip,omitempty"`
}

// ManifestEntry is one passenger as the driver sees them. Phone is tiered and
// there are no per-passenger coordinates: Phase 1 has a single staging address
// on purpose.
type ManifestEntry struct {
	BookingID string     `json:"booking_id"`
	FirstName string     `json:"first_name"`
	Seats     int        `json:"seats"`
	Status    string     `json:"status"`
	Phone     string     `json:"phone,omitempty"`
	BoardedAt *time.Time `json:"boarded_at,omitempty"`
	NoShowAt  *time.Time `json:"no_show_at,omitempty"`
}

// Manifest is the driver's view of one trip's passengers.
type Manifest struct {
	TripID        string          `json:"trip_id"`
	Status        string          `json:"status"`
	DepartAt      time.Time       `json:"depart_at"`
	TotalSeats    int             `json:"total_seats"`
	BookedSeats   int             `json:"booked_seats"`
	HeldSeats     int             `json:"held_seats"`
	PhonesVisible bool            `json:"phones_visible"`
	Passengers    []ManifestEntry `json:"passengers"`
}

// ── Customer: browse ─────────────────────────────────────────────────────────

const tripSelectCols = `
	t.id, t.corridor, t.origin_name, t.destination_name, t.staging_address,
	ST_Y(t.staging_point::geometry), ST_X(t.staging_point::geometry),
	t.depart_at, t.price_per_seat_rwf, t.total_seats,
	t.total_seats - t.booked_seats - t.held_seats,
	t.status, t.cancel_reason, t.completed_at,
	COALESCE(u.full_name, ''), o.display_name, vt.code, dv.plate_number`

const tripJoins = `
	  FROM intercity_trips t
	  JOIN driver_profiles dp ON dp.id = t.driver_id
	  JOIN users u            ON u.id = dp.user_id
	  JOIN intercity_operators o ON o.id = t.operator_id
	  JOIN driver_vehicles dv ON dv.id = t.vehicle_id
	  JOIN vehicle_types vt   ON vt.id = dv.vehicle_type_id`

// searchTripsSQL filters ?date= as a RANGE in Africa/Kigali.
//
// `depart_at::date = $2` would be evaluated in the container's timezone (UTC),
// so a 01:00 Kigali departure lands on the PREVIOUS UTC date and vanishes from
// its own day in the results. The half-open range below converts the local
// calendar day to two instants, which is also sargable against
// idx_intercity_trips_search. Precedent: analytics/repository.go:312.
const searchTripsSQL = `
	SELECT ` + tripSelectCols + tripJoins + `
	 WHERE t.deleted_at IS NULL
	   AND t.status = 'OPEN'
	   AND t.depart_at > NOW() + interval '5 minutes'
	   AND ($1::text IS NULL OR t.corridor = $1::text)
	   AND ($2::date IS NULL OR (t.depart_at >= (($2::date)::timestamp AT TIME ZONE 'Africa/Kigali')
	                         AND t.depart_at <  (($2::date + 1)::timestamp AT TIME ZONE 'Africa/Kigali')))
	   AND t.total_seats - t.booked_seats - t.held_seats >= $3
	 ORDER BY t.depart_at
	 LIMIT $4 OFFSET $5`

// SearchTrips lists bookable departures. Paginated and never carrying a phone.
func (s *Service) SearchTrips(ctx context.Context, corridor, date string, seats, limit, offset int) ([]*TripView, error) {
	var corridorArg, dateArg *string
	if c := strings.TrimSpace(corridor); c != "" {
		corridorArg = &c
	}
	if d := strings.TrimSpace(date); d != "" {
		if _, err := time.Parse("2006-01-02", d); err != nil {
			return nil, validationErr("date must be YYYY-MM-DD")
		}
		dateArg = &d
	}
	if seats < 1 {
		seats = 1
	}

	rows, err := s.repo.db.Query(ctx, searchTripsSQL, corridorArg, dateArg, seats, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*TripView{}
	for rows.Next() {
		t, err := scanTripView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

const tripByIDSQL = `
	SELECT ` + tripSelectCols + tripJoins + `
	 WHERE t.id = $1 AND t.deleted_at IS NULL`

// GetTrip returns one trip's public detail. Not restricted to OPEN: a passenger
// holding a booking must still be able to load the trip once it starts boarding.
func (s *Service) GetTrip(ctx context.Context, tripID string) (*TripView, error) {
	t, err := scanTripView(s.repo.db.QueryRow(ctx, tripByIDSQL, tripID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTripNotFoundHTTP
	}
	if err != nil {
		return nil, err
	}
	return t, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTripView(row rowScanner) (*TripView, error) {
	var t TripView
	var fullName string
	if err := row.Scan(&t.ID, &t.Corridor, &t.OriginName, &t.DestinationName, &t.StagingAddress,
		&t.StagingLat, &t.StagingLng, &t.DepartAt, &t.PricePerSeatRWF, &t.TotalSeats,
		&t.SeatsAvailable, &t.Status, &t.CancelReason, &t.CompletedAt,
		&fullName, &t.OperatorName, &t.VehicleType, &t.VehiclePlate); err != nil {
		return nil, err
	}
	t.DriverFirstName = firstName(fullName)
	t.MaxSeatsPerBooking = maxSeatsPerBooking(t.TotalSeats)
	if t.SeatsAvailable < 0 {
		t.SeatsAvailable = 0
	}
	return &t, nil
}

// ── Customer: booking ────────────────────────────────────────────────────────

const tripForHoldSQL = `
	SELECT t.total_seats, t.booked_seats, t.held_seats, u.id, vt.code
	  FROM intercity_trips t
	  JOIN driver_profiles dp ON dp.id = t.driver_id
	  JOIN users u            ON u.id = dp.user_id
	  JOIN driver_vehicles dv ON dv.id = t.vehicle_id
	  JOIN vehicle_types vt   ON vt.id = dv.vehicle_type_id
	 WHERE t.id = $1 AND t.deleted_at IS NULL`

// countOtherActiveBookingsSQL excludes this trip deliberately: a retry of a
// hold the customer already owns must not be rejected by the concurrency cap,
// and uq_intercity_active_booking already forbids two live bookings on one trip.
const countOtherActiveBookingsSQL = `
	SELECT COUNT(*)
	  FROM intercity_bookings
	 WHERE customer_id = $1
	   AND trip_id <> $2
	   AND deleted_at IS NULL
	   AND status IN ('HELD', 'CONFIRMED', 'BOARDED')`

// HoldSeats reserves seats for HoldTTL after the checks a CHECK constraint
// cannot make: the proportional per-booking cap (needs the parent trip), the
// concurrency cap (needs the customer's other bookings), and the credit clamp
// (needs the driver's balance).
func (s *Service) HoldSeats(ctx context.Context, tripID, customerID string, seats int, idemKey string) (*BookingView, error) {
	if seats < 1 {
		return nil, validationErr("seats must be at least 1")
	}
	if strings.TrimSpace(idemKey) == "" {
		return nil, validationErr("idempotency_key is required")
	}
	if len(idemKey) > 120 {
		return nil, validationErr("idempotency_key is too long")
	}

	var total, booked, held int
	var driverUserID, vehicleTypeCode string
	err := s.repo.db.QueryRow(ctx, tripForHoldSQL, tripID).
		Scan(&total, &booked, &held, &driverUserID, &vehicleTypeCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTripNotFoundHTTP
	}
	if err != nil {
		return nil, err
	}

	if seats > maxSeatsPerBooking(total) {
		return nil, ErrSeatCap
	}

	var active int
	if err := s.repo.db.QueryRow(ctx, countOtherActiveBookingsSQL, customerID, tripID).Scan(&active); err != nil {
		return nil, err
	}
	if active >= maxActiveBookingsPerCustomer {
		return nil, ErrTooManyBookings
	}

	balance, err := s.driverBalance(ctx, driverUserID, vehicleTypeCode, total)
	if err != nil {
		return nil, err
	}

	b, err := s.repo.Hold(ctx, tripID, customerID, seats, idemKey, sellableOccupancy(total, balance))
	if errors.Is(err, ErrSeatsUnavailable) {
		return nil, ErrSeatsUnavailableHTTP
	}
	if err != nil {
		return nil, err
	}

	s.broadcastSeats(ctx, tripID)
	return bookingView(b), nil
}

// driverBalance reads the v4 entitlement balance for the trip's vehicle type.
//
// A driver on an unlimited plan is not clamped at all — capped at the vehicle's
// own capacity, which the seats_never_oversold CHECK enforces regardless.
func (s *Service) driverBalance(ctx context.Context, driverUserID, vehicleTypeCode string, totalSeats int) (int, error) {
	if s.ledger == nil {
		return 0, apperrors.ErrInternal
	}
	ents, err := s.ledger.ListEntitlementsForUser(ctx, driverUserID)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	for _, e := range ents {
		if e.VehicleTypeCode != vehicleTypeCode {
			continue
		}
		if e.UnlimitedUntil != nil && e.UnlimitedUntil.After(now) {
			return totalSeats, nil
		}
		return e.RidesRemaining + e.BonusRemaining, nil
	}
	return 0, nil
}

// ConfirmBooking turns a hold into a confirmed seat. Repeat confirms are an
// idempotent success (the seats already moved), never a 409.
func (s *Service) ConfirmBooking(ctx context.Context, bookingID, customerID string) (*BookingView, error) {
	b, err := s.repo.Confirm(ctx, bookingID, customerID)
	switch {
	case errors.Is(err, ErrBookingNotFound):
		return nil, ErrBookingNotFoundHTTP
	case errors.Is(err, ErrSeatsUnavailable):
		// The hold lapsed or was cancelled underneath the confirm.
		return nil, ErrSeatsUnavailableHTTP
	case err != nil:
		return nil, err
	}
	s.broadcastSeats(ctx, b.TripID)
	s.broadcast(b.TripID, "booking_received", map[string]interface{}{
		"booking_id": b.ID, "seats": b.Seats,
	})
	return bookingView(b), nil
}

// CancelBooking releases the seats a passenger is holding or has confirmed.
func (s *Service) CancelBooking(ctx context.Context, bookingID, customerID string) error {
	// Ownership is asserted inside cancelBookingSQL's WHERE clause; an id that
	// is not this customer's simply matches nothing and is a no-op, which is
	// also the correct answer for an already-cancelled booking.
	var tripID string
	err := s.repo.db.QueryRow(ctx,
		`SELECT trip_id FROM intercity_bookings WHERE id = $1 AND customer_id = $2 AND deleted_at IS NULL`,
		bookingID, customerID).Scan(&tripID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBookingNotFoundHTTP
	}
	if err != nil {
		return err
	}
	if err := s.repo.CancelBooking(ctx, bookingID, customerID, "CUSTOMER"); err != nil {
		return err
	}
	s.broadcastSeats(ctx, tripID)
	return nil
}

const listBookingsSQL = `
	SELECT b.id, b.trip_id, b.seats, b.status, b.price_per_seat_rwf, b.hold_expires_at,
	       ` + tripSelectCols + `
	  FROM intercity_bookings b
	  JOIN intercity_trips t  ON t.id = b.trip_id
	  JOIN driver_profiles dp ON dp.id = t.driver_id
	  JOIN users u            ON u.id = dp.user_id
	  JOIN intercity_operators o ON o.id = t.operator_id
	  JOIN driver_vehicles dv ON dv.id = t.vehicle_id
	  JOIN vehicle_types vt   ON vt.id = dv.vehicle_type_id
	 WHERE b.customer_id = $1 AND b.deleted_at IS NULL
	 ORDER BY b.created_at DESC
	 LIMIT $2 OFFSET $3`

// ListBookings returns the caller's own bookings. The customer_id predicate is
// in the WHERE clause, so there is no version of this query that can return
// somebody else's row.
func (s *Service) ListBookings(ctx context.Context, customerID string, limit, offset int) ([]*BookingView, error) {
	rows, err := s.repo.db.Query(ctx, listBookingsSQL, customerID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*BookingView{}
	for rows.Next() {
		v, err := scanBookingWithTrip(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

const bookingByIDSQL = `
	SELECT b.id, b.trip_id, b.seats, b.status, b.price_per_seat_rwf, b.hold_expires_at,
	       ` + tripSelectCols + `
	  FROM intercity_bookings b
	  JOIN intercity_trips t  ON t.id = b.trip_id
	  JOIN driver_profiles dp ON dp.id = t.driver_id
	  JOIN users u            ON u.id = dp.user_id
	  JOIN intercity_operators o ON o.id = t.operator_id
	  JOIN driver_vehicles dv ON dv.id = t.vehicle_id
	  JOIN vehicle_types vt   ON vt.id = dv.vehicle_type_id
	 WHERE b.id = $1 AND b.customer_id = $2 AND b.deleted_at IS NULL`

func (s *Service) GetBooking(ctx context.Context, bookingID, customerID string) (*BookingView, error) {
	v, err := scanBookingWithTrip(s.repo.db.QueryRow(ctx, bookingByIDSQL, bookingID, customerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrBookingNotFoundHTTP
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

func scanBookingWithTrip(row rowScanner) (*BookingView, error) {
	var v BookingView
	var t TripView
	var fullName string
	if err := row.Scan(&v.ID, &v.TripID, &v.Seats, &v.Status, &v.PricePerSeatRWF, &v.HoldExpiresAt,
		&t.ID, &t.Corridor, &t.OriginName, &t.DestinationName, &t.StagingAddress,
		&t.StagingLat, &t.StagingLng, &t.DepartAt, &t.PricePerSeatRWF, &t.TotalSeats,
		&t.SeatsAvailable, &t.Status, &t.CancelReason, &t.CompletedAt,
		&fullName, &t.OperatorName, &t.VehicleType, &t.VehiclePlate); err != nil {
		return nil, err
	}
	t.DriverFirstName = firstName(fullName)
	t.MaxSeatsPerBooking = maxSeatsPerBooking(t.TotalSeats)
	if t.SeatsAvailable < 0 {
		t.SeatsAvailable = 0
	}
	v.TotalRWF = v.Seats * v.PricePerSeatRWF
	v.Trip = &t
	return &v, nil
}

func bookingView(b *Booking) *BookingView {
	return &BookingView{
		ID:              b.ID,
		TripID:          b.TripID,
		Seats:           b.Seats,
		Status:          b.Status,
		PricePerSeatRWF: b.PricePerSeat,
		TotalRWF:        b.Seats * b.PricePerSeat,
		HoldExpiresAt:   b.HoldExpiresAt,
	}
}

// ── Driver: publish ──────────────────────────────────────────────────────────

// PublishTripInput is the driver's publish payload. driver_id is NEVER taken
// from the body — it is resolved from the JWT.
type PublishTripInput struct {
	Corridor  string
	VehicleID string
	// OriginName/DestinationName/Origin/Destination are NOT taken from the
	// caller — publish derives all four from the corridor. Kept off the input
	// struct entirely so the contract cannot quietly drift back.
	StagingAddress  string
	StagingLat      *float64
	StagingLng      *float64
	DepartAt        time.Time
	TotalSeats      int
	PricePerSeatRWF int
}

const insertTripSQL = `
	INSERT INTO intercity_trips
	    (driver_id, operator_id, vehicle_id, corridor, origin_name, destination_name,
	     origin_point, destination_point, staging_address, staging_point,
	     depart_at, total_seats, price_per_seat_rwf, status)
	VALUES ($1, $2, $3, $4, $5, $6,
	        ST_SetSRID(ST_MakePoint($7, $8), 4326)::geography,
	        ST_SetSRID(ST_MakePoint($9, $10), 4326)::geography,
	        $11,
	        CASE WHEN $12::float8 IS NULL THEN NULL
	             ELSE ST_SetSRID(ST_MakePoint($12::float8, $13::float8), 4326)::geography END,
	        $14, $15, $16, 'OPEN')
	RETURNING id`

// PublishTrip creates a departure for the signed-in driver.
//
// 402 NO_CREDITS when the balance is empty: credits are charged per chargeable
// seat at departure, so publishing with nothing to charge against would sell
// seats the platform can never bill for.
func (s *Service) PublishTrip(ctx context.Context, driverUserID string, in PublishTripInput) (*TripView, error) {
	// origin_name, destination_name and both endpoint coordinates are DERIVED
	// from the corridor (migration 101), never accepted from the caller. A
	// corridor IS a fixed pair of places: letting each driver send their own
	// would put two drivers' "Musanze" in two different spots, which is the
	// fragmentation the corridor lookup table exists to prevent — and the client
	// has no coordinates to send anyway, so publish could not succeed at all.
	stagingAddr, err := validateFreeText("staging_address", in.StagingAddress, maxStagingAddrLen)
	if err != nil {
		return nil, err
	}
	if (in.StagingLat == nil) != (in.StagingLng == nil) {
		return nil, validationErr("staging_lat and staging_lng must be supplied together")
	}
	if in.StagingLat != nil {
		if err := (geo.Point{Lat: *in.StagingLat, Lng: *in.StagingLng}).Validate(); err != nil {
			return nil, validationErr("staging coordinates are invalid")
		}
	}
	if in.PricePerSeatRWF < 1 || in.PricePerSeatRWF > 500000 {
		return nil, validationErr("price_per_seat_rwf must be between 1 and 500000")
	}
	now := time.Now()
	if in.DepartAt.Before(now.Add(minDepartLead)) {
		return nil, validationErr("depart_at must be at least %d minutes from now", int(minDepartLead.Minutes()))
	}
	if in.DepartAt.After(now.Add(maxDepartHorizon)) {
		return nil, validationErr("depart_at cannot be more than 7 days away")
	}

	// The corridor must exist and be active: `corridor` is an FK, so a bad code
	// would otherwise surface as a 23503 500 on a path no driver can self-serve.
	var originName, destName string
	var originLng, originLat, destLng, destLat float64
	if err := s.repo.db.QueryRow(ctx, `
		SELECT origin_name, destination_name,
		       ST_X(origin_point::geometry), ST_Y(origin_point::geometry),
		       ST_X(destination_point::geometry), ST_Y(destination_point::geometry)
		  FROM intercity_corridors WHERE code = $1 AND is_active`,
		in.Corridor).Scan(&originName, &destName,
		&originLng, &originLat, &destLng, &destLat); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, validationErr("unknown corridor")
		}
		return nil, err
	}

	// Vehicle ownership and capacity in ONE predicate: the vehicle must be this
	// driver's, active, and physically able to seat what is being sold.
	var profileID, vehicleTypeCode string
	var vehicleSeats *int
	var typeMaxPassengers int
	err = s.repo.db.QueryRow(ctx, `
		SELECT dp.id, vt.code, dv.passenger_seats, vt.max_passengers
		  FROM driver_vehicles dv
		  JOIN driver_profiles dp ON dp.id = dv.driver_id
		  JOIN vehicle_types vt   ON vt.id = dv.vehicle_type_id
		 WHERE dv.id = $1 AND dv.is_active = TRUE AND dp.user_id = $2`,
		in.VehicleID, driverUserID).Scan(&profileID, &vehicleTypeCode, &vehicleSeats, &typeMaxPassengers)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, validationErr("vehicle not found or not yours")
	}
	if err != nil {
		return nil, err
	}
	// The floor comes BEFORE the capacity check: a moto asking for one seat
	// satisfies `total_seats <= capacity` perfectly, and that is precisely the
	// trip that must never exist. Driver.Vehicle.IntercityEligible reports the
	// same predicate so the app can hide the screen rather than fail here.
	if !VehicleEligible(vehicleSeats, typeMaxPassengers) {
		return nil, ErrVehicleNotIntercityEligible
	}
	capacity := VehicleSeatCapacity(vehicleSeats, typeMaxPassengers)
	if in.TotalSeats < 1 || in.TotalSeats > capacity {
		return nil, validationErr("total_seats must be between 1 and %d for this vehicle", capacity)
	}

	var openTrips int
	if err := s.repo.db.QueryRow(ctx, `
		SELECT COUNT(*) FROM intercity_trips
		 WHERE driver_id = $1 AND deleted_at IS NULL AND status IN ('OPEN', 'BOARDING')`,
		profileID).Scan(&openTrips); err != nil {
		return nil, err
	}
	if openTrips >= maxOpenTripsPerDriver {
		return nil, ErrTripLimit
	}

	// Credit gate. HasCredits is read from the CONCRETE *packages.LedgerService
	// (the v4 entitlement ledger) — packages.Service.HasCredits has the same
	// signature but reads the orphaned legacy table and would wave everyone
	// through.
	if s.ledger == nil {
		return nil, apperrors.ErrInternal
	}
	hasCredits, err := s.ledger.HasCredits(ctx, driverUserID, vehicleTypeCode)
	if err != nil {
		return nil, err
	}
	if !hasCredits {
		return nil, ErrNoCredits
	}

	operatorID, err := s.resolveOperator(ctx, driverUserID, profileID)
	if err != nil {
		return nil, err
	}

	var tripID string
	err = s.repo.db.QueryRow(ctx, insertTripSQL,
		profileID, operatorID, in.VehicleID, in.Corridor, originName, destName,
		originLng, originLat, destLng, destLat,
		stagingAddr, in.StagingLng, in.StagingLat,
		in.DepartAt, in.TotalSeats, in.PricePerSeatRWF).Scan(&tripID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
			strings.Contains(pgErr.ConstraintName, "uq_intercity_trip_driver_slot") {
			return nil, ErrDuplicateDeparture
		}
		return nil, err
	}

	// A departure inside the lockout window removes the driver from city
	// dispatch immediately; one further out is a no-op today and is picked up
	// when the window opens. Deriving (rather than setting a flag) makes this
	// safe to call unconditionally.
	if err := s.repo.RecomputeDriverCommitment(ctx, profileID); err != nil {
		s.log.Error().Err(err).Str("trip_id", tripID).Msg("intercity: commitment recompute failed after publish")
	}

	return s.GetTrip(ctx, tripID)
}

// resolveOperator finds the operator this driver publishes under, creating the
// degenerate INDIVIDUAL operator on first publish.
//
// §15: everyone is an operator — an individual is an operator of one vehicle
// who is also its driver. Phase 1 publishes under the signed-in driver's own
// profile, so the roster check is "is this driver on this operator's roster",
// exactly as it will be for a company. Company owners who do not drive are
// Phase 2 (they hold no driver profile, so RoleDriverActive excludes them).
func (s *Service) resolveOperator(ctx context.Context, ownerUserID, driverProfileID string) (string, error) {
	var operatorID string
	err := s.repo.db.QueryRow(ctx, `
		SELECT o.id
		  FROM intercity_operators o
		  JOIN intercity_operator_drivers od ON od.operator_id = o.id AND od.deleted_at IS NULL
		 WHERE o.owner_user_id = $1
		   AND o.deleted_at IS NULL
		   AND o.approval_status = 'APPROVED'
		   AND od.driver_profile_id = $2
		 ORDER BY o.created_at
		 LIMIT 1`, ownerUserID, driverProfileID).Scan(&operatorID)
	if err == nil {
		return operatorID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	// First publish: provision the individual operator, mirroring migration
	// 100's backfill. The driver is already DRIVER_ACTIVE — a separately
	// reviewed operator record would gate an approved driver on a queue that
	// does not exist yet.
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	err = tx.QueryRow(ctx, `
		INSERT INTO intercity_operators (owner_user_id, display_name, kind, approval_status)
		SELECT u.id, COALESCE(NULLIF(u.full_name, ''), 'Operator'), 'INDIVIDUAL', 'APPROVED'
		  FROM users u WHERE u.id = $1
		ON CONFLICT DO NOTHING
		RETURNING id`, ownerUserID).Scan(&operatorID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Raced another publish: uq_intercity_operator_individual won.
		if err := tx.QueryRow(ctx, `
			SELECT id FROM intercity_operators
			 WHERE owner_user_id = $1 AND kind = 'INDIVIDUAL' AND deleted_at IS NULL`,
			ownerUserID).Scan(&operatorID); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO intercity_operator_drivers (operator_id, driver_profile_id)
		VALUES ($1, $2) ON CONFLICT DO NOTHING`, operatorID, driverProfileID); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return operatorID, nil
}

// ── Driver: read ─────────────────────────────────────────────────────────────

const listDriverTripsSQL = `
	SELECT ` + tripSelectCols + tripJoins + `
	 WHERE t.deleted_at IS NULL
	   AND (dp.user_id = $1 OR o.owner_user_id = $1)
	 ORDER BY t.depart_at DESC
	 LIMIT $2 OFFSET $3`

// ListDriverTrips returns the signed-in driver's trips — the ones they drive
// and the ones their operator published.
func (s *Service) ListDriverTrips(ctx context.Context, driverUserID string, limit, offset int) ([]*TripView, error) {
	rows, err := s.repo.db.Query(ctx, listDriverTripsSQL, driverUserID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*TripView{}
	for rows.Next() {
		t, err := scanTripView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// tripOwnedSQL is the ownership predicate for every driver-side trip read.
// The assigned driver OR the operator owner (§15) — both in the WHERE clause.
const tripOwnedSQL = `
	SELECT t.status, t.depart_at, t.completed_at, t.total_seats, t.booked_seats, t.held_seats
	  FROM intercity_trips t
	  JOIN driver_profiles dp    ON dp.id = t.driver_id
	  JOIN intercity_operators o ON o.id = t.operator_id
	 WHERE t.id = $1 AND t.deleted_at IS NULL
	   AND (dp.user_id = $2 OR (o.owner_user_id = $2 AND o.deleted_at IS NULL))`

const manifestRowsSQL = `
	SELECT b.id, COALESCE(u.full_name, ''), u.phone_number, b.seats, b.status,
	       b.boarded_at, b.no_show_at
	  FROM intercity_bookings b
	  JOIN users u ON u.id = b.customer_id
	 WHERE b.trip_id = $1
	   AND b.deleted_at IS NULL
	   AND b.status IN ('HELD', 'CONFIRMED', 'BOARDED', 'COMPLETED', 'NO_SHOW')
	 ORDER BY b.created_at`

// GetManifest returns the trip's passengers with phone access tiered by §9.
//
// The tier is computed from the server's clock and the trip's own state. A
// driver who publishes a plausible trip and cancels it must end up with a list
// of masked numbers, or publishing becomes a free phone-harvesting instrument.
func (s *Service) GetManifest(ctx context.Context, tripID, driverUserID string) (*Manifest, error) {
	var m Manifest
	var completedAt *time.Time
	err := s.repo.db.QueryRow(ctx, tripOwnedSQL, tripID, driverUserID).
		Scan(&m.Status, &m.DepartAt, &completedAt, &m.TotalSeats, &m.BookedSeats, &m.HeldSeats)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTripNotFoundHTTP
	}
	if err != nil {
		return nil, err
	}
	m.TripID = tripID

	tier := manifestPhoneTier(time.Now(), m.DepartAt, completedAt, m.Status)
	m.PhonesVisible = tier == phoneFull

	rows, err := s.repo.db.Query(ctx, manifestRowsSQL, tripID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m.Passengers = []ManifestEntry{}
	for rows.Next() {
		var e ManifestEntry
		var fullName, phone string
		if err := rows.Scan(&e.BookingID, &fullName, &phone, &e.Seats, &e.Status,
			&e.BoardedAt, &e.NoShowAt); err != nil {
			return nil, err
		}
		e.FirstName = firstName(fullName)
		e.Phone = manifestPhone(tier, e.Status, phone)
		m.Passengers = append(m.Passengers, e)
	}
	return &m, rows.Err()
}

// ── Driver: mutate ───────────────────────────────────────────────────────────

// PatchTripInput carries only the fields a driver may edit. Everything absent
// is left alone; everything present is checked against the freeze rule.
type PatchTripInput struct {
	StagingAddress  *string
	StagingLat      *float64
	StagingLng      *float64
	TotalSeats      *int
	OriginName      *string
	DestinationName *string
	DepartAt        *time.Time
	PricePerSeatRWF *int
}

// frozen reports whether the payload touches a field that is frozen once the
// trip has passengers.
func (in PatchTripInput) frozen() bool {
	return in.OriginName != nil || in.DestinationName != nil ||
		in.DepartAt != nil || in.PricePerSeatRWF != nil
}

// PatchTrip applies the §7 whitelist.
//
// Once booked_seats + held_seats > 0 only the boarding point and INCREASES to
// total_seats are permitted. The freeze engages on held seats too: a passenger
// mid-hold must not have the corridor, destination or departure time changed
// underneath them. Lowering total_seats below what is sold would violate
// seats_never_oversold and 500 on a legitimate driver action, so it is refused
// in the statement's own WHERE clause rather than checked beforehand.
func (s *Service) PatchTrip(ctx context.Context, tripID, driverUserID string, in PatchTripInput) (*TripView, error) {
	var stagingAddr *string
	if in.StagingAddress != nil {
		v, err := validateFreeText("staging_address", *in.StagingAddress, maxStagingAddrLen)
		if err != nil {
			return nil, err
		}
		stagingAddr = &v
	}
	if in.OriginName != nil {
		v, err := validateFreeText("origin_name", *in.OriginName, maxPlaceNameLen)
		if err != nil {
			return nil, err
		}
		in.OriginName = &v
	}
	if in.DestinationName != nil {
		v, err := validateFreeText("destination_name", *in.DestinationName, maxPlaceNameLen)
		if err != nil {
			return nil, err
		}
		in.DestinationName = &v
	}
	if (in.StagingLat == nil) != (in.StagingLng == nil) {
		return nil, validationErr("staging_lat and staging_lng must be supplied together")
	}
	if in.StagingLat != nil {
		if err := (geo.Point{Lat: *in.StagingLat, Lng: *in.StagingLng}).Validate(); err != nil {
			return nil, validationErr("staging coordinates are invalid")
		}
	}
	if in.PricePerSeatRWF != nil && (*in.PricePerSeatRWF < 1 || *in.PricePerSeatRWF > 500000) {
		return nil, validationErr("price_per_seat_rwf must be between 1 and 500000")
	}
	if in.DepartAt != nil {
		now := time.Now()
		if in.DepartAt.Before(now.Add(minDepartLead)) || in.DepartAt.After(now.Add(maxDepartHorizon)) {
			return nil, validationErr("depart_at must be between 15 minutes and 7 days from now")
		}
	}

	// The freeze is asserted in SQL, not by a prior SELECT: a hold landing
	// between a read and the write would otherwise slip through the check.
	freezeGuard := ""
	if in.frozen() {
		freezeGuard = " AND t.booked_seats + t.held_seats = 0"
	}

	const patchSQL = `
		UPDATE intercity_trips t SET
		    staging_address    = COALESCE($3, t.staging_address),
		    staging_point      = CASE WHEN $4::float8 IS NULL THEN t.staging_point
		                              ELSE ST_SetSRID(ST_MakePoint($4::float8, $5::float8), 4326)::geography END,
		    total_seats        = COALESCE($6, t.total_seats),
		    origin_name        = COALESCE($7, t.origin_name),
		    destination_name   = COALESCE($8, t.destination_name),
		    depart_at          = COALESCE($9, t.depart_at),
		    price_per_seat_rwf = COALESCE($10, t.price_per_seat_rwf),
		    updated_at         = NOW()
		  FROM driver_profiles dp, intercity_operators o
		 WHERE t.id = $1
		   AND t.deleted_at IS NULL
		   AND t.status IN ('OPEN', 'BOARDING')
		   AND dp.id = t.driver_id
		   AND o.id = t.operator_id
		   AND (dp.user_id = $2 OR (o.owner_user_id = $2 AND o.deleted_at IS NULL))
		   AND ($6::int IS NULL OR $6::int >= t.booked_seats + t.held_seats)
		   -- total_seats may never exceed the vehicle that is actually going.
		   -- PublishTrip checks this; PATCH did not, so a 4-seat cab could be
		   -- patched to 30 and sold 30 times.
		   -- Same effective capacity PublishTrip uses (VehicleSeatCapacity):
		   -- declared seats when set, else the type's catalogue capacity. A bare
		   -- dv.passenger_seats is NULL for every vehicle whose owner never
		   -- filled it in, and NULL makes this predicate false — which silently
		   -- refused a legitimate seat increase.
		   AND ($6::int IS NULL OR $6::int <= (
		         SELECT COALESCE(NULLIF(dv.passenger_seats, 0), vt.max_passengers)
		           FROM driver_vehicles dv
		           JOIN vehicle_types vt ON vt.id = dv.vehicle_type_id
		          WHERE dv.id = t.vehicle_id))
		   -- And it may not be RAISED once anything is sold. Raising it after the
		   -- sale inflated the no-show discount budget, which was a complete bill
		   -- escape before the budget was re-denominated; keeping capacity frozen
		   -- removes the lever entirely rather than relying on one fix.
		   AND ($6::int IS NULL
		        OR t.booked_seats + t.held_seats = 0
		        OR $6::int <= t.total_seats)`

	tag, err := s.repo.db.Exec(ctx, patchSQL+freezeGuard,
		tripID, driverUserID, stagingAddr, in.StagingLng, in.StagingLat,
		in.TotalSeats, in.OriginName, in.DestinationName, in.DepartAt, in.PricePerSeatRWF)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, s.classifyTripFailure(ctx, tripID, driverUserID, in.frozen())
	}

	if in.DepartAt != nil {
		if err := s.repo.LockDriverForDeparture(ctx, tripID); err != nil {
			s.log.Error().Err(err).Str("trip_id", tripID).Msg("intercity: commitment recompute failed after patch")
		}
	}
	s.broadcastSeats(ctx, tripID)
	return s.GetTrip(ctx, tripID)
}

// classifyTripFailure turns "the guarded UPDATE matched nothing" into the right
// status code. It is a NON-AUTHORITATIVE second look, used only to word the
// answer: the guarded statement above is what actually decided.
func (s *Service) classifyTripFailure(ctx context.Context, tripID, driverUserID string, frozenFields bool) error {
	var status string
	var booked, held int
	err := s.repo.db.QueryRow(ctx, `
		SELECT t.status, t.booked_seats, t.held_seats
		  FROM intercity_trips t
		  JOIN driver_profiles dp    ON dp.id = t.driver_id
		  JOIN intercity_operators o ON o.id = t.operator_id
		 WHERE t.id = $1 AND t.deleted_at IS NULL
		   AND (dp.user_id = $2 OR (o.owner_user_id = $2 AND o.deleted_at IS NULL))`,
		tripID, driverUserID).Scan(&status, &booked, &held)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTripNotFoundHTTP
	}
	if err != nil {
		return err
	}
	if frozenFields && booked+held > 0 {
		return ErrTripFrozen
	}
	if status != TripOpen && status != TripBoarding {
		return apperrors.ErrInvalidTransition
	}
	return ErrTripFrozen
}

// setTripStatusOwned moves a trip between states with the ownership predicate
// AND the from-state assertion in the same WHERE clause, then recomputes the
// driver's availability inside the SAME transaction — the invariant
// availability.SetTripStatus exists to protect.
//
// extraGuard is appended to the WHERE clause (it may reference t) and takes no
// parameters, so it can never be a source of injection.
func (s *Service) setTripStatusOwned(ctx context.Context, tripID, driverUserID, to string, from []string, extraGuard string) error {
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var driverID string
	err = tx.QueryRow(ctx, `
		UPDATE intercity_trips t
		   SET status       = $3::text,
		       started_at   = CASE WHEN $3::text = 'IN_TRANSIT' THEN NOW() ELSE t.started_at END,
		       completed_at = CASE WHEN $3::text = 'COMPLETED'  THEN NOW() ELSE t.completed_at END,
		       cancelled_at = CASE WHEN $3::text = 'CANCELLED'  THEN NOW() ELSE t.cancelled_at END,
		       updated_at   = NOW()
		  FROM driver_profiles dp, intercity_operators o
		 WHERE t.id = $1
		   AND t.deleted_at IS NULL
		   AND t.status = ANY($4::text[])
		   AND dp.id = t.driver_id
		   AND o.id = t.operator_id
		   AND (dp.user_id = $2 OR (o.owner_user_id = $2 AND o.deleted_at IS NULL))`+extraGuard+`
		RETURNING t.driver_id`, tripID, driverUserID, to, from).Scan(&driverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.classifyTransitionFailure(ctx, tripID, driverUserID)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, recomputeCommitmentSQL, driverID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) classifyTransitionFailure(ctx context.Context, tripID, driverUserID string) error {
	var owned bool
	if err := s.repo.db.QueryRow(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM intercity_trips t
		      JOIN driver_profiles dp    ON dp.id = t.driver_id
		      JOIN intercity_operators o ON o.id = t.operator_id
		     WHERE t.id = $1 AND t.deleted_at IS NULL
		       AND (dp.user_id = $2 OR (o.owner_user_id = $2 AND o.deleted_at IS NULL)))`,
		tripID, driverUserID).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return ErrTripNotFoundHTTP
	}
	return apperrors.ErrInvalidTransition
}

// StartTrip moves a trip to IN_TRANSIT.
//
// D2: depart_at is a promise — the vehicle never leaves before it, with ONE
// exception: the driver may start early iff every CONFIRMED booking has
// boarded. That is enforced as a server-side predicate, not a disabled button.
func (s *Service) StartTrip(ctx context.Context, tripID, driverUserID string) error {
	const d2Guard = `
		   AND (NOW() >= t.depart_at
		        OR NOT EXISTS (SELECT 1 FROM intercity_bookings b
		                        WHERE b.trip_id = t.id AND b.deleted_at IS NULL
		                          AND b.status = 'CONFIRMED'))`
	if err := s.setTripStatusOwned(ctx, tripID, driverUserID, TripInTransit,
		[]string{TripOpen, TripBoarding}, d2Guard); err != nil {
		return err
	}
	s.broadcast(tripID, "trip_departing", map[string]interface{}{"trip_id": tripID})
	return nil
}

// CompleteTrip closes a trip and completes the passengers who boarded.
func (s *Service) CompleteTrip(ctx context.Context, tripID, driverUserID string) error {
	if err := s.setTripStatusOwned(ctx, tripID, driverUserID, TripCompleted,
		[]string{TripInTransit}, ""); err != nil {
		return err
	}
	// Boarded passengers become COMPLETED. Counters are untouched: a completed
	// trip's inventory is history, and the charge obligation was snapshotted at
	// departure — settlement never re-derives from bookings.
	if _, err := s.repo.db.Exec(ctx, `
		UPDATE intercity_bookings SET status = 'COMPLETED', updated_at = NOW()
		 WHERE trip_id = $1 AND deleted_at IS NULL AND status = 'BOARDED'`, tripID); err != nil {
		return err
	}
	return nil
}

// CancelTrip cancels a departure and every live booking on it.
//
// D4: HELD, CONFIRMED **and** BOARDED bookings are all cancelled — leaving
// boarded passengers un-notified on a cancelled trip was a v1 defect. Only OPEN
// and BOARDING trips can be cancelled: once the credit obligation exists it is
// immutable, and there is no reversal path.
func (s *Service) CancelTrip(ctx context.Context, tripID, driverUserID, reason string) error {
	cleanReason, err := validateFreeText("cancel_reason", reason, maxCancelReasonLen)
	if err != nil {
		return err
	}

	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var driverID string
	err = tx.QueryRow(ctx, `
		UPDATE intercity_trips t
		   SET status = 'CANCELLED', cancelled_at = NOW(), cancel_reason = $3,
		       held_seats = 0, booked_seats = 0, updated_at = NOW()
		  FROM driver_profiles dp, intercity_operators o
		 WHERE t.id = $1
		   AND t.deleted_at IS NULL
		   AND t.status IN ('OPEN', 'BOARDING')
		   -- Cancellation is not an escape hatch. BOARDING is not time-bounded, so
		   -- without this a driver could fill the bus, let everyone board, drive
		   -- the route, collect the cash and THEN tap Cancel: every booking is
		   -- cancelled, the trip goes terminal, and the departure worker never
		   -- looks at it again. Whether they paid would be a race against a ticker.
		   AND t.depart_at > NOW()
		   -- And once an obligation exists it is immutable; cancelling must not
		   -- be able to orphan it.
		   AND NOT EXISTS (SELECT 1 FROM intercity_credit_charges c WHERE c.trip_id = t.id)
		   AND dp.id = t.driver_id
		   AND o.id = t.operator_id
		   AND (dp.user_id = $2 OR (o.owner_user_id = $2 AND o.deleted_at IS NULL))
		RETURNING t.driver_id`, tripID, driverUserID, cleanReason).Scan(&driverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.classifyTransitionFailure(ctx, tripID, driverUserID)
	}
	if err != nil {
		return err
	}

	rows, err := tx.Query(ctx, `
		UPDATE intercity_bookings
		   SET status = 'CANCELLED', cancelled_at = NOW(),
		       cancelled_by_role = 'DRIVER', updated_at = NOW()
		 WHERE trip_id = $1 AND deleted_at IS NULL
		   AND status IN ('HELD', 'CONFIRMED', 'BOARDED')
		RETURNING customer_id`, tripID)
	if err != nil {
		return err
	}
	var affected []string
	for rows.Next() {
		var customerID string
		if err := rows.Scan(&customerID); err != nil {
			rows.Close()
			return err
		}
		affected = append(affected, customerID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, recomputeCommitmentSQL, driverID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// The server is the source of truth, so the state change is already
	// durable; WS and FCM are how the passengers find out. Both are
	// best-effort and neither can roll the cancellation back.
	s.broadcast(tripID, "trip_cancelled", map[string]interface{}{
		"trip_id": tripID, "reason": cleanReason,
	})
	for _, customerID := range affected {
		s.notifyPassenger(ctx, customerID, "Trip cancelled",
			"Your intercity trip was cancelled: "+cleanReason,
			"INTERCITY_TRIP_CANCELLED", map[string]string{"trip_id": tripID})
	}
	return nil
}

// boardOrNoShowSQL is one statement for both driver marks.
//
// It asserts, in a single WHERE clause: the booking exists, is CONFIRMED, is
// NOT soft-deleted, BELONGS TO THIS TRIP, and the trip is this driver's and is
// BOARDING. The `b.trip_id = $2` term is not decoration — without it a driver
// mutates a booking on someone else's trip by passing a foreign booking_id.
//
// Re-asserting the trip row also serialises these marks behind the departure
// transaction's row lock, so a mark cannot race the chargeable-seat count.
const boardOrNoShowSQL = `
	UPDATE intercity_bookings b
	   SET status       = $4::text,
	       boarded_at   = CASE WHEN $4::text = 'BOARDED' THEN NOW() ELSE b.boarded_at END,
	       no_show_at   = CASE WHEN $4::text = 'NO_SHOW' THEN NOW() ELSE b.no_show_at END,
	       marked_by_driver_id = CASE WHEN $4::text = 'NO_SHOW' THEN dp.id ELSE b.marked_by_driver_id END,
	       updated_at   = NOW()
	  FROM intercity_trips t, driver_profiles dp, intercity_operators o
	 WHERE b.id = $1
	   AND b.trip_id = $2
	   AND b.deleted_at IS NULL
	   AND b.status = 'CONFIRMED'
	   AND t.id = b.trip_id
	   AND t.deleted_at IS NULL
	   AND t.status = 'BOARDING'
	   AND dp.id = t.driver_id
	   AND o.id = t.operator_id
	   AND (dp.user_id = $3 OR (o.owner_user_id = $3 AND o.deleted_at IS NULL))
	RETURNING b.customer_id, b.seats`

// BoardPassenger marks a confirmed booking as boarded.
func (s *Service) BoardPassenger(ctx context.Context, tripID, bookingID, driverUserID string) error {
	if err := s.promoteToBoardingIfDue(ctx, tripID, driverUserID); err != nil {
		return err
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var customerID string
	var seats int
	err = tx.QueryRow(ctx, boardOrNoShowSQL, bookingID, tripID, driverUserID, BookingBoarded).
		Scan(&customerID, &seats)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.classifyMarkFailure(ctx, tripID, bookingID, driverUserID)
	}
	if err != nil {
		return err
	}
	// Counters are untouched: a boarded seat is still occupied, it has simply
	// moved along the booking state machine.
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.broadcast(tripID, "booking_boarded", map[string]interface{}{"seats": seats})
	return nil
}

// MarkNoShow records a driver-asserted no-show.
//
// The platform cannot verify it (the city flow checks driver proximity;
// intercity has no equivalent signal), so the mark records who made it, when,
// notifies the passenger so it is disputable, and increments a counter that
// blocks nobody. The seat is NOT released: a driver must not be able to
// mark-then-resell.
func (s *Service) MarkNoShow(ctx context.Context, tripID, bookingID, driverUserID string) error {
	if err := s.promoteToBoardingIfDue(ctx, tripID, driverUserID); err != nil {
		return err
	}
	tx, err := s.repo.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var customerID string
	var seats int
	err = tx.QueryRow(ctx, boardOrNoShowSQL, bookingID, tripID, driverUserID, BookingNoShow).
		Scan(&customerID, &seats)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.classifyMarkFailure(ctx, tripID, bookingID, driverUserID)
	}
	if err != nil {
		return err
	}
	// Same transaction as the status change, never outside it (§3, migration 098).
	if _, err := tx.Exec(ctx,
		`UPDATE customer_profiles SET no_show_count = no_show_count + 1, updated_at = NOW()
		  WHERE user_id = $1`, customerID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	s.notifyPassenger(ctx, customerID, "Marked as no-show",
		"The driver marked you as a no-show for your intercity trip. Contact support if this is wrong.",
		"INTERCITY_NO_SHOW", map[string]string{"trip_id": tripID, "booking_id": bookingID})
	s.broadcast(tripID, "booking_no_show", map[string]interface{}{"seats": seats})
	return nil
}

// promoteToBoardingIfDue moves an OPEN trip into BOARDING once the server's
// clock passes depart_at - 30min.
//
// INTERIM. The departure worker of §11 D3 owns this transition (it also
// snapshots the chargeable seats), and that worker is not built yet. Without
// this, board and no-show — which both re-assert `t.status = 'BOARDING'` —
// could never succeed. It is clock-derived and never driver-declared: a driver
// cannot open the boarding window early to unmask the manifest, which is
// exactly the property §9 depends on.
func (s *Service) promoteToBoardingIfDue(ctx context.Context, tripID, driverUserID string) error {
	_, err := s.repo.db.Exec(ctx, `
		UPDATE intercity_trips t
		   SET status = 'BOARDING', updated_at = NOW()
		  FROM driver_profiles dp, intercity_operators o
		 WHERE t.id = $1
		   AND t.deleted_at IS NULL
		   AND t.status = 'OPEN'
		   AND NOW() >= t.depart_at - INTERVAL '`+LockoutLead+`'
		   AND dp.id = t.driver_id
		   AND o.id = t.operator_id
		   AND (dp.user_id = $2 OR (o.owner_user_id = $2 AND o.deleted_at IS NULL))`,
		tripID, driverUserID)
	return err
}

// classifyMarkFailure words the answer after a guarded mark matched no row.
// Non-authoritative: only the statement above decided anything.
func (s *Service) classifyMarkFailure(ctx context.Context, tripID, bookingID, driverUserID string) error {
	var owned bool
	if err := s.repo.db.QueryRow(ctx, `
		SELECT EXISTS(
		    SELECT 1 FROM intercity_trips t
		      JOIN driver_profiles dp    ON dp.id = t.driver_id
		      JOIN intercity_operators o ON o.id = t.operator_id
		     WHERE t.id = $1 AND t.deleted_at IS NULL
		       AND (dp.user_id = $2 OR (o.owner_user_id = $2 AND o.deleted_at IS NULL)))`,
		tripID, driverUserID).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return ErrTripNotFoundHTTP
	}
	// The trip is ours, so the failure is about the booking: it is not on this
	// trip, does not exist, or is not CONFIRMED. All three answer 404/409
	// without revealing which — a foreign booking_id must not be confirmable
	// as "exists, but elsewhere".
	var eligible bool
	if err := s.repo.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM intercity_bookings
		               WHERE id = $1 AND trip_id = $2 AND deleted_at IS NULL)`,
		bookingID, tripID).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return ErrBookingNotFoundHTTP
	}
	return apperrors.ErrInvalidTransition
}

func (s *Service) notifyPassenger(ctx context.Context, customerID, title, body, nType string, data map[string]string) {
	if s.notify == nil {
		return
	}
	s.notify.SendToAllDevices(ctx, customerID, title, body, nType, data)
}

// ── Realtime ─────────────────────────────────────────────────────────────────

// CanWatchTrip authorises a trip WebSocket subscription SERVER-SIDE.
//
// A live booking on the trip, the assigned driver, or the operator owner —
// nothing else. Without this every authenticated user could subscribe to any
// trip id and receive a live feed of a rival's bookings.
func (s *Service) CanWatchTrip(ctx context.Context, tripID, userID string) (bool, error) {
	var ok bool
	err := s.repo.db.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM intercity_bookings b
		               WHERE b.trip_id = $1 AND b.customer_id = $2
		                 AND b.deleted_at IS NULL
		                 AND b.status IN ('HELD', 'CONFIRMED', 'BOARDED'))
		    OR EXISTS(SELECT 1 FROM intercity_trips t
		                JOIN driver_profiles dp    ON dp.id = t.driver_id
		                JOIN intercity_operators o ON o.id = t.operator_id
		               WHERE t.id = $1 AND t.deleted_at IS NULL
		                 AND (dp.user_id = $2 OR (o.owner_user_id = $2 AND o.deleted_at IS NULL)))`,
		tripID, userID).Scan(&ok)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// broadcastSeats emits trip_seats_changed with COUNTS ONLY.
//
// Never passenger identities: the watcher set includes every other passenger on
// the trip, and a rival driver can hold one seat to join it.
func (s *Service) broadcastSeats(ctx context.Context, tripID string) {
	if s.ws == nil {
		return
	}
	var total, booked, held int
	var status string
	if err := s.repo.db.QueryRow(ctx, `
		SELECT total_seats, booked_seats, held_seats, status
		  FROM intercity_trips WHERE id = $1 AND deleted_at IS NULL`,
		tripID).Scan(&total, &booked, &held, &status); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.log.Error().Err(err).Str("trip_id", tripID).Msg("intercity: seat broadcast read failed")
		}
		return
	}
	available := total - booked - held
	if available < 0 {
		available = 0
	}
	s.ws.NotifyTrip(tripID, "trip_seats_changed", map[string]interface{}{
		"trip_id":         tripID,
		"total_seats":     total,
		"seats_available": available,
		"status":          status,
	})
	if available == 0 {
		s.ws.NotifyTrip(tripID, "trip_full", map[string]interface{}{"trip_id": tripID})
	}
}

// Corridor is a bookable route. Corridors are a lookup table rather than free
// text so that "Kigali", "kigali" and "Kigali " cannot become three different
// routes — a passenger searching a corridor that HAS trips would otherwise
// silently see an empty list.
type Corridor struct {
	Code            string `json:"code"`
	OriginName      string `json:"origin_name"`
	DestinationName string `json:"destination_name"`
}

// ListCorridors returns the routes a passenger may search, and the routes a
// driver may publish on.
//
// Without this endpoint the corridor picker — the first screen of the flow —
// has nothing to show, and every screen behind it is unreachable: `corridor` is
// NOT NULL and REFERENCES intercity_corridors(code), so the client cannot
// invent one. It is unauthenticated-shaped data (no user content, no PII) but
// sits inside the authenticated group like the rest of the surface.
func (s *Service) ListCorridors(ctx context.Context) ([]Corridor, error) {
	rows, err := s.repo.db.Query(ctx, `
		SELECT code, origin_name, destination_name
		  FROM intercity_corridors
		 WHERE is_active = TRUE
		 ORDER BY origin_name, destination_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Corridor, 0, 8)
	for rows.Next() {
		var c Corridor
		if err := rows.Scan(&c.Code, &c.OriginName, &c.DestinationName); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
