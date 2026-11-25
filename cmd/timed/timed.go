package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/syntaxir/bench/internal/probe"
	"github.com/syntaxir/bench/internal/workloads"
)

// The timed captures run every measurement in arms: head (the checkout of SyntaxIR), control (a byte-identical
// copy of head: its differences from head are the noise of the machine, the A/A spread) and, with
// -base, base (the same measurement built from another revision). Each round runs every unit (a
// top-level benchmark, a TypeScript workload and entry point) once in every arm, each run a fresh
// process, with the order of the arms rotated by round and unit, so that drift of the machine falls
// on every arm alike. The results record the arm and the round of each run as configuration lines
// ("arm: control", "round: 3"), in the format of go test -bench (benchfmt), so benchstat -col arm
// reads them as they are.

// Arm names.
const (
	armHead    = "head"
	armControl = "control"
	armBase    = "base"
)

// aaLimit is the largest A/A spread that a run may show: a capture whose geomean of the control's
// medians over head's is further than this from 1 is rejected, and a row whose own A/A ratio is
// further is noisy and cannot be cited.
const aaLimit = 0.015

// timedConfig is what a timed capture runs.
type timedConfig struct {
	harness   string // the root of this repository
	root      string // the checkout of SyntaxIR measured as head
	baseRoot  string // a checkout of the base revision; empty without -base
	baseRev   string
	work      string
	rounds    int
	benchtime string
	bench     string // the -bench pattern
	grammars  string // the -grammars of the matrix part, comma-separated

	sxirBins  map[string]string   // the sxir command built for an arm, by arm
	workloads *workloads.Manifest // head's workload manifest, once exported
}

// sxir returns the sxir command of an arm's checkout, built once.
func (c *timedConfig) sxir(arm string) (string, error) {
	if b, ok := c.sxirBins[arm]; ok {
		return b, nil
	}
	dir := filepath.Join(c.work, "sxir-"+arm)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := probe.BuildSXIR(c.srcRoot(arm), dir)
	if err != nil {
		return "", fmt.Errorf("building sxir for %s: %w", arm, err)
	}
	if c.sxirBins == nil {
		c.sxirBins = map[string]string{}
	}
	c.sxirBins[arm] = b
	return b, nil
}

// manifest returns head's workload manifest, exported once into the work directory.
func (c *timedConfig) manifest() (*workloads.Manifest, error) {
	if c.workloads != nil {
		return c.workloads, nil
	}
	m, err := workloads.Export(c.root, filepath.Join(c.work, "workloads"))
	if err != nil {
		return nil, err
	}
	c.workloads = m
	return m, nil
}

// arms returns the arms of the capture in their unrotated order.
func (c *timedConfig) arms() []string {
	if c.baseRoot != "" {
		return []string{armHead, armControl, armBase}
	}
	return []string{armHead, armControl}
}

// srcRoot returns the checkout that an arm's binaries are built from and run in.
func (c *timedConfig) srcRoot(arm string) string {
	if arm == armBase {
		return c.baseRoot
	}
	return c.root
}

// rotated returns the arms starting at index i modulo their number.
func rotated(arms []string, i int) []string {
	k := i % len(arms)
	return append(slices.Clone(arms[k:]), arms[:k]...)
}

// goSuite is a set of Go benchmarks: a package, the pattern of its benchmarks and the pattern of
// the top-level benchmarks to leave out (empty for none).
type goSuite struct {
	pkg, pattern, exclude string
	// wedge marks the package of the wedge module of this repository, which is built against the
	// checkout of each arm (see prepareWedge); pkg is then ".".
	wedge bool
}

// dir returns the directory of a suite's package for an arm; the control arm runs in head's.
func (c *timedConfig) suiteDir(arm string, s goSuite) string {
	if s.wedge {
		if arm == armControl {
			arm = armHead
		}
		return filepath.Join(c.work, "wedge", arm)
	}
	return filepath.Join(c.srcRoot(arm), filepath.FromSlash(s.pkg))
}

// suiteEnv returns the environment that builds a suite's package for an arm.
func (c *timedConfig) suiteEnv(arm string, s goSuite) []string {
	if s.wedge {
		return []string{"GOWORK=" + filepath.Join(c.suiteDir(arm, s), "go.work")}
	}
	return nil
}

// importPath returns the import path of a suite's package, as go test prints it.
func (s goSuite) importPath() string {
	if s.wedge {
		return wedgeModule
	}
	return "syntaxir.dev/sxir/" + strings.TrimPrefix(strings.TrimPrefix(s.pkg, "./"), "/")
}

// goUnit is a top-level benchmark of a suite, with the pattern of its sub-benchmarks to run.
type goUnit struct {
	suite   goSuite
	name    string
	pattern string // the -test.bench pattern of the run
}

// testBinaries builds the test binary of every suite's package for every arm: head's and base's from
// their checkouts, control's as a copy of head's. It returns the paths by arm and package.
func testBinaries(c *timedConfig, suites []goSuite) (map[string]map[string]string, error) {
	bins := map[string]map[string]string{}
	for _, arm := range c.arms() {
		bins[arm] = map[string]string{}
		dir := filepath.Join(c.work, "bin", arm)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		for _, s := range suites {
			if _, ok := bins[arm][s.pkg]; ok {
				continue
			}
			out := filepath.Join(dir, strings.NewReplacer("/", "_", ".", "_").Replace(s.importPath())+".test")
			if arm == armControl {
				if err := copyFile(bins[armHead][s.pkg], out); err != nil {
					return nil, err
				}
			} else {
				fmt.Fprintf(os.Stderr, "building %s for %s\n", s.importPath(), arm)
				if _, err := execOut(c.suiteDir(arm, s), c.suiteEnv(arm, s), "go", "test", "-c", "-o", out, "."); err != nil {
					return nil, fmt.Errorf("building the %s test binary of %s: %w", arm, s.pkg, err)
				}
			}
			bins[arm][s.pkg] = out
		}
	}
	return bins, nil
}

func copyFile(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, data, 0o755)
}

// splitPattern splits a -bench pattern at the slashes that are outside brackets and parentheses, as
// go test does: each part matches one level of the benchmark names.
func splitPattern(p string) []string {
	var parts []string
	depth, cs, start := 0, false, 0
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '[':
			cs = true
		case ']':
			cs = false
		case '(':
			if !cs {
				depth++
			}
		case ')':
			if !cs {
				depth--
			}
		case '\\':
			i++
		case '/':
			if depth == 0 && !cs {
				parts = append(parts, p[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, p[start:])
}

// goUnits lists the top-level benchmarks of the suites that the -bench pattern selects, from head's
// binaries. A unit runs the benchmark alone, with the rest of the pattern for its sub-benchmarks.
func goUnits(c *timedConfig, suites []goSuite, bins map[string]map[string]string) ([]goUnit, error) {
	parts := splitPattern(c.bench)
	top, err := regexp.Compile(parts[0])
	if err != nil {
		return nil, fmt.Errorf("-bench: %w", err)
	}
	rest := ""
	if len(parts) > 1 {
		rest = "/" + strings.Join(parts[1:], "/")
	}
	var units []goUnit
	for _, s := range suites {
		var exclude *regexp.Regexp
		if s.exclude != "" {
			exclude = regexp.MustCompile(s.exclude)
		}
		out, err := execOut(c.suiteDir(armHead, s), nil, bins[armHead][s.pkg], "-test.list", s.pattern)
		if err != nil {
			return nil, fmt.Errorf("listing the benchmarks of %s: %w", s.importPath(), err)
		}
		for _, name := range strings.Fields(out) {
			if !strings.HasPrefix(name, "Benchmark") || !top.MatchString(name) || exclude != nil && exclude.MatchString(name) {
				continue
			}
			units = append(units, goUnit{suite: s, name: name, pattern: "^" + regexp.QuoteMeta(name) + "$" + rest})
		}
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("-bench %q selects no benchmark", c.bench)
	}
	return units, nil
}

// runGoSuites runs the units of the suites in every arm for the rounds of c, and returns the results
// in benchfmt: the configuration of go test (goos, goarch, cpu) once, then each run's results after
// its pkg, arm and round. It also returns the SHA-256 of each arm's binaries.
func runGoSuites(c *timedConfig, suites []goSuite) (body []byte, hashes map[string]string, err error) {
	bins, err := testBinaries(c, suites)
	if err != nil {
		return nil, nil, err
	}
	hashes = map[string]string{}
	for arm, byPkg := range bins {
		var pkgs []string
		for p := range byPkg {
			pkgs = append(pkgs, p)
		}
		slices.Sort(pkgs)
		var hs []string
		for _, p := range pkgs {
			h, err := fileHash(byPkg[p])
			if err != nil {
				return nil, nil, err
			}
			hs = append(hs, p+"="+h)
		}
		hashes[arm] = strings.Join(hs, " ")
	}
	units, err := goUnits(c, suites, bins)
	if err != nil {
		return nil, nil, err
	}
	var b bytes.Buffer
	configured := false
	for round := 1; round <= c.rounds; round++ {
		for i, u := range units {
			for _, arm := range rotated(c.arms(), round+i) {
				fmt.Fprintf(os.Stderr, "round %d/%d %s %s\n", round, c.rounds, arm, u.name)
				cmd := exec.Command(bins[arm][u.suite.pkg], "-test.run", "^$", "-test.bench", u.pattern,
					"-test.benchmem", "-test.count", "1", "-test.benchtime", c.benchtime, "-test.timeout", "0")
				cmd.Dir = c.suiteDir(arm, u.suite)
				var out bytes.Buffer
				cmd.Stdout = &out
				cmd.Stderr = os.Stderr
				if err := cmd.Run(); err != nil {
					return nil, nil, fmt.Errorf("%s in %s, round %d: %w\n%s", u.name, arm, round, err, out.String())
				}
				var lines []string
				sc := bufio.NewScanner(&out)
				sc.Buffer(make([]byte, 1<<20), 1<<20)
				for sc.Scan() {
					l := sc.Text()
					switch {
					case strings.HasPrefix(l, "Benchmark"):
						lines = append(lines, l)
					case !configured && (strings.HasPrefix(l, "goos: ") || strings.HasPrefix(l, "goarch: ") || strings.HasPrefix(l, "cpu: ")):
						fmt.Fprintln(&b, l)
					}
				}
				configured = true
				if len(lines) == 0 && arm == armBase {
					// The unit is newer than base: its rows are head's and control's alone, and the
					// analysis marks them as not in base.
					continue
				}
				if len(lines) == 0 {
					return nil, nil, fmt.Errorf("%s in %s, round %d: no results\n%s", u.name, arm, round, out.String())
				}
				fmt.Fprintf(&b, "pkg: %s\narm: %s\nround: %d\n", u.suite.importPath(), arm, round)
				for _, l := range lines {
					fmt.Fprintln(&b, l)
				}
			}
		}
	}
	return b.Bytes(), hashes, nil
}

// matchLevels reports whether a -bench pattern selects a row name, level by level as go test does:
// each part of the pattern matches the level of the name at its place, and the name may be deeper.
func matchLevels(pattern, name string) (bool, error) {
	levels := strings.Split(name, "/")
	for i, part := range splitPattern(pattern) {
		if i >= len(levels) {
			break
		}
		re, err := regexp.Compile(part)
		if err != nil {
			return false, fmt.Errorf("-bench: %w", err)
		}
		if !re.MatchString(levels[i]) {
			return false, nil
		}
	}
	return true, nil
}

// armSamples reads the runs of results in benchfmt by arm: the values of one unit (ns/op by default)
// by benchmark name, in the order of the file. A file without arm lines is all head.
func armSamples(data []byte, unit string) (samples map[string]map[string][]float64, order []string) {
	samples = map[string]map[string][]float64{}
	seen := map[string]bool{}
	arm := armHead
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "arm: "); ok {
			arm = strings.TrimSpace(v)
			continue
		}
		if !strings.HasPrefix(line, "Benchmark") {
			continue
		}
		f := strings.Fields(line)
		name := savedLabel(procsSuffix.ReplaceAllString(strings.TrimPrefix(f[0], "Benchmark"), ""))
		for i := 2; i+1 < len(f); i += 2 {
			if f[i+1] != unit {
				continue
			}
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				continue
			}
			if samples[arm] == nil {
				samples[arm] = map[string][]float64{}
			}
			samples[arm][name] = append(samples[arm][name], v)
			if !seen[name] {
				seen[name] = true
				order = append(order, name)
			}
		}
	}
	return samples, order
}

// verdict names what a row lets a reader claim.
type verdict string

const (
	verdictNoisy     verdict = "noisy: no claim"          // the row's own A/A spread exceeds aaLimit
	verdictFew       verdict = "too few rounds: no claim" // no interval at the confidence level
	verdictSame      verdict = "no difference"            // the interval of the ratio holds 1
	verdictInSpread  verdict = "within A/A spread"        // the effect is not larger than the row's A/A spread
	verdictNotInBase verdict = "not in base: no claim"    // base has no such benchmark: head and control only
)

// row is the analysis of one benchmark of a timed capture.
type row struct {
	name            string
	head, lo, hi    float64 // head's median and its interval
	headCI          bool
	aa              float64 // median(control) / median(head)
	noisy           bool
	hasBase         bool
	ratio, rLo, rHi float64 // median(head) / median(base) and its interval
	ratioCI         bool
	p               float64 // Mann-Whitney p-value of head against base
	verdict         verdict
}

// analysis is the A/A judgment of a timed capture and, with a base, the A/B rows.
type analysis struct {
	rows     []row
	geomean  float64 // geomean of the A/A ratios
	rejected bool
	// distribution is set for the analysis of a percentile unit, which judges its rows but never
	// rejects the capture: the capture stands or falls by its mean times.
	distribution bool
}

// percentileUnits are the units of the benchmarks that report the distribution of their operations
// (the Document edit latencies) besides the mean.
var percentileUnits = []string{"p50-ns/op", "p99-ns/op"}

// analyzeDistributions analyzes the percentile units of a Go capture that has them, one analysis
// per unit, each judging its rows without rejecting the capture.
func analyzeDistributions(body []byte) ([]*analysis, []string, error) {
	var as []*analysis
	var units []string
	for _, u := range percentileUnits {
		samples, order := armSamples(body, u)
		if len(order) == 0 {
			continue
		}
		a, err := analyze(samples, order)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", u, err)
		}
		a.distribution, a.rejected = true, false
		as, units = append(as, a), append(units, u)
	}
	return as, units, nil
}

// analyze compares the arms of a capture. The capture is rejected when the geomean of the rows' A/A
// ratios is further than aaLimit from 1. A row is noisy when its own A/A ratio is. A claim on a row
// against base (its verdict) needs the row not noisy, an interval of the ratio that does not hold
// 1, and an effect larger than the row's own A/A spread.
func analyze(samples map[string]map[string][]float64, order []string) (*analysis, error) {
	head, control, base := samples[armHead], samples[armControl], samples[armBase]
	if len(control) == 0 {
		return nil, fmt.Errorf("the results hold no control arm")
	}
	a := &analysis{}
	logSum, n := 0.0, 0
	for _, name := range order {
		h, c := head[name], control[name]
		if len(h) == 0 || len(c) == 0 {
			return nil, fmt.Errorf("%s: missing in the head or control arm", name)
		}
		r := row{name: name, head: median(h)}
		r.lo, r.hi, r.headCI = medianCI(h, confidence)
		r.aa = median(c) / r.head
		r.noisy = math.Abs(r.aa-1) > aaLimit
		logSum += math.Log(r.aa)
		n++
		if b := base[name]; len(base) > 0 && len(b) == 0 {
			r.verdict = verdictNotInBase
		} else if len(b) > 0 {
			r.hasBase = true
			r.ratio, r.rLo, r.rHi, r.ratioCI = ratioCI(b, h, confidence)
			_, r.p = mannWhitney(h, b)
			switch spread := math.Abs(r.aa - 1); {
			case r.noisy:
				r.verdict = verdictNoisy
			case !r.ratioCI:
				r.verdict = verdictFew
			case r.rLo <= 1 && 1 <= r.rHi:
				r.verdict = verdictSame
			case math.Abs(r.ratio-1) <= spread:
				r.verdict = verdictInSpread
			case r.ratio < 1:
				r.verdict = verdict(fmt.Sprintf("faster by %.1f%%", (1-r.ratio)*100))
			default:
				r.verdict = verdict(fmt.Sprintf("slower by %.1f%%", (r.ratio-1)*100))
			}
		}
		a.rows = append(a.rows, r)
	}
	a.geomean = math.Exp(logSum / float64(n))
	a.rejected = math.Abs(a.geomean-1) > aaLimit
	return a, nil
}

// summary returns the lines that the header of the results records about the analysis.
func (a *analysis) summary() []string {
	noisy := 0
	for _, r := range a.rows {
		if r.noisy {
			noisy++
		}
	}
	state := "accepted"
	if a.rejected {
		state = "rejected"
	} else if a.distribution {
		state = "rows judged alone: a percentile does not reject the capture"
	}
	return []string{
		fmt.Sprintf("aa-geomean: %.4f (%+.2f%%, limit ±%.1f%%, %s)", a.geomean, (a.geomean-1)*100, aaLimit*100, state),
		fmt.Sprintf("aa-noisy: %d of %d rows", noisy, len(a.rows)),
	}
}

// write prints the analysis as a table: per row head's median and interval, the A/A ratio and, with
// a base, the ratio to base with its interval, the p-value and the verdict.
func (a *analysis) write(w io.Writer, title, unit string) {
	fmt.Fprintf(w, "## %s\n\n", title)
	for _, l := range a.summary() {
		fmt.Fprintln(w, l)
	}
	fmt.Fprintln(w)
	hasBase := slices.ContainsFunc(a.rows, func(r row) bool { return r.hasBase })
	fmt.Fprintf(w, "%-60s %14s %27s %8s", "benchmark", "head "+unit, "95% CI", "A/A")
	if hasBase {
		fmt.Fprintf(w, " %8s %19s %7s  %s", "head/base", "95% CI", "p", "verdict")
	}
	fmt.Fprintln(w)
	for _, r := range a.rows {
		ci := "–"
		if r.headCI {
			ci = fmt.Sprintf("[%s, %s]", fmtSig(r.lo, 4), fmtSig(r.hi, 4))
		}
		aa := fmt.Sprintf("%.4f", r.aa)
		if r.noisy {
			aa += "!"
		}
		fmt.Fprintf(w, "%-60s %14s %27s %8s", r.name, fmtSig(r.head, 4), ci, aa)
		if hasBase && !r.hasBase {
			fmt.Fprintf(w, " %8s %19s %7s  %s", "–", "–", "–", r.verdict)
		}
		if r.hasBase {
			rci := "–"
			if r.ratioCI {
				rci = fmt.Sprintf("[%.4f, %.4f]", r.rLo, r.rHi)
			}
			fmt.Fprintf(w, " %8.4f %19s %7.3f  %s", r.ratio, rci, r.p, r.verdict)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
}

// captureMatrix runs cmd/profilematrix on the rows of head's manifest with their options as the
// baseline (head) and its control copy, in the rounds and benchtime of c, with the sizes of the
// TypeScript parsers, and returns its output: a "cell GRAMMAR VARIANT METRIC VALUE" line per value
// (ns_op the minimum over the rounds; b_op, allocs_op, retained, rss, gc_share, src_bytes, bin_bytes,
// compile_ms, ts_raw, ts_gzip), then its table of ratios.
func captureMatrix(c *timedConfig) ([]byte, error) {
	m, err := c.manifest()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("go", "run", "./cmd/profilematrix", "-variant", armHead+"=", "-control",
		"-rounds", strconv.Itoa(c.rounds), "-benchtime", c.benchtime, "-ts", "-root", c.root, "-manifest", m.Dir)
	if c.grammars != "" {
		cmd.Args = append(cmd.Args, "-grammars", c.grammars)
	}
	cmd.Dir = c.harness
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	fmt.Fprintf(os.Stderr, "running %s\n", strings.Join(cmd.Args, " "))
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running profilematrix: %w", err)
	}
	return out.Bytes(), nil
}

// matrixSamples reads the sample lines of profilematrix's output, the time of every round, as the
// samples of the head and control arms by grammar, the grammars in the order of their first sample.
func matrixSamples(data []byte) (map[string]map[string][]float64, []string) {
	samples := map[string]map[string][]float64{}
	var order []string
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) != 6 || f[0] != "sample" || f[4] != "ns_op" {
			continue
		}
		v, err := strconv.ParseFloat(f[5], 64)
		if err != nil {
			continue
		}
		if samples[f[2]] == nil {
			samples[f[2]] = map[string][]float64{}
		}
		if f[2] == armHead && !slices.Contains(order, f[1]) {
			order = append(order, f[1])
		}
		samples[f[2]][f[1]] = append(samples[f[2]][f[1]], v)
	}
	return samples, order
}
