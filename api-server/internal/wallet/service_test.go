package wallet

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

func TestTopUp_DisabledToPreventMinting(t *testing.T) {
	// GIVEN a service with paymentsEnabled = true/false (nil repository)
	s := NewService(nil, zerolog.Nop(), true)

	// WHEN TopUp is called
	tx, err := s.TopUp(context.Background(), "user-123", 5000, "0788888888")

	// THEN it must return the PAYMENTS_DISABLED 501 error and never proceed
	assert.Nil(t, tx)
	assert.Error(t, err)
	var appErr *apperrors.AppError
	assert.True(t, errors.As(err, &appErr))
	assert.Equal(t, http.StatusNotImplemented, appErr.StatusCode)
	assert.Equal(t, "PAYMENTS_DISABLED", appErr.Code)
}

func TestWithdraw_DisabledToPreventCashOut(t *testing.T) {
	// GIVEN a service (nil repository)
	s := NewService(nil, zerolog.Nop(), true)

	// WHEN Withdraw is called
	tx, err := s.Withdraw(context.Background(), "user-123", 5000, "0788888888")

	// THEN it must return the PAYMENTS_DISABLED 501 error
	assert.Nil(t, tx)
	assert.Error(t, err)
	var appErr *apperrors.AppError
	assert.True(t, errors.As(err, &appErr))
	assert.Equal(t, http.StatusNotImplemented, appErr.StatusCode)
	assert.Equal(t, "PAYMENTS_DISABLED", appErr.Code)
}
