package wallet

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/workspace/ride-platform/pkg/errors"
)

// Repository is the only type allowed to touch wallet DB rows.
// All balance mutations run inside a serialisable transaction with
// SELECT … FOR UPDATE to prevent concurrent double-spends.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

// GetByUserID returns the wallet for a user, or ErrNotFound.
func (r *Repository) GetByUserID(ctx context.Context, userID string) (*Wallet, error) {
	var w Wallet
	err := r.db.QueryRow(ctx,
		`SELECT id, user_id, balance_rwf, created_at, updated_at
		   FROM wallets WHERE user_id = $1`, userID,
	).Scan(&w.ID, &w.UserID, &w.BalanceRWF, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperrors.ErrNotFound
	}
	return &w, err
}

// ListTransactions returns the most recent transactions for a user.
func (r *Repository) ListTransactions(ctx context.Context, userID string, limit, offset int) ([]*Transaction, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, wallet_id, user_id, type, amount_rwf, balance_after,
		        COALESCE(description,''), COALESCE(phone_number,''), COALESCE(external_ref,''),
		        status, created_at
		   FROM wallet_transactions
		  WHERE user_id = $1
		  ORDER BY created_at DESC
		  LIMIT $2 OFFSET $3`,
		userID, limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var txs []*Transaction
	for rows.Next() {
		var t Transaction
		if err := rows.Scan(
			&t.ID, &t.WalletID, &t.UserID, &t.Type, &t.AmountRWF, &t.BalanceAfter,
			&t.Description, &t.PhoneNumber, &t.ExternalRef, &t.Status, &t.CreatedAt,
		); err != nil {
			return nil, err
		}
		txs = append(txs, &t)
	}
	return txs, rows.Err()
}

// TopUp adds amount to the wallet and records the transaction.
func (r *Repository) TopUp(ctx context.Context, userID string, amountRWF int64, phoneNumber, externalRef, description string) (*Transaction, error) {
	return r.mutate(ctx, userID, TxTopUp, amountRWF, phoneNumber, externalRef, description, StatusCompleted)
}

// Withdraw deducts amount from the wallet.
func (r *Repository) Withdraw(ctx context.Context, userID string, amountRWF int64, phoneNumber, externalRef, description string) (*Transaction, error) {
	return r.mutate(ctx, userID, TxWithdraw, amountRWF, phoneNumber, externalRef, description, StatusCompleted)
}

// DeductForPackage deducts the package price atomically.
func (r *Repository) DeductForPackage(ctx context.Context, userID string, amountRWF int64, description string) (*Transaction, error) {
	return r.mutate(ctx, userID, TxPackagePurchase, amountRWF, "", "", description, StatusCompleted)
}

// CreditGrant adds funds to a wallet (admin action).
func (r *Repository) CreditGrant(ctx context.Context, userID string, amountRWF int64, description string) (*Transaction, error) {
	return r.mutate(ctx, userID, TxCreditGrant, amountRWF, "", "", description, StatusCompleted)
}

// mutate runs a serialisable transaction that locks the wallet row, applies the
// balance delta, and writes the audit record atomically.
func (r *Repository) mutate(
	ctx context.Context,
	userID string,
	txType TxType,
	amountRWF int64,
	phoneNumber, externalRef, description string,
	status TxStatus,
) (*Transaction, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Lock the wallet row for this transaction to prevent double-spends.
	var walletID string
	var currentBalance int64
	err = tx.QueryRow(ctx,
		`SELECT id, balance_rwf FROM wallets WHERE user_id = $1 FOR UPDATE`, userID,
	).Scan(&walletID, &currentBalance)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apperrors.ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	// Compute new balance.
	var newBalance int64
	switch txType {
	case TxTopUp, TxCreditGrant, TxRefund:
		newBalance = currentBalance + amountRWF
	case TxWithdraw, TxPackagePurchase:
		newBalance = currentBalance - amountRWF
		if newBalance < 0 {
			return nil, apperrors.New(402, "INSUFFICIENT_FUNDS", "Wallet balance is too low for this operation.")
		}
	}

	// Update wallet balance.
	_, err = tx.Exec(ctx,
		`UPDATE wallets SET balance_rwf = $1, updated_at = NOW() WHERE id = $2`,
		newBalance, walletID,
	)
	if err != nil {
		return nil, err
	}

	// Insert audit record and return it.
	var t Transaction
	err = tx.QueryRow(ctx,
		`INSERT INTO wallet_transactions
		        (wallet_id, user_id, type, amount_rwf, balance_after, description, phone_number, external_ref, status)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, wallet_id, user_id, type, amount_rwf, balance_after,
		           COALESCE(description,''), COALESCE(phone_number,''), COALESCE(external_ref,''),
		           status, created_at`,
		walletID, userID, string(txType), amountRWF, newBalance,
		description, phoneNumber, externalRef, string(status),
	).Scan(
		&t.ID, &t.WalletID, &t.UserID, &t.Type, &t.AmountRWF, &t.BalanceAfter,
		&t.Description, &t.PhoneNumber, &t.ExternalRef, &t.Status, &t.CreatedAt,
	)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &t, nil
}
