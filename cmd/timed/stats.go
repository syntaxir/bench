package main

import (
	"math"
	"slices"
)

// The statistics of the timed captures are distribution-free, as benchstat's are: a benchmark's
// times are not normally distributed (they have a floor and a long tail of interference), so the
// report summarizes a sample by its median with a confidence interval from order statistics, and
// compares two samples with the Mann-Whitney U test and the matching interval of their ratio.

// confidence is the confidence level of the intervals and the complement of the significance level
// of the tests.
const confidence = 0.95

// medianCI returns the distribution-free confidence interval of the median of xs: the order
// statistics x(k) and x(n+1-k) (1-based) with the largest k whose coverage, 1 - 2·P(B ≤ k-1) for B
// binomial(n, 1/2), is at least conf. ok is false when no k qualifies (fewer than 6 samples at 95%).
func medianCI(xs []float64, conf float64) (lo, hi float64, ok bool) {
	n := len(xs)
	if n == 0 {
		return 0, 0, false
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	k := 0
	cdf := 0.0 // P(B ≤ j-1) for the j being tried
	for j := 1; j <= n/2; j++ {
		cdf += binomHalf(n, j-1)
		if 1-2*cdf < conf {
			break
		}
		k = j
	}
	if k == 0 {
		return 0, 0, false
	}
	return s[k-1], s[n-k], true
}

// binomHalf returns P(B = i) for B binomial(n, 1/2).
func binomHalf(n, i int) float64 {
	lg := func(x int) float64 { v, _ := math.Lgamma(float64(x + 1)); return v }
	return math.Exp(lg(n) - lg(i) - lg(n-i) - float64(n)*math.Ln2)
}

// uDist returns the null distribution of the Mann-Whitney statistic U for samples of sizes m and n
// without ties: p[u] = P(U = u) for u = 0..m·n. It counts the arrangements by the recurrence
// c(m, n, u) = c(m-1, n, u-n) + c(m, n-1, u).
func uDist(m, n int) []float64 {
	// c[j][u] holds c(i, j, u) for the current i.
	c := make([][]float64, n+1)
	for j := range c {
		c[j] = make([]float64, m*n+1)
		c[j][0] = 1 // c(0, j, 0) = 1
	}
	for i := 1; i <= m; i++ {
		next := make([][]float64, n+1)
		next[0] = make([]float64, m*n+1)
		next[0][0] = 1 // c(i, 0, 0) = 1
		for j := 1; j <= n; j++ {
			next[j] = make([]float64, m*n+1)
			for u := 0; u <= i*j; u++ {
				v := next[j-1][u]
				if u >= j {
					v += c[j][u-j]
				}
				next[j][u] = v
			}
		}
		c = next
	}
	total := 0.0
	for _, v := range c[n] {
		total += v
	}
	p := make([]float64, m*n+1)
	for u, v := range c[n] {
		p[u] = v / total
	}
	return p
}

// exactLimit is the largest m·n for which the exact distribution of U is used; larger samples use
// the normal approximation.
const exactLimit = 2500

// mannWhitney returns the statistic U of a (the pairs (x, y) with x from a above y from b, a tie
// counting one half) and the two-sided p-value of the hypothesis that a and b come from one
// distribution: exact when the samples have no ties and are small, else from the normal
// approximation with the tie and continuity corrections.
func mannWhitney(a, b []float64) (u, p float64) {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return 0, 1
	}
	ties := false
	for _, x := range a {
		for _, y := range b {
			switch {
			case x > y:
				u++
			case x == y:
				u += 0.5
				ties = true
			}
		}
	}
	all := append(slices.Clone(a), b...)
	slices.Sort(all)
	for i := 1; i < len(all); i++ {
		if all[i] == all[i-1] {
			ties = true
		}
	}
	if !ties && m*n <= exactLimit {
		d := uDist(m, n)
		small := min(u, float64(m*n)-u)
		cdf := 0.0
		for v := 0; float64(v) <= small; v++ {
			cdf += d[v]
		}
		return u, min(1, 2*cdf)
	}
	// Normal approximation; the variance is reduced by the ties: Σ(t³ - t) over the groups of
	// equal values.
	N := float64(m + n)
	tieSum := 0.0
	for i := 0; i < len(all); {
		j := i
		for j < len(all) && all[j] == all[i] {
			j++
		}
		t := float64(j - i)
		tieSum += t*t*t - t
		i = j
	}
	mf, nf := float64(m), float64(n)
	mean := mf * nf / 2
	sd := math.Sqrt(mf * nf / 12 * ((N + 1) - tieSum/(N*(N-1))))
	if sd == 0 {
		return u, 1
	}
	z := (math.Abs(u-mean) - 0.5) / sd
	if z < 0 {
		z = 0
	}
	return u, min(1, math.Erfc(z/math.Sqrt2))
}

// ratioCI returns the ratio of the median of b to the median of a and its distribution-free
// confidence interval: the interval of the shift of log b against log a that inverts the
// Mann-Whitney test (the order statistics D(k) and D(m·n+1-k) of the m·n differences
// log y - log x, k the largest with P(U ≤ k-1) ≤ (1-conf)/2), mapped back to a ratio. ok is false
// when the samples are too small for any k, or hold a value that is not positive.
func ratioCI(a, b []float64, conf float64) (ratio, lo, hi float64, ok bool) {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return 0, 0, 0, false
	}
	for _, v := range append(slices.Clone(a), b...) {
		if v <= 0 {
			return 0, 0, 0, false
		}
	}
	ratio = median(b) / median(a)
	alpha := (1 - conf) / 2
	k := 0
	if m*n <= exactLimit {
		d := uDist(m, n)
		cdf := 0.0
		for j := 1; j <= m*n/2; j++ {
			cdf += d[j-1]
			if cdf > alpha {
				break
			}
			k = j
		}
	} else {
		mf, nf := float64(m), float64(n)
		z := normalQuantile(1 - alpha)
		k = int(math.Floor(mf*nf/2 - z*math.Sqrt(mf*nf*(mf+nf+1)/12)))
	}
	if k < 1 {
		return ratio, 0, 0, false
	}
	diffs := make([]float64, 0, m*n)
	for _, x := range a {
		for _, y := range b {
			diffs = append(diffs, math.Log(y)-math.Log(x))
		}
	}
	slices.Sort(diffs)
	return ratio, math.Exp(diffs[k-1]), math.Exp(diffs[m*n-k]), true
}

// normalQuantile returns the p-quantile of the standard normal distribution.
func normalQuantile(p float64) float64 { return math.Sqrt2 * math.Erfinv(2*p-1) }
