package handlers

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// Textbook example (Taha): max 3x + 5y, x ≤ 4, 2y ≤ 12, 3x + 2y ≤ 18 → x=2, y=6, value 36.
// Shadow prices: 0 for the first constraint (it is not binding), 1.5 and 1 for the others.
func TestSimplexTextbook(t *testing.T) {
	res, err := simplex(
		[]float64{3, 5},
		[][]float64{{1, 0}, {0, 2}, {3, 2}},
		[]float64{4, 12, 18},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !near(res.X[0], 2) || !near(res.X[1], 6) || !near(res.Value, 36) {
		t.Fatalf("got x=%v value=%v, want [2 6] 36", res.X, res.Value)
	}
	want := []float64{0, 1.5, 1}
	for i := range want {
		if !near(res.Duals[i], want[i]) {
			t.Fatalf("duals = %v, want %v", res.Duals, want)
		}
	}
}

func TestSimplexZeroIsOptimal(t *testing.T) {
	// Every variable only loses money: the best plan is to do nothing.
	res, err := simplex([]float64{-1, -2}, [][]float64{{1, 1}}, []float64{10})
	if err != nil {
		t.Fatal(err)
	}
	if !near(res.Value, 0) || !near(res.X[0], 0) || !near(res.X[1], 0) {
		t.Fatalf("got %+v, want zero plan", res)
	}
}

func TestSimplexUnbounded(t *testing.T) {
	// max x with only x − y ≤ 1: x can grow forever together with y.
	_, err := simplex([]float64{1, 0}, [][]float64{{1, -1}}, []float64{1})
	if err != errUnbounded {
		t.Fatalf("err = %v, want errUnbounded", err)
	}
}

func TestSimplexDegenerate(t *testing.T) {
	// Degenerate vertex (b = 0 in one row) — Bland's rule must not cycle.
	res, err := simplex(
		[]float64{10, -57, -9, -24},
		[][]float64{
			{0.5, -5.5, -2.5, 9},
			{0.5, -1.5, -0.5, 1},
			{1, 0, 0, 0},
		},
		[]float64{0, 0, 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !near(res.Value, 1) {
		t.Fatalf("value = %v, want 1", res.Value)
	}
}

// A tiny purchasing problem in the same shape the handler builds:
// two dishes share one ingredient, budget covers only part of the demand.
func TestSimplexPurchasingShape(t *testing.T) {
	// vars: s1, s2 (portions), x (kg bought)
	// max 1000·s1 + 600·s2 − 2000·x
	// 0.2·s1 + 0.2·s2 − x ≤ 0   (ingredient balance, empty stock)
	// 2000·x ≤ 20000            (budget → at most 10 kg)
	// s1 ≤ 30, s2 ≤ 40          (forecast demand)
	res, err := simplex(
		[]float64{1000, 600, -2000},
		[][]float64{
			{0.2, 0.2, -1},
			{0, 0, 2000},
			{1, 0, 0},
			{0, 1, 0},
		},
		[]float64{0, 20000, 30, 40},
	)
	if err != nil {
		t.Fatal(err)
	}
	// 10 kg feed 50 portions: all 30 of the more profitable dish first, then 20 of the other.
	if !near(res.X[0], 30) || !near(res.X[1], 20) || !near(res.X[2], 10) {
		t.Fatalf("plan = %v, want [30 20 10]", res.X)
	}
	// Shadow price of the budget: +1 ₸ buys 1/2000 kg = 1/400 portion of dish 2,
	// i.e. +1.5 ₸ revenue for −1 ₸ spent → +0.5 ₸ profit per extra tenge.
	if !near(res.Duals[1], 0.5) {
		t.Fatalf("budget shadow price = %v, want 0.5", res.Duals[1])
	}
}
