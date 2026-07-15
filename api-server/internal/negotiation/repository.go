package negotiation

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Round is one fare negotiation round.
type Round struct {
	ID              string
	RideID          string
	RoundNumber     int
	ProposedBy      string // "CUSTOMER" | "DRIVER"
	ProposedAmount  float64
	Response        *string // ACCEPTED | COUNTERED | DECLINED | TIMEOUT
	Message         *string
	CallInitiated   bool
	CallInitiatedAt *time.Time
	CreatedAt       time.Time
}

// TextMessage is a chat message sent during negotiation.
type TextMessage struct {
	ID        string    `json:"id"`
	RideID    string    `json:"ride_id"`
	Sender    string    `json:"sender"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Repository handles negotiation round DB operations.
type Repository struct {
	db *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *Repository {
	return &Repository{db: db}
}

func (r *Repository) CountRounds(ctx context.Context, rideID string) (int, error) {
	var count int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM negotiation_rounds WHERE ride_id = $1`, rideID).Scan(&count)
	return count, err
}

// CountRoundsByRole counts how many offers a specific role (CUSTOMER|DRIVER) has made for a ride.
func (r *Repository) CountRoundsByRole(ctx context.Context, rideID, role string) (int, error) {
	var count int
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*) FROM negotiation_rounds WHERE ride_id = $1 AND proposed_by = $2`,
		rideID, role,
	).Scan(&count)
	return count, err
}

func (r *Repository) CreateRound(ctx context.Context, rideID string, roundNumber int, proposedBy string, amount float64) (*Round, error) {
	round := &Round{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO negotiation_rounds (ride_id, round_number, proposed_by, proposed_amount)
		VALUES ($1, $2, $3, $4)
		RETURNING id, ride_id, round_number, proposed_by, proposed_amount, response, message, call_initiated, call_initiated_at, created_at
	`, rideID, roundNumber, proposedBy, amount).Scan(
		&round.ID, &round.RideID, &round.RoundNumber, &round.ProposedBy,
		&round.ProposedAmount, &round.Response, &round.Message, &round.CallInitiated,
		&round.CallInitiatedAt, &round.CreatedAt,
	)
	return round, err
}

func (r *Repository) GetLatestRound(ctx context.Context, rideID string) (*Round, error) {
	round := &Round{}
	err := r.db.QueryRow(ctx, `
		SELECT id, ride_id, round_number, proposed_by, proposed_amount, response,
		       message, call_initiated, call_initiated_at, created_at
		FROM negotiation_rounds
		WHERE ride_id = $1
		ORDER BY round_number DESC LIMIT 1
	`, rideID).Scan(
		&round.ID, &round.RideID, &round.RoundNumber, &round.ProposedBy,
		&round.ProposedAmount, &round.Response, &round.Message, &round.CallInitiated,
		&round.CallInitiatedAt, &round.CreatedAt,
	)
	return round, err
}

func (r *Repository) SetResponse(ctx context.Context, roundID, response string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE negotiation_rounds SET response = $1 WHERE id = $2`,
		response, roundID,
	)
	return err
}

func (r *Repository) MarkCallInitiated(ctx context.Context, rideID string) error {
	// Postgres does not allow ORDER BY / LIMIT on a bare UPDATE — target the
	// latest round via a subquery instead.
	_, err := r.db.Exec(ctx, `
		UPDATE negotiation_rounds
		SET call_initiated = TRUE, call_initiated_at = NOW()
		WHERE id = (
			SELECT id FROM negotiation_rounds
			WHERE ride_id = $1
			ORDER BY round_number DESC
			LIMIT 1
		)
	`, rideID)
	return err
}

func (r *Repository) ListRounds(ctx context.Context, rideID string) ([]*Round, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, ride_id, round_number, proposed_by, proposed_amount, response,
		       message, call_initiated, call_initiated_at, created_at
		FROM negotiation_rounds WHERE ride_id = $1 ORDER BY round_number ASC
	`, rideID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rounds []*Round
	for rows.Next() {
		round := &Round{}
		if err := rows.Scan(
			&round.ID, &round.RideID, &round.RoundNumber, &round.ProposedBy,
			&round.ProposedAmount, &round.Response, &round.Message, &round.CallInitiated,
			&round.CallInitiatedAt, &round.CreatedAt,
		); err != nil {
			return nil, err
		}
		rounds = append(rounds, round)
	}
	return rounds, rows.Err()
}

// ── Text messages ────────────────────────────────────────────────────────────

func (r *Repository) CreateTextMessage(ctx context.Context, rideID, sender, body string) (*TextMessage, error) {
	msg := &TextMessage{}
	err := r.db.QueryRow(ctx, `
		INSERT INTO negotiation_messages (ride_id, sender, body)
		VALUES ($1, $2, $3)
		RETURNING id, ride_id, sender, body, created_at
	`, rideID, sender, body).Scan(&msg.ID, &msg.RideID, &msg.Sender, &msg.Body, &msg.CreatedAt)
	return msg, err
}

func (r *Repository) ListTextMessages(ctx context.Context, rideID string) ([]*TextMessage, error) {
	rows, err := r.db.Query(ctx, `
		SELECT id, ride_id, sender, body, created_at
		FROM negotiation_messages WHERE ride_id = $1 ORDER BY created_at ASC
	`, rideID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []*TextMessage
	for rows.Next() {
		m := &TextMessage{}
		if err := rows.Scan(&m.ID, &m.RideID, &m.Sender, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}
