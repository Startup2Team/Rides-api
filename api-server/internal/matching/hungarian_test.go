package matching

import (
	"math"
	"testing"
)

// totalCost sums the assigned costs for a rowToCol assignment, ignoring
// unassigned rows (-1).
func totalCost(cost [][]float64, rowToCol []int) float64 {
	sum := 0.0
	for i, j := range rowToCol {
		if j >= 0 {
			sum += cost[i][j]
		}
	}
	return sum
}

// bruteForceMin returns the minimum total cost of matching min(rows,cols)
// distinct pairs, by exhaustive search — the reference oracle. It iterates over
// the smaller dimension so it handles rectangular matrices in either
// orientation (matching every element of the smaller side to a distinct element
// of the larger, exactly as the solver does).
func bruteForceMin(cost [][]float64) float64 {
	rows := len(cost)
	if rows == 0 {
		return 0
	}
	cols := len(cost[0])
	// at(a, b): cost with `a` indexing the smaller dimension.
	small, large := rows, cols
	transposed := false
	if cols < rows {
		small, large = cols, rows
		transposed = true
	}
	at := func(a, b int) float64 {
		if transposed {
			return cost[b][a]
		}
		return cost[a][b]
	}
	best := math.Inf(1)
	used := make([]bool, large)
	var rec func(a int, acc float64)
	rec = func(a int, acc float64) {
		if acc >= best {
			return
		}
		if a == small {
			if acc < best {
				best = acc
			}
			return
		}
		for b := 0; b < large; b++ {
			if used[b] {
				continue
			}
			used[b] = true
			rec(a+1, acc+at(a, b))
			used[b] = false
		}
	}
	rec(0, 0)
	return best
}

func assertDistinctCols(t *testing.T, rowToCol []int) {
	t.Helper()
	seen := map[int]bool{}
	for _, j := range rowToCol {
		if j < 0 {
			continue
		}
		if seen[j] {
			t.Fatalf("column %d assigned to more than one row: %v", j, rowToCol)
		}
		seen[j] = true
	}
}

func TestMinCostAssignment_KnownOptimum(t *testing.T) {
	// Classic 3×3 with a unique optimum. Optimal assignment is
	// row0→col1 (2), row1→col0 (3), row2→col2 (3) = 8? Let's just check against
	// the brute-force oracle to avoid hand-error.
	cost := [][]float64{
		{4, 2, 8},
		{3, 9, 5},
		{8, 4, 3},
	}
	got := minCostAssignment(cost)
	assertDistinctCols(t, got)
	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d", len(got))
	}
	want := bruteForceMin(cost)
	if math.Abs(totalCost(cost, got)-want) > 1e-9 {
		t.Fatalf("total=%.3f want optimum=%.3f assignment=%v", totalCost(cost, got), want, got)
	}
}

func TestMinCostAssignment_CollisionCase(t *testing.T) {
	// The greedy-collision scenario: two rides, one "in between" driver.
	// Drivers (rows) × rides (cols). Driver 0 is close to both; driver 1 only
	// good for ride 0; driver 2 only good for ride 1. Greedy would give the
	// in-between driver 0 to whichever ride grabs first, pushing the other onto
	// a bad driver. Optimal splits: 0→ride0 via d1, ride1 via d0? Check oracle.
	cost := [][]float64{
		{60, 70},  // driver 0: near both
		{65, 400}, // driver 1: near ride 0 only
		{420, 80}, // driver 2: near ride 1 only
	}
	got := minCostAssignment(cost)
	assertDistinctCols(t, got)
	want := bruteForceMin(cost)
	if math.Abs(totalCost(cost, got)-want) > 1e-9 {
		t.Fatalf("total=%.3f want optimum=%.3f assignment=%v", totalCost(cost, got), want, got)
	}
}

func TestMinCostAssignment_Rectangular_MoreDriversThanRides(t *testing.T) {
	// 4 drivers, 2 rides. Every ride must be served; two drivers go unused.
	cost := [][]float64{
		{10, 90},
		{90, 12},
		{50, 50},
		{11, 11},
	}
	got := minCostAssignment(cost)
	assertDistinctCols(t, got)
	// Exactly 2 rows assigned (one per column), the rest -1.
	assigned := 0
	for _, j := range got {
		if j >= 0 {
			assigned++
		}
	}
	if assigned != 2 {
		t.Fatalf("want 2 assigned rows, got %d: %v", assigned, got)
	}
	want := bruteForceMin(cost)
	if math.Abs(totalCost(cost, got)-want) > 1e-9 {
		t.Fatalf("total=%.3f want optimum=%.3f", totalCost(cost, got), want)
	}
}

func TestMinCostAssignment_Rectangular_MoreRidesThanDrivers(t *testing.T) {
	// 2 drivers, 4 rides. Only 2 rides can be served; 2 columns unmatched.
	cost := [][]float64{
		{10, 90, 30, 40},
		{90, 12, 35, 20},
	}
	got := minCostAssignment(cost)
	assertDistinctCols(t, got)
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	// Both drivers should be assigned to their cheapest distinct rides.
	for i, j := range got {
		if j < 0 {
			t.Fatalf("driver %d left unassigned though rides remain: %v", i, got)
		}
	}
}

func TestMinCostAssignment_Empty(t *testing.T) {
	if got := minCostAssignment(nil); got != nil {
		t.Fatalf("nil matrix: want nil, got %v", got)
	}
	if got := minCostAssignment([][]float64{}); got != nil {
		t.Fatalf("empty matrix: want nil, got %v", got)
	}
	got := minCostAssignment([][]float64{{}, {}})
	for i, j := range got {
		if j != -1 {
			t.Fatalf("zero-col matrix: row %d want -1, got %d", i, j)
		}
	}
}

func TestSolveAssignment_FairnessCapForbidsFarDriver(t *testing.T) {
	// Ride 0 best ETA = 60s. With capFactor=2, slack=0, cap=120s. Driver 1 at
	// 400s to ride 0 exceeds the cap and must NOT be chosen for ride 0 even if
	// that would minimise the raw sum. Single ride, two drivers.
	cost := [][]float64{
		{60},  // driver 0
		{400}, // driver 1 — beyond cap
	}
	col := solveAssignment(cost, 2.0, 0)
	if len(col) != 1 {
		t.Fatalf("want 1 ride col, got %d", len(col))
	}
	if col[0] != 0 {
		t.Fatalf("ride 0 should be served by driver 0 (60s), got driver %d", col[0])
	}
}

func TestSolveAssignment_AllCappedLeavesUnassigned(t *testing.T) {
	// Every driver is far AND there is only one driver; best=300, cap=600,
	// so the single driver is IN cap — served. Now make a second ride whose
	// only reachable driver is beyond its cap → that ride unassigned.
	// Rows=drivers, cols=rides.
	inf := math.Inf(1)
	cost := [][]float64{
		// ride0  ride1
		{100, 100000}, // driver 0: great for ride0, absurd for ride1
		{inf, 100},    // driver 1: no route to ride0, great for ride1
	}
	// ride1 best = 100 (driver1). cap=200. driver0→ride1 = 100000 forbidden.
	// So ride1 must get driver1, ride0 must get driver0. Both served.
	col := solveAssignment(cost, 2.0, 0)
	if col[0] != 0 || col[1] != 1 {
		t.Fatalf("want ride0→d0, ride1→d1; got %v", col)
	}
}

func TestSolveAssignment_NoRouteIsForbidden(t *testing.T) {
	inf := math.Inf(1)
	// Single ride, the only driver has no route (+Inf). Must be left unassigned,
	// never force-matched.
	cost := [][]float64{{inf}}
	col := solveAssignment(cost, 2.0, 120)
	if col[0] != -1 {
		t.Fatalf("no-route pairing must be unassigned, got driver %d", col[0])
	}
}

func TestSolveAssignment_SlackWidensCapForTinyBest(t *testing.T) {
	// Best ETA = 30s. capFactor=2 → 60s cap without slack would forbid a
	// perfectly reasonable 90s driver. With slack=120, cap=180s, so the 90s
	// driver for a SECOND ride is allowed.
	cost := [][]float64{
		{30, 90}, // driver 0: best for ride0, ok for ride1
		{95, 30}, // driver 1: ok for ride0, best for ride1
	}
	// With slack the assignment should serve both rides at their best:
	// ride0→d0 (30), ride1→d1 (30).
	col := solveAssignment(cost, 2.0, 120)
	if col[0] != 0 || col[1] != 1 {
		t.Fatalf("want ride0→d0, ride1→d1; got %v", col)
	}
}

func TestSolveAssignment_MinimisesTotalWithinCap(t *testing.T) {
	// Two rides, three drivers, all within generous caps. Verify the solver
	// picks the globally cheapest feasible pairing, not a greedy per-ride one.
	cost := [][]float64{
		{50, 55}, // driver 0
		{52, 90}, // driver 1
		{88, 51}, // driver 2
	}
	col := solveAssignment(cost, 5.0, 300) // caps loose → pure min-sum
	assigned := map[int]int{}
	for ride, drv := range col {
		if drv >= 0 {
			assigned[drv] = ride
		}
	}
	// Compare against brute force of the raw matrix (caps are loose).
	want := bruteForceMin([][]float64{{50, 55}, {52, 90}, {88, 51}})
	sum := 0.0
	for ride, drv := range col {
		if drv >= 0 {
			sum += cost[drv][ride]
		}
	}
	if math.Abs(sum-want) > 1e-9 {
		t.Fatalf("total=%.1f want optimum=%.1f col=%v", sum, want, col)
	}
}
