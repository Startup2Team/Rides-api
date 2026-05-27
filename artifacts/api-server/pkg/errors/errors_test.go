package errors_test

import (
	stderrors "errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

func TestAppErrorFormattingAndComparison(t *testing.T) {
	err := apperrors.New(http.StatusTeapot, "TEAPOT", "short and stout")

	assert.Equal(t, "[TEAPOT] short and stout", err.Error())
	assert.True(t, stderrors.Is(err, apperrors.New(http.StatusBadRequest, "TEAPOT", "different message")))
	assert.False(t, stderrors.Is(err, apperrors.ErrBadRequest))
}

func TestNewfFormatsMessage(t *testing.T) {
	err := apperrors.Newf(http.StatusConflict, "ACTIVE_RIDE", "ride %s is active", "ride-1")

	assert.Equal(t, http.StatusConflict, err.StatusCode)
	assert.Equal(t, "ACTIVE_RIDE", err.Code)
	assert.Equal(t, "ride ride-1 is active", err.Message)
}
