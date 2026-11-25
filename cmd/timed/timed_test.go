package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// armResults returns benchfmt results of two benchmarks in rounds, with each arm's ns/op a factor
// of a base value that grows a little with the round: scale[arm][bench].
func armResults(rounds int, scale map[string]map[string]float64) string {
	var b strings.Builder
	b.WriteString("# commit: abc\ngoos: darwin\ngoarch: arm64\ncpu: test\n")
	for r := 1; r <= rounds; r++ {
		for _, arm := range []string{armHead, armControl, armBase} {
			if scale[arm] == nil {
				continue
			}
			fmt.Fprintf(&b, "pkg: syntaxir.dev/sxir/bench\narm: %s\nround: %d\n", arm, r)
			for _, name := range []string{"Parse/A", "Parse/B"} {
				ns := 1000 * scale[arm][name] * (1 + float64(r)/1000)
				fmt.Fprintf(&b, "Benchmark%s-16 \t 100\t %.1f ns/op\t 10 B/op\t 1 allocs/op\n", name, ns)
			}
		}
	}
	return b.String()
}

func TestRotatedArms(t *testing.T) {
	arms := []string{"a", "b", "c"}
	for i, want := range [][]string{{"a", "b", "c"}, {"b", "c", "a"}, {"c", "a", "b"}, {"a", "b", "c"}} {
		if got := rotated(arms, i); !slices.Equal(got, want) {
			t.Errorf("rotated(%d) = %v, want %v", i, got, want)
		}
	}
}

func TestSplitPattern(t *testing.T) {
	for p, want := range map[string][]string{
		".":                    {"."},
		"Parse/JSON":           {"Parse", "JSON"},
		"Parse/(JSON|CSV)/a/b": {"Parse", "(JSON|CSV)", "a", "b"},
		"X[/]Y/Z":              {"X[/]Y", "Z"},
		"(A/B)/C":              {"(A/B)", "C"},
	} {
		if got := splitPattern(p); !slices.Equal(got, want) {
			t.Errorf("splitPattern(%q) = %q, want %q", p, got, want)
		}
	}
}

// The document is rendered from the head arm alone; the samples of every arm are read for the
// analysis.
func TestArmsSeparate(t *testing.T) {
	data := armResults(3, map[string]map[string]float64{
		armHead:    {"Parse/A": 1, "Parse/B": 2},
		armControl: {"Parse/A": 5, "Parse/B": 5},
	})
	r, err := parseResults([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.bench["Parse/A"]; got.runs != 3 || got.ns > 1010 {
		t.Errorf("head of Parse/A: %d runs, median %v; want 3 runs near 1000", got.runs, got.ns)
	}
	s, order := armSamples([]byte(data), "ns/op")
	if !slices.Equal(order, []string{"Parse/A", "Parse/B"}) || len(s[armHead]["Parse/A"]) != 3 || len(s[armControl]["Parse/B"]) != 3 {
		t.Errorf("samples: order %v, %v", order, s)
	}
}

// A capture whose geomean A/A ratio stays within the limit is accepted, and one past it rejected; a
// row whose own A/A ratio is past it is noisy whatever the geomean.
func TestAnalyzeRejectsBySpread(t *testing.T) {
	for _, c := range []struct {
		control          map[string]float64
		rejected, noisyA bool
	}{
		{map[string]float64{"Parse/A": 1.001, "Parse/B": 0.999}, false, false},
		{map[string]float64{"Parse/A": 1.03, "Parse/B": 0.99}, false, true}, // geomean ≈ 1.0097
		{map[string]float64{"Parse/A": 1.02, "Parse/B": 1.02}, true, true},
	} {
		data := armResults(10, map[string]map[string]float64{
			armHead:    {"Parse/A": 1, "Parse/B": 1},
			armControl: c.control,
		})
		a, err := analyze(armSamples([]byte(data), "ns/op"))
		if err != nil {
			t.Fatal(err)
		}
		if a.rejected != c.rejected || a.rows[0].noisy != c.noisyA {
			t.Errorf("control %v: rejected %v, Parse/A noisy %v; want %v, %v (geomean %v)", c.control, a.rejected, a.rows[0].noisy, c.rejected, c.noisyA, a.geomean)
		}
		if !strings.Contains(strings.Join(a.summary(), "\n"), map[bool]string{true: "rejected", false: "accepted"}[c.rejected]) {
			t.Errorf("summary %q does not state the judgment", a.summary())
		}
	}
	if _, err := analyze(armSamples([]byte(armResults(3, map[string]map[string]float64{armHead: {"Parse/A": 1, "Parse/B": 1}})), "ns/op")); err == nil {
		t.Error("results without a control arm: no error")
	}
}

// An A/B claim on a row needs the row not noisy, an interval of the ratio that does not hold 1, and
// an effect larger than the row's own A/A spread.
func TestAnalyzeVerdicts(t *testing.T) {
	interleaved := func(rounds int, head, control, base []float64) string {
		var b strings.Builder
		for r := 0; r < rounds; r++ {
			for arm, v := range map[string][]float64{armHead: head, armControl: control, armBase: base} {
				fmt.Fprintf(&b, "arm: %s\nBenchmarkX-16 1 %v ns/op 0 B/op 0 allocs/op\n", arm, v[r])
			}
		}
		return b.String()
	}
	seq := func(start, step float64) []float64 {
		var xs []float64
		for i := 0; i < 10; i++ {
			xs = append(xs, start+step*float64(i))
		}
		return xs
	}
	for _, c := range []struct {
		name                string
		head, control, base []float64
		want                string
	}{
		{"faster", seq(900, 1), seq(900.5, 1), seq(1000, 1), "faster by 10.0%"},
		{"slower", seq(1100, 1), seq(1100.5, 1), seq(1000, 1), "slower by 10.0%"},
		{"no difference", seq(1000, 1), seq(1000.5, 1), seq(1000.5, 1), string(verdictSame)},
		{"noisy", seq(900, 1), seq(950, 1), seq(1000, 1), string(verdictNoisy)},
		// A 1% effect, clear of 1, but the row's control differs from head by 1.2%.
		{"within the spread", seq(1000, 0.1), seq(1012, 0.1), seq(1010, 0.1), string(verdictInSpread)},
	} {
		a, err := analyze(armSamples([]byte(interleaved(10, c.head, c.control, c.base)), "ns/op"))
		if err != nil {
			t.Fatal(err)
		}
		if got := string(a.rows[0].verdict); !strings.HasPrefix(got, c.want) {
			t.Errorf("%s: verdict %q, want %q (row %+v)", c.name, got, c.want, a.rows[0])
		}
		var w strings.Builder
		a.write(&w, "t", "ns/op")
		if !strings.Contains(w.String(), string(a.rows[0].verdict)) {
			t.Errorf("%s: the table does not print the verdict:\n%s", c.name, w.String())
		}
	}
	few := interleaved(2, seq(900, 1), seq(900, 1), seq(1000, 1))
	a, err := analyze(armSamples([]byte(few), "ns/op"))
	if err != nil {
		t.Fatal(err)
	}
	if a.rows[0].verdict != verdictFew {
		t.Errorf("two rounds: verdict %q, want %q", a.rows[0].verdict, verdictFew)
	}
}

// A benchmark newer than base has rows of head and control only: they are judged A/A and marked
// as not in base, with no ratio, while the other rows compare with base; a row missing from head or
// control fails the analysis.
func TestAnalyzeNotInBase(t *testing.T) {
	var b strings.Builder
	for r := 0; r < 10; r++ {
		fmt.Fprintf(&b, "arm: head\nBenchmarkOld-16 1 %d ns/op\nBenchmarkNew-16 1 %d ns/op\n", 1000+r, 500+r)
		fmt.Fprintf(&b, "arm: control\nBenchmarkOld-16 1 %d ns/op\nBenchmarkNew-16 1 %d ns/op\n", 1000+r, 500+r)
		fmt.Fprintf(&b, "arm: base\nBenchmarkOld-16 1 %d ns/op\n", 1000+r)
	}
	a, err := analyze(armSamples([]byte(b.String()), "ns/op"))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]row{}
	for _, r := range a.rows {
		byName[r.name] = r
	}
	if r := byName["Old"]; !r.hasBase || r.verdict != verdictSame {
		t.Errorf("Old: %+v", r)
	}
	if r := byName["New"]; r.hasBase || r.verdict != verdictNotInBase || r.aa != 1 {
		t.Errorf("New: %+v", r)
	}
	if a.rejected || len(a.rows) != 2 {
		t.Errorf("analysis: rejected %v, %d rows", a.rejected, len(a.rows))
	}
	var w strings.Builder
	a.write(&w, "t", "ns/op")
	if !strings.Contains(w.String(), string(verdictNotInBase)) {
		t.Errorf("the table does not mark the new row:\n%s", w.String())
	}
	// Without the control's run of New the analysis fails.
	if _, err := analyze(armSamples([]byte(strings.ReplaceAll(b.String(), "arm: control\nBenchmarkOld-16", "arm: control\nBenchmarkOnly-16")), "ns/op")); err == nil {
		t.Error("a row missing from control was analyzed")
	}
}

func TestAnalyzeDistributions(t *testing.T) {
	// The p99 of control is 10% above head's, far beyond the A/A limit; the mean is equal.
	var b strings.Builder
	for r := 0; r < 10; r++ {
		fmt.Fprintf(&b, "arm: head\nBenchmarkEdit-16 1 %d ns/op %d p50-ns/op %d p99-ns/op\nBenchmarkParse-16 1 %d ns/op\n", 1000+r, 900+r, 2000+r, 700+r)
		fmt.Fprintf(&b, "arm: control\nBenchmarkEdit-16 1 %d ns/op %d p50-ns/op %d p99-ns/op\nBenchmarkParse-16 1 %d ns/op\n", 1000+r, 900+r, 2200+r, 700+r)
	}
	body := []byte(b.String())
	a, err := analyze(armSamples(body, "ns/op"))
	if err != nil || a.rejected {
		t.Fatalf("the mean analysis: %v, rejected %v", err, a != nil && a.rejected)
	}
	dists, units, err := analyzeDistributions(body)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(units, percentileUnits) || len(dists) != 2 {
		t.Fatalf("units %v, %d analyses", units, len(dists))
	}
	for i, d := range dists {
		if d.rejected || !d.distribution || len(d.rows) != 1 || d.rows[0].name != "Edit" {
			t.Errorf("%s: rejected %v, distribution %v, rows %+v", units[i], d.rejected, d.distribution, d.rows)
		}
	}
	if p99 := dists[1].rows[0]; !p99.noisy {
		t.Errorf("p99: the row with a 10%% A/A spread is not noisy: %+v", p99)
	}
	var w strings.Builder
	dists[1].write(&w, "t", "p99-ns/op")
	if !strings.Contains(w.String(), "a percentile does not reject the capture") {
		t.Errorf("the table does not say the unit judges rows only:\n%s", w.String())
	}
	// A capture without percentile units has no distribution analyses.
	if dists, _, err := analyzeDistributions([]byte(armResults(3, map[string]map[string]float64{
		armHead: {"Parse/A": 1, "Parse/B": 1}, armControl: {"Parse/A": 1, "Parse/B": 1}}))); err != nil || len(dists) != 0 {
		t.Errorf("without percentiles: %d analyses, %v", len(dists), err)
	}
}

func TestMatrixSamples(t *testing.T) {
	// The cells hold the minimum of the rounds; the samples are every round's time, and only they are read.
	out := "cell json head ns_op 100\ncell json control ns_op 101\ncell json head rss 5\n" +
		"sample json head 1 ns_op 100\nsample json control 1 ns_op 103\nsample csv control 1 ns_op 50\nsample csv head 1 ns_op 51\n" +
		"sample json control 2 ns_op 101\nsample json head 2 ns_op 102\nsample json head 2 rss 5\nratio table\n"
	s, order := matrixSamples([]byte(out))
	if !slices.Equal(order, []string{"json", "csv"}) || !slices.Equal(s[armControl]["json"], []float64{103, 101}) ||
		!slices.Equal(s[armHead]["json"], []float64{100, 102}) || len(s[armHead]) != 2 {
		t.Errorf("got %v %v", order, s)
	}
}

// A timed capture needs the exclusive lock, held by the process's benchlock.sh, and an open window;
// -force measures without them and says so.
func TestTimedLock(t *testing.T) {
	state := t.TempDir()
	t.Setenv("SXIR_AGENT_STATE", state)
	t.Setenv("SXIR_BENCHLOCK", "")
	if l, err := timedLock(true); err != nil || !strings.Contains(l, "unlocked") {
		t.Errorf("-force: %q, %v", l, err)
	}
	if _, err := timedLock(false); err == nil {
		t.Error("outside the lock: no error")
	}
	t.Setenv("SXIR_BENCHLOCK", "bench 123")
	write := func(name, content string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(state, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("benchlock/owner", "other 456\n")
	if _, err := timedLock(false); err == nil {
		t.Error("the lock held by another job: no error")
	}
	write("benchlock/owner", "bench 123\n")
	if _, err := timedLock(false); err == nil || !strings.Contains(err.Error(), "window") {
		t.Errorf("no window: %v", err)
	}
	write("window", fmt.Sprintf("id=w1 owner=lead start=1 until=%d reason=quiet run\n", time.Now().Add(-time.Minute).Unix()))
	if _, err := timedLock(false); err == nil {
		t.Error("an expired window: no error")
	}
	write("window", fmt.Sprintf("id=w1 owner=lead start=1 until=%d reason=quiet run\n", time.Now().Add(time.Hour).Unix()))
	l, err := timedLock(false)
	if err != nil || !strings.Contains(l, "exclusive bench 123") || !strings.Contains(l, "window w1") || !strings.Contains(l, "quiet run") {
		t.Errorf("held: %q, %v", l, err)
	}
}

// Every header records the provenance of the run.
func TestProvenanceHeader(t *testing.T) {
	p := newProvenance("abc", "exclusive x 1", 10, "1s", []string{armHead, armControl}, "")
	p.source, p.harness = "https://github.com/syntaxir/syntaxir.git, main", "def"
	p.binaries[armHead] = "bench=00"
	h := string(p.header())
	for _, k := range []string{"date", "commit", "go", "cores", "load", "load-after", "os", "gomaxprocs", "gogc",
		"gomemlimit", "goexperiment", "power", "thermal", "benchtime", "rounds", "arms", "binary-head", "source", "harness", "lock"} {
		if !strings.Contains(h, "# "+k+": ") {
			t.Errorf("header lacks %s:\n%s", k, h)
		}
	}
	if _, err := parseResults([]byte(h + "BenchmarkX-16 1 10 ns/op 0 B/op 0 allocs/op\n")); err != nil {
		t.Errorf("the header does not parse as results: %v", err)
	}
	// The load after the run is read when the samples end, and an equal value says it was not updated.
	p.loadStart, p.loadEnd = "1.00 2.00 3.00", "1.00 2.00 3.00"
	if h := string(p.header()); !strings.Contains(h, "# load-after: 1.00 2.00 3.00 (unchanged: ") {
		t.Errorf("an unchanged load is not marked:\n%s", h)
	}
	p.loadEnd = "4.00 2.50 3.10"
	if h := string(p.header()); !strings.Contains(h, "# load-after: 4.00 2.50 3.10\n") || !strings.Contains(h, "# load: 1.00 2.00 3.00\n") {
		t.Errorf("the loads are not as measured:\n%s", h)
	}
}

// The document shows the head arm: control runs added to the saved results change nothing in it
// but the sentence on the A/A judgment, which the header's analysis lines add.
func TestRenderShowsHeadAndTheAAJudgment(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(resultsDir, "results.txt"))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := render(data)
	if err != nil {
		t.Fatal(err)
	}
	var control strings.Builder
	control.WriteString("arm: control\n")
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "Benchmark") {
			f := strings.Fields(l)
			f[1] = "1"
			f[2] = "1" // 1 ns/op: far from head, so a leak into the document shows
			control.WriteString(strings.Join(f, " ") + "\n")
		}
	}
	armed, err := render(append(append([]byte("arm: head\n"), data...), control.String()...))
	if err != nil {
		t.Fatal(err)
	}
	if string(armed) != string(plain) {
		t.Error("control runs changed the document")
	}
	header := "# rounds: 20\n# gogc: 100 (default)\n# gomemlimit: off (default)\n" +
		"# aa-geomean: 1.0010 (+0.10%, limit ±1.5%, accepted)\n# aa-noisy: 2 of 300 rows\n"
	judged, err := render(append([]byte(header), data...))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(judged), "interleaved in 20 rotated rounds with an A/A control copy") ||
		!strings.Contains(string(judged), "2 of 300 rows are noisy") {
		t.Error("the document does not state the A/A judgment of the header")
	}
}

func TestMatchLevels(t *testing.T) {
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{".", "TS/Parse/JSON", true},
		{"TS/Parse/JSON", "TS/Parse/JSON", true},
		{"TS/Parse", "TS/Parse/JSON", true},
		{"TS/Parse/JSON", "TS/Parse/JSONL", true}, // unanchored, as go test matches
		{"TS/Parse/^JSON$", "TS/Parse/JSONL", false},
		{"TS/Typed", "TS/Parse/JSON", false},
		{"TS//JSON", "TS/Typed/JSON", true},
		{"Parse", "TS/Parse/JSON", false},
	} {
		got, err := matchLevels(tc.pattern, tc.name)
		if err != nil || got != tc.want {
			t.Errorf("matchLevels(%q, %q) = %v, %v; want %v", tc.pattern, tc.name, got, err, tc.want)
		}
	}
	if _, err := matchLevels("TS/(", "TS/Parse"); err == nil {
		t.Error("matchLevels accepted an invalid pattern")
	}
}

func TestRerunNames(t *testing.T) {
	o := options{parts: "go,ts,matrix,wedge", bench: "."}
	if o.anyRerun() || o.resultsName("ts", "results-ts.txt") != "results-ts.txt" {
		t.Fatal("a full capture is not a rerun")
	}
	o.bench = "TS/Parse"
	if o.resultsName("go", "results.txt") != "results-rerun.txt" || o.resultsName("ts", "results-ts.txt") != "results-ts-rerun.txt" ||
		o.resultsName("wedge", "results-wedge.txt") != "results-wedge-rerun.txt" {
		t.Error("-bench names the go, ts and wedge parts as reruns")
	}
	if o.rerun("matrix") {
		t.Error("-bench made the matrix part a rerun")
	}
	o = options{parts: "matrix", bench: "TS/Parse", grammars: "json"}
	if o.rerun("go") || o.rerun("ts") || o.resultsName("matrix", "results-matrix.txt") != "results-matrix-rerun.txt" {
		t.Error("-grammars names only the captured matrix part as a rerun")
	}
}
