package customer

import "testing"

func TestComputeLevel(t *testing.T) {
	tests := []struct {
		name            string
		rides           int
		wantLevel       string
		wantIndex       int
		wantNext        string // "" means top tier (nil NextLevel)
		wantToNext      int
		wantProgressLow float64 // inclusive lower bound
		wantProgressHi  float64 // inclusive upper bound
	}{
		{"zero rides", 0, "BRONZE", 0, "SILVER", 10, 0, 0},
		{"just below silver", 9, "BRONZE", 0, "SILVER", 1, 0.89, 0.91},
		{"exactly silver", 10, "SILVER", 1, "GOLD", 40, 0, 0},
		{"just below gold", 49, "SILVER", 1, "GOLD", 1, 0.97, 0.99},
		{"exactly gold", 50, "GOLD", 2, "PREMIUM", 100, 0, 0},
		{"just below premium", 149, "GOLD", 2, "PREMIUM", 1, 0.98, 1.0},
		{"exactly premium", 150, "PREMIUM", 3, "", 0, 1.0, 1.0},
		{"well past premium", 500, "PREMIUM", 3, "", 0, 1.0, 1.0},
		{"negative clamps to zero", -5, "BRONZE", 0, "SILVER", 10, 0, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeLevel(tc.rides, 0)
			if got.Level != tc.wantLevel || got.LevelIndex != tc.wantIndex {
				t.Fatalf("level = %s(%d), want %s(%d)", got.Level, got.LevelIndex, tc.wantLevel, tc.wantIndex)
			}
			if tc.wantNext == "" {
				if got.NextLevel != nil {
					t.Fatalf("NextLevel = %v, want nil (top tier)", *got.NextLevel)
				}
			} else {
				if got.NextLevel == nil || *got.NextLevel != tc.wantNext {
					t.Fatalf("NextLevel = %v, want %s", got.NextLevel, tc.wantNext)
				}
			}
			if got.RidesToNextLevel != tc.wantToNext {
				t.Fatalf("RidesToNextLevel = %d, want %d", got.RidesToNextLevel, tc.wantToNext)
			}
			if got.ProgressToNext < tc.wantProgressLow || got.ProgressToNext > tc.wantProgressHi {
				t.Fatalf("ProgressToNext = %.3f, want in [%.2f, %.2f]", got.ProgressToNext, tc.wantProgressLow, tc.wantProgressHi)
			}
		})
	}
}
