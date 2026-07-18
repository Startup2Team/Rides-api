package paymentmethods

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

const methodCols = `id, provider, label, phone_number, is_default, created_at`

func scanMethod(row pgx.Row) (*Method, error) {
	m := &Method{}
	if err := row.Scan(&m.ID, &m.Provider, &m.Label, &m.PhoneNumber, &m.IsDefault, &m.CreatedAt); err != nil {
		return nil, err
	}
	return m, nil
}

// List returns the caller's methods, default first then newest.
func (r *Repository) List(ctx context.Context, userID string) ([]*Method, error) {
	rows, err := r.db.Query(ctx,
		`SELECT `+methodCols+` FROM customer_payment_methods
		 WHERE user_id = $1
		 ORDER BY is_default DESC, created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := make([]*Method, 0)
	for rows.Next() {
		m, err := scanMethod(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, m)
	}
	return list, rows.Err()
}

// FindByID returns a single method scoped to the owner, or pgx.ErrNoRows.
func (r *Repository) FindByID(ctx context.Context, userID, id string) (*Method, error) {
	return scanMethod(r.db.QueryRow(ctx,
		`SELECT `+methodCols+` FROM customer_payment_methods
		 WHERE user_id = $1 AND id = $2`, userID, id))
}

// FindByIdempotencyKey returns the method previously created with this key, if any.
func (r *Repository) FindByIdempotencyKey(ctx context.Context, userID, key string) (*Method, error) {
	return scanMethod(r.db.QueryRow(ctx,
		`SELECT `+methodCols+` FROM customer_payment_methods
		 WHERE user_id = $1 AND idempotency_key = $2`, userID, key))
}

// Default returns the caller's default method or (nil, nil) if none.
func (r *Repository) Default(ctx context.Context, userID string) (*Method, error) {
	m, err := scanMethod(r.db.QueryRow(ctx,
		`SELECT `+methodCols+` FROM customer_payment_methods
		 WHERE user_id = $1 AND is_default LIMIT 1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// Insert creates a method. When makeDefault is true it first clears any
// existing default (single-default invariant) inside a transaction.
func (r *Repository) Insert(ctx context.Context, userID string, in AddInput) (*Method, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if in.IsDefault {
		if _, err := tx.Exec(ctx,
			`UPDATE customer_payment_methods SET is_default = FALSE, updated_at = NOW()
			 WHERE user_id = $1 AND is_default`, userID); err != nil {
			return nil, err
		}
	}

	var key *string
	if in.IdempotencyKey != "" {
		key = &in.IdempotencyKey
	}
	m, err := scanMethod(tx.QueryRow(ctx,
		`INSERT INTO customer_payment_methods (user_id, provider, label, phone_number, is_default, idempotency_key)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 RETURNING `+methodCols,
		userID, in.Provider, in.Label, in.PhoneNumber, in.IsDefault, key))
	if err != nil {
		return nil, err
	}
	return m, tx.Commit(ctx)
}

// Update patches label/phone/default. makeDefault clears the previous default.
func (r *Repository) Update(ctx context.Context, userID, id string, in UpdateInput) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if in.IsDefault != nil && *in.IsDefault {
		if _, err := tx.Exec(ctx,
			`UPDATE customer_payment_methods SET is_default = FALSE, updated_at = NOW()
			 WHERE user_id = $1 AND is_default AND id <> $2`, userID, id); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx,
		`UPDATE customer_payment_methods SET
		   label        = COALESCE($3, label),
		   phone_number = CASE WHEN $4 THEN $5 ELSE phone_number END,
		   is_default   = COALESCE($6, is_default),
		   updated_at   = NOW()
		 WHERE user_id = $1 AND id = $2`,
		userID, id, in.Label, in.PhoneNumber != nil, in.PhoneNumber, in.IsDefault); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetDefault promotes one method and demotes the rest, atomically.
func (r *Repository) SetDefault(ctx context.Context, userID, id string) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx,
		`UPDATE customer_payment_methods SET is_default = FALSE, updated_at = NOW()
		 WHERE user_id = $1 AND is_default AND id <> $2`, userID, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE customer_payment_methods SET is_default = TRUE, updated_at = NOW()
		 WHERE user_id = $1 AND id = $2`, userID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Delete removes a method. Returns pgx.ErrNoRows semantics via rowsAffected.
func (r *Repository) Delete(ctx context.Context, userID, id string) (int64, error) {
	ct, err := r.db.Exec(ctx,
		`DELETE FROM customer_payment_methods WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
