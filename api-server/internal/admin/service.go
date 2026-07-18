package admin

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	goredis "github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
)

// DBTX is the minimal database interface the Service requires.
// *pgxpool.Pool satisfies this interface automatically.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// BonusService grants the registration bonus when a driver is approved.
type BonusService interface {
	GrantRegistrationBonus(ctx context.Context, driverID, vehicleTypeID string) (any, error)
}

// PackagesService grants the free-trial credit when a driver is first approved.
type PackagesService interface {
	GrantFreeTrialIfEligible(ctx context.Context, driverUserID, vehicleTypeCode string) error
}

// Notifier persists an in-app notification and pushes it to every device the
// user has registered (dead tokens pruned). Satisfied by *notification.Service.
// Wired via SetNotifier so admin approve/reject decisions reach the driver's
// phone as a push, not only a status change they must poll for.
type Notifier interface {
	SendToAllDevices(ctx context.Context, userID, title, body, nType string, data map[string]string)
}

// Service handles admin business logic.
type Service struct {
	db       DBTX
	log      zerolog.Logger
	packages PackagesService
	rdb      goredis.UniversalClient
	bonus    BonusService
	notifier Notifier
}

func NewService(db DBTX, log zerolog.Logger) *Service {
	return &Service{db: db, log: log}
}

func (s *Service) SetPackagesService(svc PackagesService) { s.packages = svc }
func (s *Service) SetBonusService(svc BonusService)       { s.bonus = svc }
func (s *Service) SetNotifier(n Notifier)                 { s.notifier = n }

// SetRedis wires the Redis client used by account-assist operations
// (clearing OTP lockouts, GPS anomaly counters).
func (s *Service) SetRedis(rdb goredis.UniversalClient) {
	s.rdb = rdb
}

// ── Driver management ─────────────────────────────────────────────────────

// ── Customer management ───────────────────────────────────────────────────

// ── Rides ─────────────────────────────────────────────────────────────────

// ── Negotiations ──────────────────────────────────────────────────────────

// ── Revenue / transactions ────────────────────────────────────────────────

// ── Safety flags ──────────────────────────────────────────────────────────

// ── Account assist ───────────────────────────────────────────────────────

// ── Helpers ───────────────────────────────────────────────────────────────

func buildWhere(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(clauses, " AND ")
}

func periodToInterval(period string) string {
	switch period {
	case "week":
		return "INTERVAL '7 days'"
	case "month":
		return "INTERVAL '30 days'"
	case "quarter":
		return "INTERVAL '90 days'"
	case "year":
		return "INTERVAL '365 days'"
	default:
		return "INTERVAL '1 day'"
	}
}

// ── Driver detail / create / update / delete ──────────────────────────────

// ── Customer update / ban ─────────────────────────────────────────────────

// ── Live rides ────────────────────────────────────────────────────────────

// ── Negotiation detail ────────────────────────────────────────────────────

// ── Revenue (unified) ─────────────────────────────────────────────────────

type LaunchTask struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Details       string  `json:"details"`
	EstimateHours float64 `json:"estimate_hours"`
	Owner         string  `json:"owner"`
	Status        string  `json:"status"`
	CriticalPath  bool    `json:"critical_path,omitempty"`
}

type LaunchTrack struct {
	Name  string       `json:"name"`
	Tasks []LaunchTask `json:"tasks"`
}

type LaunchTrackerData struct {
	Team        []string      `json:"team"`
	APIEndpoint string        `json:"api_endpoint"`
	Tracks      []LaunchTrack `json:"tracks"`
}
