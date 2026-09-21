package intercity

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The per-booking cap is PROPORTIONAL: 4 seats is a whole cab but a quarter of
// a Coaster. The DB CHECK is only the structural bound (1..30) because a CHECK
// cannot see the parent trip, so this function is the real rule.
func TestMaxSeatsPerBooking(t *testing.T) {
	cases := []struct {
		totalSeats int
		want       int
	}{
		{1, 1}, // a 1-seat vehicle must still be bookable
		{2, 1},
		{3, 1},
		{4, 2},  // cab: no single account takes the whole car
		{8, 4},  // Hiace
		{16, 8}, // the absolute ceiling engages
		{18, 8}, // Coaster: half a bus is already too much
		{30, 8},
		{0, 1}, // defensive: never returns 0, which would reject every booking
	}
	for _, c := range cases {
		assert.Equal(t, c.want, maxSeatsPerBooking(c.totalSeats),
			"total_seats=%d", c.totalSeats)
	}
}

// REGRESSION. acquireSeatsSQL asserts `booked + held + seats <= sellable`, so
// sellable is an absolute cap on OCCUPANCY. Feeding it the remaining-seats form
// (min(total - booked - held, balance)) double-counts the seats already sold
// and refuses every booking after the first: with total=8, booked=4 and a
// balance of 100 the predicate becomes 4 + seats <= 4.
func TestSellableOccupancy_IsAnOccupancyCapNotRemainingSeats(t *testing.T) {
	// Plenty of credits: the vehicle's own capacity is the only limit.
	assert.Equal(t, 8, sellableOccupancy(8, 100))
	// Exactly enough.
	assert.Equal(t, 8, sellableOccupancy(8, 8))
	// Thin balance clamps below capacity.
	assert.Equal(t, 3, sellableOccupancy(8, 3))
	// Empty balance sells nothing — and the customer still only ever sees
	// SEATS_UNAVAILABLE, never the number.
	assert.Equal(t, 0, sellableOccupancy(8, 0))

	// The property that matters: with 4 seats already sold and credits to
	// spare, another 4 must still fit.
	const total, booked, balance = 8, 4, 100
	sellable := sellableOccupancy(total, balance)
	assert.True(t, booked+4 <= sellable,
		"a healthy driver must still be able to sell the second half of the vehicle")
}

func TestValidateFreeText_AcceptsOrdinaryText(t *testing.T) {
	v, err := validateFreeText("staging_address", "  Nyabugogo Taxi Park, Gate 3  ", maxStagingAddrLen)
	assert.NoError(t, err)
	assert.Equal(t, "Nyabugogo Taxi Park, Gate 3", v, "must be trimmed")

	// Numbers that are not phone numbers must survive: prices, seats, years.
	_, err = validateFreeText("staging_address", "Stand 12, bay 4 — 5000 RWF per seat", maxStagingAddrLen)
	assert.NoError(t, err)
}

// Free text is advertising space unless bounded. A driver who can write
// "cheaper if you call me on 0788…" pulls passengers off-platform, taking the
// trip's accountability and the credit charge with them.
func TestValidateFreeText_RejectsContactDetails(t *testing.T) {
	bad := []string{
		"Call me 0788123456",
		"cheaper direct: 0788 123 456",
		"+250-788-123-456 for a discount",
		"whatsapp 250788123456",
		"book at www.cheaprides.rw",
		"https://example.com/book",
		"dm me @quickbus",
		"mail me at bus@gmail.com",
	}
	for _, s := range bad {
		_, err := validateFreeText("staging_address", s, maxStagingAddrLen)
		assert.Error(t, err, "must reject %q", s)
	}
}

func TestValidateFreeText_BoundsLengthAndRequiresContent(t *testing.T) {
	_, err := validateFreeText("origin_name", "   ", maxPlaceNameLen)
	assert.Error(t, err, "blank is not a place name")

	long := make([]rune, maxPlaceNameLen+1)
	for i := range long {
		long[i] = 'a'
	}
	_, err = validateFreeText("origin_name", string(long), maxPlaceNameLen)
	assert.Error(t, err, "over the cap")
}

// Enough for a driver to confirm the passenger at the gate; not enough to
// build a marketable list of verified numbers.
func TestMaskPhone(t *testing.T) {
	assert.Equal(t, "07•• ••• •56", maskPhone("+250788123456"))
	assert.Equal(t, "07•• ••• •56", maskPhone("250788123456"))
	assert.Equal(t, "07•• ••• •56", maskPhone("0788123456"))
	assert.Equal(t, "07•• ••• •56", maskPhone("078 812 3456"))
	assert.Equal(t, "", maskPhone("12"), "unparseable shows nothing, never a guessable stub")
	assert.NotContains(t, maskPhone("+250788123456"), "8123", "the middle must not survive")
}

// §9's ladder, decided from the SERVER's clock and the trip's own state.
func TestManifestPhoneTier(t *testing.T) {
	depart := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

	// At booking — well before lockout.
	assert.Equal(t, phoneMasked,
		manifestPhoneTier(depart.Add(-4*time.Hour), depart, nil, TripOpen))

	// Inside the lockout window but the trip has NOT been moved to BOARDING:
	// still masked. The transition is clock-derived server-side, so a driver
	// cannot unmask early by declaring themselves boarding.
	assert.Equal(t, phoneMasked,
		manifestPhoneTier(depart.Add(-10*time.Minute), depart, nil, TripOpen))

	// From depart_at - 30min with the trip BOARDING.
	assert.Equal(t, phoneFull,
		manifestPhoneTier(depart.Add(-lockoutLead), depart, nil, TripBoarding))
	assert.Equal(t, phoneFull,
		manifestPhoneTier(depart.Add(-time.Minute), depart, nil, TripBoarding))

	// One second before lockout is still masked.
	assert.Equal(t, phoneMasked,
		manifestPhoneTier(depart.Add(-lockoutLead-time.Second), depart, nil, TripBoarding))

	// After completed_at + 24h the driver keeps no phone access at all — and
	// that beats the BOARDING rule, whatever the trip row still says.
	completed := depart.Add(3 * time.Hour)
	assert.Equal(t, phoneNone,
		manifestPhoneTier(completed.Add(25*time.Hour), depart, &completed, TripBoarding))
	assert.Equal(t, phoneFull,
		manifestPhoneTier(completed.Add(23*time.Hour), depart, &completed, TripBoarding))
}

func TestManifestPhone_ReleasesFullNumbersToConfirmedOnly(t *testing.T) {
	const real = "+250788123456"

	assert.Equal(t, real, manifestPhone(phoneFull, BookingConfirmed, real))
	// A passenger who has already boarded needs no call.
	assert.Equal(t, "07•• ••• •56", manifestPhone(phoneFull, BookingBoarded, real))
	// A hold is not a passenger yet.
	assert.Equal(t, "07•• ••• •56", manifestPhone(phoneFull, BookingHeld, real))
	assert.Equal(t, "07•• ••• •56", manifestPhone(phoneMasked, BookingConfirmed, real))
	assert.Equal(t, "", manifestPhone(phoneNone, BookingConfirmed, real))
}

func TestFirstName(t *testing.T) {
	assert.Equal(t, "Aline", firstName("Aline Uwase"))
	assert.Equal(t, "Aline", firstName("  Aline  "))
	assert.Equal(t, "Passenger", firstName(""), "a nameless account is still a passenger")
}
