package handlers

import (
	"errors"
	"math"
)

// ---------- Simplex method ----------
//
// Solves a linear programme in the form
//
//	maximise   c·x
//	subject to A·x ≤ b,  x ≥ 0,  with b ≥ 0
//
// b ≥ 0 means x = 0 is always a feasible starting point, so the classic
// one-phase tableau method is enough (no "phase 1"). Bland's rule
// (the smallest index wins) guarantees the method never cycles.

const lpEps = 1e-9

var errUnbounded = errors.New("linear programme is unbounded")

type lpResult struct {
	X      []float64 // optimal values of the variables
	Value  float64   // optimal objective value
	Duals  []float64 // shadow price of every constraint: how much the objective grows per +1 of b[i]
	Pivots int
}

func simplex(c []float64, A [][]float64, b []float64) (lpResult, error) {
	m, n := len(A), len(c)
	width := n + m + 1 // variables | slack variables | right-hand side

	// Tableau: m constraint rows + objective row.
	t := make([][]float64, m+1)
	for i := 0; i < m; i++ {
		if b[i] < 0 {
			return lpResult{}, errors.New("simplex: right-hand side must be non-negative")
		}
		t[i] = make([]float64, width)
		copy(t[i], A[i])
		t[i][n+i] = 1 // slack
		t[i][width-1] = b[i]
	}
	t[m] = make([]float64, width)
	for j := 0; j < n; j++ {
		t[m][j] = -c[j]
	}

	basis := make([]int, m)
	for i := range basis {
		basis[i] = n + i // start from the slack basis: x = 0
	}

	pivots := 0
	for {
		// Entering column: first one (Bland) with a negative reduced cost.
		enter := -1
		for j := 0; j < width-1; j++ {
			if t[m][j] < -lpEps {
				enter = j
				break
			}
		}
		if enter < 0 {
			break // optimal
		}

		// Leaving row: minimum ratio test, ties broken by the smaller basis index.
		leave := -1
		best := math.Inf(1)
		for i := 0; i < m; i++ {
			if t[i][enter] > lpEps {
				ratio := t[i][width-1] / t[i][enter]
				if ratio < best-lpEps || (math.Abs(ratio-best) <= lpEps && basis[i] < basis[leave]) {
					best, leave = ratio, i
				}
			}
		}
		if leave < 0 {
			return lpResult{}, errUnbounded
		}

		// Pivot.
		p := t[leave][enter]
		for j := range t[leave] {
			t[leave][j] /= p
		}
		for i := 0; i <= m; i++ {
			if i == leave || math.Abs(t[i][enter]) < lpEps {
				continue
			}
			f := t[i][enter]
			for j := range t[i] {
				t[i][j] -= f * t[leave][j]
			}
		}
		basis[leave] = enter
		pivots++
		if pivots > 10000 {
			return lpResult{}, errors.New("simplex: too many iterations")
		}
	}

	res := lpResult{X: make([]float64, n), Duals: make([]float64, m), Value: t[m][width-1], Pivots: pivots}
	for i, v := range basis {
		if v < n {
			res.X[v] = t[i][width-1]
		}
	}
	// In the optimal tableau the objective row under slack i is the dual value y_i.
	for i := 0; i < m; i++ {
		res.Duals[i] = t[m][n+i]
	}
	return res, nil
}
