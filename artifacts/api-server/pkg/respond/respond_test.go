package respond_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
	"github.com/workspace/ride-platform/pkg/respond"
)

func TestSuccessResponsesUseEnvelope(t *testing.T) {
	w := httptest.NewRecorder()
	respond.Created(w, map[string]string{"id": "ride-1"})

	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var body struct {
		Data map[string]string `json:"data"`
	}
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, "ride-1", body.Data["id"])
}

func TestNoContentWrites204WithoutBody(t *testing.T) {
	w := httptest.NewRecorder()
	respond.NoContent(w)

	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.String())
}

func TestErrorResponses(t *testing.T) {
	t.Run("app error", func(t *testing.T) {
		w := httptest.NewRecorder()
		respond.Error(w, apperrors.ErrNotFound)

		assert.Equal(t, http.StatusNotFound, w.Code)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
		assert.Equal(t, "NOT_FOUND", body.Error.Code)
	})

	t.Run("unknown error", func(t *testing.T) {
		w := httptest.NewRecorder()
		respond.Error(w, errors.New("boom"))

		assert.Equal(t, http.StatusInternalServerError, w.Code)
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
		assert.Equal(t, "INTERNAL", body.Error.Code)
	})
}
