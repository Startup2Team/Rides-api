package nationalid_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/workspace/ride-platform/pkg/nationalid"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name        string
		country     string
		number      string
		wantCountry string
		wantNumber  string
	}{
		{"already clean RW", "rw", "1234567890123456", "RW", "1234567890123456"},
		{"spaces and dashes RW", "rw", "1234 5678-9012 3456", "RW", "1234567890123456"},
		{"lowercase UG", "ug", "ab12cd34ef56gh", "UG", "AB12CD34EF56GH"},
		{"underscores", " rw ", "1234_5678_9012_3456", "RW", "1234567890123456"},
		{"empty", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotCountry, gotNumber := nationalid.Normalize(c.country, c.number)
			assert.Equal(t, c.wantCountry, gotCountry)
			assert.Equal(t, c.wantNumber, gotNumber)
		})
	}
}

func TestValidate_RW(t *testing.T) {
	valid := []string{"1234567890123456", "0000000000000000"}
	for _, n := range valid {
		assert.NoError(t, nationalid.Validate("RW", n), n)
	}

	invalid := []string{
		"123456789012345",   // 15 digits — too short
		"12345678901234567", // 17 digits — too long
		"123456789012345A",  // letter not allowed
		"",                  // empty
	}
	for _, n := range invalid {
		err := nationalid.Validate("RW", n)
		assert.Error(t, err, n)
	}
}

func TestValidate_UG(t *testing.T) {
	valid := []string{"AB12CD34EF56GH", "12345678901234"}
	for _, n := range valid {
		assert.NoError(t, nationalid.Validate("UG", n), n)
	}

	invalid := []string{
		"AB12CD34EF56G",   // 13 chars — too short
		"AB12CD34EF56GHI", // 15 chars — too long
		"ab12cd34ef56gh",  // lowercase (Normalize should have upper-cased it first)
		"AB12-CD34EF56G",  // punctuation (Normalize should have stripped it first)
	}
	for _, n := range invalid {
		err := nationalid.Validate("UG", n)
		assert.Error(t, err, n)
	}
}

func TestValidate_RequiresBothFields(t *testing.T) {
	err := nationalid.Validate("", "1234567890123456")
	assert.True(t, errors.Is(err, nationalid.ErrCountryRequired))

	err = nationalid.Validate("RW", "")
	assert.True(t, errors.Is(err, nationalid.ErrNumberRequired))
}

func TestValidate_UnsupportedCountry(t *testing.T) {
	err := nationalid.Validate("KE", "1234567890123456")
	assert.True(t, errors.Is(err, nationalid.ErrUnsupportedCountry))
}

func TestValidate_NormalizeThenValidate_RoundTrip(t *testing.T) {
	// The exact flow every caller must run: normalize what the user typed,
	// then validate the normalized form.
	country, number := nationalid.Normalize("rw", "1234 5678 9012 3456")
	assert.NoError(t, nationalid.Validate(country, number))

	country, number = nationalid.Normalize("ug", "ab12-cd34-ef56-gh")
	assert.NoError(t, nationalid.Validate(country, number))
}

func TestMask(t *testing.T) {
	assert.Equal(t, "************3456", nationalid.Mask("1234567890123456"))
	assert.Equal(t, "**********56GH", nationalid.Mask("AB12CD34EF56GH"))
	assert.Equal(t, "", nationalid.Mask(""))
	assert.Equal(t, "****", nationalid.Mask("1234")) // len == 4: fully redacted, no 4 digits to safely reveal
	assert.Equal(t, "***", nationalid.Mask("123"))
}
