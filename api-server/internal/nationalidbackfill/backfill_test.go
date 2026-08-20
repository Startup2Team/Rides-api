package nationalidbackfill

import (
	"errors"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/pkg/nationalid"
)

var rwFormat = regexp.MustCompile(`^\d{16}$`)

func TestGenerateRWNationalID_MatchesRWFormat(t *testing.T) {
	// Generate a batch and confirm every value satisfies both the raw regex
	// and pkg/nationalid.Validate("RW", ...) — the same check the driver and
	// admin capture paths run on a real driver's number, so a backfilled
	// placeholder is indistinguishable in shape from a genuine one.
	for i := 0; i < 200; i++ {
		id, err := GenerateRWNationalID()
		require.NoError(t, err)
		assert.True(t, rwFormat.MatchString(id), "generated id %q does not match ^\\d{16}$", id)
		assert.NoError(t, nationalid.Validate("RW", id))
	}
}

func TestGenerateRWNationalID_Uniqueish(t *testing.T) {
	// Not a proof of uniqueness (impossible for a PRNG in a unit test), just a
	// smoke test that the generator isn't returning a constant or a narrow
	// range — a bug that would make the retry-on-collision loop in Run spin
	// uselessly against uq_users_national_id.
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		id, err := GenerateRWNationalID()
		require.NoError(t, err)
		seen[id] = true
	}
	assert.Greater(t, len(seen), 190, "generated ids collided far more often than a 16-digit random space should")
}

func TestIsNationalIDConflict(t *testing.T) {
	assert.True(t, IsNationalIDConflict(&pgconn.PgError{Code: "23505", ConstraintName: "uq_users_national_id"}))
	assert.False(t, IsNationalIDConflict(&pgconn.PgError{Code: "23505", ConstraintName: "some_other_constraint"}),
		"a 23505 on a different constraint must not match")
	assert.False(t, IsNationalIDConflict(errors.New("some other error")))
	assert.False(t, IsNationalIDConflict(nil))
}
