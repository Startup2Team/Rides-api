package matching

import "math"

// minCostAssignment solves the rectangular linear assignment problem: given a
// cost matrix cost[i][j] (rows × cols, finite values), find an assignment of
// rows to distinct columns that MINIMISES the total assigned cost, using the
// O(n³) Hungarian / Kuhn–Munkres method.
//
// Returns rowToCol of length rows, where rowToCol[i] is the column assigned to
// row i, or -1 if row i is left unassigned (only possible when rows > cols).
// Every returned column index is distinct. cost must be rectangular (every row
// the same length); an empty matrix returns an empty slice.
//
// It is a pure, deterministic function with no I/O — the unit-tested core of
// batched dispatch. Callers encode "forbidden" pairings as a large finite cost
// (never +Inf into the solver) and drop them after the solve; see
// solveAssignment.
func minCostAssignment(cost [][]float64) []int {
	rows := len(cost)
	if rows == 0 {
		return nil
	}
	cols := len(cost[0])
	if cols == 0 {
		r := make([]int, rows)
		for i := range r {
			r[i] = -1
		}
		return r
	}

	if rows <= cols {
		return hungarian(cost, rows, cols)
	}

	// The core requires #rows <= #cols. Transpose, solve, and invert the result
	// so the public contract (rowToCol indexed by original rows) still holds.
	t := make([][]float64, cols)
	for j := 0; j < cols; j++ {
		t[j] = make([]float64, rows)
		for i := 0; i < rows; i++ {
			t[j][i] = cost[i][j]
		}
	}
	colToRow := hungarian(t, cols, rows) // index = original col, value = original row
	rowToCol := make([]int, rows)
	for i := range rowToCol {
		rowToCol[i] = -1
	}
	for c, r := range colToRow {
		if r >= 0 {
			rowToCol[r] = c
		}
	}
	return rowToCol
}

// hungarian is the O(n³) Kuhn–Munkres solver for the case n = rows <= m = cols,
// assigning every row to a distinct column at minimum total cost. Returns
// rowToCol of length rows. This is the well-known potentials/augmenting-path
// formulation (1-indexed internally); it is exact for any finite cost matrix.
func hungarian(cost [][]float64, rows, cols int) []int {
	const inf = math.MaxFloat64

	u := make([]float64, rows+1) // row potentials
	v := make([]float64, cols+1) // column potentials
	p := make([]int, cols+1)     // p[j] = row matched to column j (0 = none)
	way := make([]int, cols+1)   // augmenting-path back-pointers

	for i := 1; i <= rows; i++ {
		p[0] = i
		j0 := 0
		minv := make([]float64, cols+1)
		used := make([]bool, cols+1)
		for j := 0; j <= cols; j++ {
			minv[j] = inf
		}

		for {
			used[j0] = true
			i0 := p[j0]
			delta := inf
			j1 := -1
			for j := 1; j <= cols; j++ {
				if used[j] {
					continue
				}
				cur := cost[i0-1][j-1] - u[i0] - v[j]
				if cur < minv[j] {
					minv[j] = cur
					way[j] = j0
				}
				if minv[j] < delta {
					delta = minv[j]
					j1 = j
				}
			}
			for j := 0; j <= cols; j++ {
				if used[j] {
					u[p[j]] += delta
					v[j] -= delta
				} else if minv[j] < inf {
					minv[j] -= delta
				}
			}
			j0 = j1
			if p[j0] == 0 {
				break
			}
		}

		// Follow the back-pointers to flip the augmenting path.
		for j0 != 0 {
			j1 := way[j0]
			p[j0] = p[j1]
			j0 = j1
		}
	}

	rowToCol := make([]int, rows)
	for i := range rowToCol {
		rowToCol[i] = -1
	}
	for j := 1; j <= cols; j++ {
		if p[j] != 0 {
			rowToCol[p[j]-1] = j - 1
		}
	}
	return rowToCol
}

// solveAssignment applies the PER-RIDE FAIRNESS CAP and then the min-cost
// assignment to a driver×ride ETA cost matrix. cost[i][j] is the real-road (or
// Haversine-fallback) ETA in seconds from driver i to ride j's pickup; +Inf
// means no route.
//
// Objective: minimise the TOTAL assigned ETA across the batch, subject to the
// cap — a driver may serve a ride only if its ETA is
//
//	cost[i][j] <= capFactor × best[j] + slackS
//
// where best[j] is ride j's minimum available ETA over all candidate drivers.
// Pairings above the cap (and no-route pairings) are FORBIDDEN: they are given a
// large finite cost so the solver still returns a complete matching, then
// dropped afterwards. A ride with no in-cap driver gets colToRow[j] = -1 and is
// left unassigned (the caller starts its normal broadcast) rather than being
// force-matched to a bad driver.
//
// Returns colToRow of length nRides: colToRow[j] is the assigned driver row for
// ride j, or -1. Pure and deterministic.
func solveAssignment(cost [][]float64, capFactor, slackS float64) []int {
	rows := len(cost)
	if rows == 0 {
		return nil
	}
	cols := len(cost[0])
	if cols == 0 {
		return []int{}
	}
	if capFactor < 1 {
		capFactor = 2.0
	}
	if slackS < 0 {
		slackS = 0
	}

	// Per-ride best (minimum) ETA.
	best := make([]float64, cols)
	for j := range best {
		best[j] = math.Inf(1)
	}
	for i := 0; i < rows; i++ {
		for j := 0; j < cols; j++ {
			if cost[i][j] < best[j] {
				best[j] = cost[i][j]
			}
		}
	}

	// bigCost must dwarf any real ETA yet stay finite so the solver never sees
	// +Inf (which would break the potential arithmetic). Kigali cross-city ETAs
	// are well under an hour; 1e9 seconds is ~31 years — unmistakably forbidden.
	const bigCost = 1e9
	allowed := make([][]bool, rows)
	solveCost := make([][]float64, rows)
	for i := 0; i < rows; i++ {
		allowed[i] = make([]bool, cols)
		solveCost[i] = make([]float64, cols)
		for j := 0; j < cols; j++ {
			capJ := capFactor*best[j] + slackS
			c := cost[i][j]
			if math.IsInf(best[j], 1) || math.IsInf(c, 1) || c > capJ {
				solveCost[i][j] = bigCost
			} else {
				allowed[i][j] = true
				solveCost[i][j] = c
			}
		}
	}

	rowToCol := minCostAssignment(solveCost)

	colToRow := make([]int, cols)
	for j := range colToRow {
		colToRow[j] = -1
	}
	for i, col := range rowToCol {
		if col >= 0 && col < cols && allowed[i][col] {
			colToRow[col] = i
		}
	}
	return colToRow
}
