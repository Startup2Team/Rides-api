// Package nationalid is the single authoritative place for national-ID
// format validation, normalization, and display-masking (DB-1).
//
// The database (migration 080) only enforces a lenient backstop CHECK
// (`^[A-Z0-9]{5,16}$`) so it never rejects a valid future-country ID — the
// real, country-specific format lives here in Go, where it can grow to new
// countries without a migration. Every caller that captures or edits a
// national ID (driver onboarding, admin-on-behalf create, admin edit) MUST
// run Normalize then Validate before writing, so the value stored is always
// in the one canonical shape the per-country UNIQUE index relies on — an
// un-normalized duplicate ("1234 5678 9012 3456" vs "12345678901234 56")
// would otherwise slip past the index and defeat the one-ID-one-account
// fraud guard this column exists for.
package nationalid

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Sentinel errors — compare with errors.Is. Kept distinct so callers can map
// each to a different client-facing message/code.
var (
	ErrCountryRequired    = errors.New("national_id_country is required")
	ErrNumberRequired     = errors.New("national_id_number is required")
	ErrUnsupportedCountry = errors.New("unsupported national_id_country")
	ErrInvalidFormat      = errors.New("invalid national_id_number format for this country")
)

// patterns is the country-aware format table. RW = 16 digits (national ID
// card number). UG = 14-character alphanumeric NIN. Add new countries here —
// nowhere else needs to change.
var patterns = map[string]*regexp.Regexp{
	"RW": regexp.MustCompile(`^\d{16}$`),
	"UG": regexp.MustCompile(`^[A-Z0-9]{14}$`),
}

// Normalize strips whitespace and dashes and upper-cases both the number and
// the country code. Call this before Validate and before every write — it is
// what keeps "12 345-678901234 5" and "123456789012345" from being treated as
// two different IDs by the UNIQUE index.
func Normalize(country, number string) (normCountry, normNumber string) {
	normCountry = strings.ToUpper(strings.TrimSpace(country))
	n := strings.ToUpper(strings.TrimSpace(number))
	n = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(n)
	return normCountry, n
}

// Validate checks an already-normalized (country, number) pair against the
// country's format. Returns one of the sentinel errors above (wrapped with
// %w, so errors.Is still matches) on failure.
func Validate(country, number string) error {
	if country == "" {
		return ErrCountryRequired
	}
	if number == "" {
		return ErrNumberRequired
	}
	pattern, ok := patterns[country]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnsupportedCountry, country)
	}
	if !pattern.MatchString(number) {
		return fmt.Errorf("%w: %s", ErrInvalidFormat, country)
	}
	return nil
}

// SupportedCountries reports whether country (already upper-cased) has a
// known format. Exposed so callers can give a clean "unsupported country"
// error before even reaching Validate, if they want to branch on it.
func SupportedCountries() []string {
	out := make([]string, 0, len(patterns))
	for c := range patterns {
		out = append(out, c)
	}
	return out
}

// Mask redacts a national ID number down to its last 4 characters, e.g.
// "1234567890123456" -> "************3456". Safe for logs, audit metadata,
// and any list/summary view. The unredacted number is only ever returned to:
//   - the driver themself (internal/driver FindProfileByUserID — it's their
//     own ID, looked up by their own user id);
//   - SuperAdmin/OpsManager viewing the admin driver-detail payload
//     (internal/admin GetDriver — SupportStaff and every other role get this
//     Mask applied instead).
//
// Every other surface (admin audit log, server logs, the matching engine's
// internal FindProfileByID, driver lists) uses this.
func Mask(number string) string {
	if number == "" {
		return ""
	}
	if len(number) <= 4 {
		return strings.Repeat("*", len(number))
	}
	return strings.Repeat("*", len(number)-4) + number[len(number)-4:]
}
