package customer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/rs/zerolog"

	"github.com/workspace/ride-platform/internal/customer"
)

// mockRepo lets us drive Service.GetLevel without a database. We only need
// RideStats for the gamification path; the other two methods satisfy the Repo
// interface but are unused here.
type mockRepo struct {
	completedRides int
	totalSpend     float64
	statsErr       error
}

func (m *mockRepo) FindByID(_ context.Context, _ string) (*customer.Profile, error) {
	return nil, nil
}
func (m *mockRepo) UpdateProfile(_ context.Context, _ string, _, _, _, _ *string) error {
	return nil
}
func (m *mockRepo) RideStats(_ context.Context, _ string) (int, float64, error) {
	return m.completedRides, m.totalSpend, m.statsErr
}

func newTestService(repo customer.Repo) *customer.Service {
	return customer.NewService(repo, zerolog.Nop())
}

// GetLevel must turn a raw completed-ride count from the repo into the correct
// tier + progress. This proves the service wires the repo stats into
// computeLevel (the tier maths itself is exhaustively covered in level_test.go).
func TestGetLevel_MapsRepoStatsToTier(t *testing.T) {
	svc := newTestService(&mockRepo{completedRides: 55, totalSpend: 12000})

	lvl, err := svc.GetLevel(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lvl.Level != "GOLD" {
		t.Errorf("Level = %s, want GOLD (55 rides is in the 50–149 band)", lvl.Level)
	}
	if lvl.CompletedRides != 55 {
		t.Errorf("CompletedRides = %d, want 55", lvl.CompletedRides)
	}
	if lvl.TotalSpend != 12000 {
		t.Errorf("TotalSpend = %.0f, want 12000", lvl.TotalSpend)
	}
	if lvl.NextLevel == nil || *lvl.NextLevel != "PREMIUM" {
		t.Errorf("NextLevel = %v, want PREMIUM", lvl.NextLevel)
	}
	if lvl.RidesToNextLevel != 95 { // 150 - 55
		t.Errorf("RidesToNextLevel = %d, want 95", lvl.RidesToNextLevel)
	}
}

// A brand-new customer with zero rides must land in the entry tier — a common
// real case (and the one most likely to hit a divide-by-zero or nil bug).
func TestGetLevel_NewCustomerIsBronze(t *testing.T) {
	svc := newTestService(&mockRepo{completedRides: 0})

	lvl, err := svc.GetLevel(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lvl.Level != "BRONZE" {
		t.Errorf("Level = %s, want BRONZE", lvl.Level)
	}
	if lvl.ProgressToNext != 0 {
		t.Errorf("ProgressToNext = %.2f, want 0", lvl.ProgressToNext)
	}
}

// A DB failure must propagate as an error (and NOT a bogus BRONZE level) so the
// handler returns 5xx instead of quietly serving wrong loyalty data.
func TestGetLevel_PropagatesRepoError(t *testing.T) {
	wantErr := errors.New("db down")
	svc := newTestService(&mockRepo{statsErr: wantErr})

	lvl, err := svc.GetLevel(context.Background(), "user-1")
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if lvl != nil {
		t.Errorf("level = %+v, want nil on error", lvl)
	}
}
