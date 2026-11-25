package main

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The interval of the median is the pair of order statistics of the binomial tables: for n = 10,
// P(B ≤ 1) = 11/1024 ≤ 0.025 < P(B ≤ 2) = 56/1024, so k = 2 and the interval is [x(2), x(9)]; for
// n = 6, P(B = 0) = 1/64 gives [x(1), x(6)]; for n = 5, P(B = 0) = 1/32 > 0.025 leaves none.
func TestMedianCI(t *testing.T) {
	xs := []float64{10, 3, 7, 1, 9, 5, 2, 8, 6, 4}
	lo, hi, ok := medianCI(xs, confidence)
	if !ok || lo != 2 || hi != 9 {
		t.Errorf("n = 10: got [%v, %v] %v, want [2, 9]", lo, hi, ok)
	}
	lo, hi, ok = medianCI([]float64{6, 1, 5, 2, 4, 3}, confidence)
	if !ok || lo != 1 || hi != 6 {
		t.Errorf("n = 6: got [%v, %v] %v, want [1, 6]", lo, hi, ok)
	}
	if _, _, ok := medianCI([]float64{1, 2, 3, 4, 5}, confidence); ok {
		t.Error("n = 5: an interval at 95% does not exist")
	}
	// n = 20: P(B ≤ 5) = 21700/1048576 ≈ 0.0207 ≤ 0.025 < P(B ≤ 6) ≈ 0.0577, so k = 6: [x(6), x(15)].
	var big []float64
	for i := 20; i >= 1; i-- {
		big = append(big, float64(i))
	}
	lo, hi, ok = medianCI(big, confidence)
	if !ok || lo != 6 || hi != 15 {
		t.Errorf("n = 20: got [%v, %v] %v, want [6, 15]", lo, hi, ok)
	}
}

// The distribution of U for m = n = 4 has C(8, 4) = 70 arrangements, symmetric around 8, with one
// arrangement each for U = 0 and U = 1 and two for U = 2.
func TestUDist(t *testing.T) {
	d := uDist(4, 4)
	if len(d) != 17 {
		t.Fatalf("got %d values, want 17", len(d))
	}
	sum := 0.0
	for u, p := range d {
		sum += p
		if !near(p, d[16-u]) {
			t.Errorf("P(U = %d) = %v differs from P(U = %d) = %v", u, p, 16-u, d[16-u])
		}
	}
	if !near(sum, 1) || !near(d[0], 1.0/70) || !near(d[1], 1.0/70) || !near(d[2], 2.0/70) {
		t.Errorf("got sum %v, P(0..2) = %v %v %v; want 1, 1/70, 1/70, 2/70", sum, d[0], d[1], d[2])
	}
	// The table value for m = n = 10: P(U ≤ 23) ≈ 0.0216 and P(U ≤ 24) ≈ 0.0262, so the two-sided
	// critical value at 5% is 23.
	d = uDist(10, 10)
	c := 0.0
	for u := 0; u <= 23; u++ {
		c += d[u]
	}
	if c > 0.025 || c+d[24] <= 0.025 {
		t.Errorf("m = n = 10: P(U ≤ 23) = %v, P(U ≤ 24) = %v; want the 5%% critical value 23", c, c+d[24])
	}
}

// Hand-checked samples: {1, 2, 3} against {4, 5, 6} has U = 0, the one most extreme of the C(6, 3) = 20
// arrangements, so p = 2/20; {1, 2, 4} against {3, 5, 6} has U = 1 (4 > 3), and P(U ≤ 1) = 2/20, so
// p = 4/20.
func TestMannWhitney(t *testing.T) {
	for _, c := range []struct {
		a, b []float64
		u, p float64
	}{
		{[]float64{1, 2, 3}, []float64{4, 5, 6}, 0, 0.1},
		{[]float64{4, 5, 6}, []float64{1, 2, 3}, 9, 0.1},
		{[]float64{1, 2, 4}, []float64{3, 5, 6}, 1, 0.2},
		{[]float64{1, 2, 3}, []float64{1, 2, 3}, 4.5, 1},
	} {
		u, p := mannWhitney(c.a, c.b)
		if !near(u, c.u) || math.Abs(p-c.p) > 1e-9 && !(c.p == 1 && p > 0.9) {
			t.Errorf("%v vs %v: got U = %v, p = %v; want %v, %v", c.a, c.b, u, p, c.u, c.p)
		}
	}
	// The normal approximation with ties: equal samples are not different.
	a := make([]float64, 60)
	b := make([]float64, 60)
	for i := range a {
		a[i], b[i] = float64(i%7), float64(i%7)
	}
	if _, p := mannWhitney(a, b); p < 0.9 {
		t.Errorf("equal tied samples: p = %v, want about 1", p)
	}
}

// For m = n = 4, P(U ≤ 0) = 1/70 ≤ 0.025 < P(U ≤ 1) = 2/70, so k = 1 and the interval of the ratio
// spans the extreme pairwise ratios: min(b)/max(a) to max(b)/min(a). For m = n = 10, k = 24.
func TestRatioCI(t *testing.T) {
	a := []float64{100, 101, 102, 103}
	b := []float64{110, 111, 112, 113}
	r, lo, hi, ok := ratioCI(a, b, confidence)
	if !ok || !near(r, 111.5/101.5) || !near(lo, 110.0/103) || !near(hi, 113.0/100) {
		t.Errorf("m = n = 4: got %v [%v, %v] %v; want %v [%v, %v]", r, lo, hi, ok, 111.5/101.5, 110.0/103, 113.0/100)
	}
	if _, _, _, ok := ratioCI([]float64{1, 2}, []float64{3, 4}, confidence); ok {
		t.Error("m = n = 2: no interval exists at 95%")
	}
	// Ten values each, b = 2·a: every difference of logs is log 2 + log(a_j/a_i); the 24th smallest
	// and largest of the 100 lie on either side of log 2, and the interval holds 2.
	var a10, b10 []float64
	for i := 1; i <= 10; i++ {
		a10 = append(a10, 100+float64(i))
		b10 = append(b10, 2*(100+float64(i)))
	}
	r, lo, hi, ok = ratioCI(a10, b10, confidence)
	if !ok || !near(r, 2) || !(lo < 2 && 2 < hi) || lo < 1.9 || hi > 2.1 {
		t.Errorf("m = n = 10: got %v [%v, %v] %v", r, lo, hi, ok)
	}
}
