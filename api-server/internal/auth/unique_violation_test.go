package auth

import (
	"errors"
	"testing"
)

// isUniqueViolation decides whether a repo error is a Postgres unique-constraint
// collision. VerifyPhoneChange relies on it to map a racing phone-number claim
// to a friendly 409 PHONE_TAKEN instead of a raw 500. If this ever stops
// recognising the "23505" SQLSTATE, two users could silently collide — so we
// pin both the positive (23505 / "unique") and negative cases.
func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not a violation", nil, false},
		{"sqlstate 23505", errors.New(`ERROR: duplicate key value violates unique constraint "users_phone_number_key" (SQLSTATE 23505)`), true},
		{"lowercase unique keyword", errors.New("unique constraint failed"), true},
		{"unrelated error", errors.New("connection refused"), false},
		{"not-found is not a violation", errors.New("no rows in result set"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUniqueViolation(tc.err); got != tc.want {
				t.Errorf("isUniqueViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
