package matching

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// ── Second-chance reset: silent drivers freed, decliners stay excluded ─────

func TestSecondChance_ReleasesSilentKeepsDecliners(t *testing.T) {
	tried := map[string]bool{
		"silent-1":  true,
		"silent-2":  true,
		"decliner":  true,
		"decliner2": true,
	}
	declined := map[string]bool{
		"decliner":  true,
		"decliner2": true,
	}

	cleared := releaseSilentTried(tried, declined)

	assert.Equal(t, 2, cleared, "both silent drivers must be freed")
	assert.False(t, tried["silent-1"], "silent driver must be offerable again")
	assert.False(t, tried["silent-2"], "silent driver must be offerable again")
	assert.True(t, tried["decliner"], "an explicit decliner must stay excluded")
	assert.True(t, tried["decliner2"], "an explicit decliner must stay excluded")
}

func TestSecondChance_AllDeclinedClearsNothing(t *testing.T) {
	tried := map[string]bool{"a": true, "b": true}
	declined := map[string]bool{"a": true, "b": true}

	cleared := releaseSilentTried(tried, declined)

	// Nothing to re-offer — the caller uses cleared == 0 to fall through to the
	// wave sleep rather than spinning on an empty reset.
	assert.Equal(t, 0, cleared)
	assert.True(t, tried["a"])
	assert.True(t, tried["b"])
}

func TestSecondChance_EmptyPool(t *testing.T) {
	assert.Equal(t, 0, releaseSilentTried(map[string]bool{}, map[string]bool{}))
}
