//go:build integration

package dbit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/workspace/ride-platform/internal/auth"
	"github.com/workspace/ride-platform/internal/wallet"
)

// A wallet is auto-created by a DB trigger (migration 038) when a user is
// inserted, so creating a user is enough to get a zero-balance wallet.
func newUserWithWallet(t *testing.T) (userID, phone string) {
	t.Helper()
	ctx := context.Background()
	phone = uniquePhone()
	u, err := auth.NewRepository(pool).CreateUser(ctx, phone, "dev-"+phone, "android", nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, u.ID)
	return u.ID, phone
}

func TestWallet_TopUpWithdrawAndInsufficientFunds(t *testing.T) {
	ctx := context.Background()
	repo := wallet.NewRepository(pool)
	userID, phone := newUserWithWallet(t)

	// Trigger created a wallet at zero.
	w, err := repo.GetByUserID(ctx, userID)
	require.NoError(t, err)
	require.EqualValues(t, 0, w.BalanceRWF)

	// Top up 1000 → balance 1000.
	tx, err := repo.TopUp(ctx, userID, 1000, phone, "ext-topup", "test top up")
	require.NoError(t, err)
	require.EqualValues(t, 1000, tx.BalanceAfter)

	// Withdraw 400 → balance 600.
	_, err = repo.Withdraw(ctx, userID, 400, phone, "ext-wd", "test withdraw")
	require.NoError(t, err)
	w, err = repo.GetByUserID(ctx, userID)
	require.NoError(t, err)
	require.EqualValues(t, 600, w.BalanceRWF)

	// Over-withdraw must be rejected AND must not move the balance.
	_, err = repo.Withdraw(ctx, userID, 10_000, phone, "ext-over", "too much")
	require.Error(t, err, "withdrawing more than the balance must fail")
	w, err = repo.GetByUserID(ctx, userID)
	require.NoError(t, err)
	require.EqualValues(t, 600, w.BalanceRWF, "failed withdraw must not change balance")

	// Exactly two completed transactions were recorded (top up + withdraw).
	txs, err := repo.ListTransactions(ctx, userID, 50, 0)
	require.NoError(t, err)
	require.Len(t, txs, 2)
}

func TestWallet_UnknownUserIsNotFound(t *testing.T) {
	_, err := wallet.NewRepository(pool).GetByUserID(context.Background(),
		"00000000-0000-0000-0000-000000000000")
	require.Error(t, err, "a user with no wallet row must return an error, not a zero wallet")
}
