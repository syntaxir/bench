// Command timed measures a SyntaxIR checkout by the timed measurement method of SyntaxIR's
// docs/performance.md, saves the results in results/ and writes RESULTS.md and figures.json from
// them, so that neither is ever edited by hand:
//
//	go run ./cmd/timed -main ../syntaxir                  # measure the checkout as it is
//	go run ./cmd/timed -main ../syntaxir -base v0.3.0     # ... and compare it with a revision
//	go run ./cmd/timed -main https://github.com/syntaxir/syntaxir.git -head main
//	go run ./cmd/timed render                             # rewrite RESULTS.md and figures.json
//	go run ./cmd/timed release -tag results-v3            # publish them as a release
//	go run ./cmd/timed pin -main ../syntaxir -tag results-v3
//
// The checkout is used through its commands, go test of its bench package and its public packages
// only: the harness builds its sxir command, runs the benchmarks of its bench package, reads the
// workloads that the package exports (internal/workloads), and builds programs against its public
// packages. -main names a local checkout or a Git URL. Without -head a local checkout is measured as
// it is, uncommitted changes included (the headers say so); with -head, and always for a URL, the
// harness checks the revision out into a temporary worktree. -base checks out another revision the
// same way. The worktrees are removed when the run ends, fails or is interrupted.
//
// A capture saves its results in results/ (or -dir), each file with a header that records the
// provenance of the run (the commits of the checkout and of this harness, toolchain, GOMAXPROCS,
// GOGC, GOMEMLIMIT, GOEXPERIMENT, the operating system, power and thermal state, benchtime, rounds,
// the hashes of the binaries, the lock and window held, and the load before and after):
//
//	results.txt          the Go benchmarks of package bench, but generation (go)
//	results-gen.txt      generation time (BenchmarkGeneration) and the run-time backend compile
//	                     (BenchmarkRuntimeBackendCompile) (gen)
//	results-ts.txt       parse, typed and recognition times of the generated TypeScript parsers under
//	                     Node (scripts/ts-parse.mjs, one fresh process per measurement) (ts)
//	results-matrix.txt   the language grammars of cmd/profilematrix: time, memory, retained heap, peak
//	                     RSS, GC share, sizes and compile time (matrix)
//	results-wedge.txt    the wedge benchmark: SyntaxIR's generated parsers and other Go parser
//	                     generators' on the same grammars (wedge)
//	results-size.txt     the sizes of the generated Go and TypeScript parsers, as the checkout's
//	                     go run ./bench/metrics collect -parts sizes reports them (size)
//	results-compare.txt  the A/A analysis of the timed parts and, with -base, the A/B verdicts
//	*-rerun.txt          the rows that -bench (go, ts, wedge) or -grammars (matrix) select, instead of
//	                     the part's file: a rerun of noisy rows or a focused comparison, which leaves
//	                     the document as it is
//
// -parts selects the parts (default all) and keeps the files of the others. The timed parts (go,
// gen, ts, matrix, wedge) run in interleaved, rotated rounds (-rounds, at least 20 for a landing
// claim) with an A/A control arm, a byte-identical copy of the measured binaries, and with -base a
// base arm built from that revision. A timed part whose geomean A/A ratio is more than 1.5% from 1
// is rejected: its results go to a .rejected.txt file instead, and the command fails without
// rendering the document. The timed parts run only under the exclusive benchmark lock in a declared
// measurement window of the machine, unless -force.
//
// RESULTS.md is rendered from the files of results/ alone and holds results only (their analysis is
// in SyntaxIR's docs/performance.md); TestResultsAreUpToDate checks that it matches them. The ranges
// and ratios that SyntaxIR's documents quote are figures (figures.go), which figures.json lists:
// each quoting place marks the text with <!-- fig:NAME --> and <!-- /fig -->, and pin writes the
// values of a published release into a SyntaxIR checkout (see pin.go).
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// parts are the parts of a capture, in the order they run; timedParts are the ones that measure
// time.
var (
	parts      = []string{"go", "gen", "ts", "matrix", "wedge", "size"}
	timedParts = map[string]bool{"go": true, "gen": true, "ts": true, "matrix": true, "wedge": true}
)

// options are the flags of a capture.
type options struct {
	main, head, base string
	dir, parts, node string
	grammars         string
	rounds           int
	benchtime, bench string
	force            bool
}

func main() {
	var err error
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "render":
			err = renderCmd(os.Args[2:])
		case "pin":
			err = pinCmd(os.Args[2:])
		case "release":
			err = releaseCmd(os.Args[2:])
		default:
			err = captureCmd(os.Args[1:])
		}
	} else {
		err = captureCmd(nil)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "timed:", err)
		os.Exit(1)
	}
}

func captureCmd(args []string) error {
	var o options
	fl := flag.NewFlagSet("timed", flag.ExitOnError)
	fl.StringVar(&o.main, "main", "", "the SyntaxIR checkout to measure: a local directory or a Git URL (required)")
	fl.StringVar(&o.head, "head", "", "the revision to measure (default: a local checkout as it is; main for a URL)")
	fl.StringVar(&o.base, "base", "", "a revision to compare with: adds a base arm built from it, and the A/B verdicts")
	fl.StringVar(&o.dir, "dir", "", "directory of the results files (default: results/ of this repository)")
	fl.StringVar(&o.parts, "parts", strings.Join(parts, ","), "parts to capture, comma-separated: "+strings.Join(parts, ", "))
	fl.IntVar(&o.rounds, "rounds", 10, "interleaved rounds of every timed measurement (20 for a landing claim)")
	fl.StringVar(&o.benchtime, "benchtime", "1s", "the -benchtime of each Go benchmark and profilematrix run")
	fl.StringVar(&o.bench, "bench", ".", "the rows of the go, ts and wedge parts to run, as go test -bench selects them (TypeScript rows are TS/MODE/WORKLOAD); rerun noisy rows with it")
	fl.StringVar(&o.grammars, "grammars", "", "the rows of the matrix part, comma-separated (default: every row of the manifest)")
	fl.BoolVar(&o.force, "force", false, "measure without the exclusive lock and window; the headers mark the results as unlocked")
	fl.StringVar(&o.node, "node", "node", "the Node.js executable for the TypeScript measurements (24 or later)")
	fl.Parse(args)
	if fl.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q (subcommands: render, pin, release)", fl.Args())
	}
	if o.main == "" {
		return errors.New("-main names the SyntaxIR checkout to measure")
	}
	harness, err := harnessRoot()
	if err != nil {
		return err
	}
	dir := o.dir
	if dir == "" {
		dir = filepath.Join(harness, "results")
	}
	if err := capture(harness, dir, o); err != nil {
		return err
	}
	if o.anyRerun() {
		fmt.Println("a -bench or -grammars capture is a rerun of some rows, saved in -rerun files: the document is not rendered")
		return nil
	}
	if o.dir != "" {
		return nil // results elsewhere leave the document of the repository as it is
	}
	return renderRepository(harness)
}

func renderCmd(args []string) error {
	fl := flag.NewFlagSet("render", flag.ExitOnError)
	fl.Parse(args)
	harness, err := harnessRoot()
	if err != nil {
		return err
	}
	return renderRepository(harness)
}

// renderRepository writes RESULTS.md and figures.json of the repository from its results/.
func renderRepository(harness string) error {
	doc, figures, err := renderResults(filepath.Join(harness, "results"))
	if err != nil {
		return err
	}
	for name, data := range map[string][]byte{"RESULTS.md": doc, "figures.json": figures} {
		path := filepath.Join(harness, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	return nil
}

// renderResults renders the document and the figures file from the results of a directory.
func renderResults(dir string) (doc, figures []byte, err error) {
	data, tsData, sizeData, err := loadResults(filepath.Join(dir, "results.txt"))
	if err != nil {
		return nil, nil, err
	}
	wedgeData, err := os.ReadFile(filepath.Join(dir, "results-wedge.txt"))
	if errors.Is(err, os.ErrNotExist) {
		wedgeData, err = nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	doc, values, err := renderDocWith(data, tsData, sizeData, wedgeData)
	if err != nil {
		return nil, nil, err
	}
	figures, err = encodeFigures(values)
	return doc, figures, err
}

// rerun reports whether a part measures a subset of its rows (-bench for the go, ts and wedge parts,
// -grammars for the matrix part). Such a part is saved apart from the full results (results-rerun.txt,
// results-ts-rerun.txt, results-wedge-rerun.txt, results-matrix-rerun.txt) and the document is not
// rendered: it is a rerun of the noisy rows of a capture, or a focused comparison.
func (o options) rerun(part string) bool {
	if !strings.Contains(","+o.parts+",", ","+part+",") {
		return false
	}
	switch part {
	case "go", "ts", "wedge":
		return o.bench != "."
	case "matrix":
		return o.grammars != ""
	}
	return false
}

// anyRerun reports whether any part of the capture is a rerun.
func (o options) anyRerun() bool {
	return o.rerun("go") || o.rerun("ts") || o.rerun("wedge") || o.rerun("matrix")
}

// resultsName returns the name of a part's results file: name, or its -rerun form.
func (o options) resultsName(part, name string) string {
	if o.rerun(part) {
		return strings.TrimSuffix(name, ".txt") + "-rerun.txt"
	}
	return name
}

// errRejected marks a capture with a part whose A/A spread exceeds the limit.
var errRejected = errors.New("rejected")

// capture checks out the arms, runs the selected parts and saves their results in dir. A timed part
// whose A/A spread exceeds the limit is saved as a .rejected.txt file, and capture then returns an
// error.
func capture(harness, dir string, o options) error {
	selected := map[string]bool{}
	timed := false
	for _, p := range strings.Split(o.parts, ",") {
		p = strings.TrimSpace(p)
		if !strings.Contains(","+strings.Join(parts, ",")+",", ","+p+",") {
			return fmt.Errorf("-parts: unknown part %q (want %s)", p, strings.Join(parts, ", "))
		}
		selected[p] = true
		timed = timed || timedParts[p]
	}
	if o.rounds < 1 {
		return fmt.Errorf("-rounds must be positive")
	}
	lock := ""
	if timed {
		var err error
		if lock, err = timedLock(o.force); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "sxir-timed-")
	if err != nil {
		return err
	}
	// The go command compares the directories of a go.work with the working directory after
	// resolving links (TMPDIR is under /var, a link, on macOS), so the work directory is named by
	// its resolved path.
	if work, err = filepath.EvalSymlinks(work); err != nil {
		return err
	}
	cleanups := newCleanups()
	defer cleanups.run()
	cleanups.add(func() { os.RemoveAll(work) })
	checkouts, err := checkoutArms(o, work, timed, cleanups)
	if err != nil {
		return err
	}
	root := checkouts.head
	commit := describeCommit(root)
	c := &timedConfig{harness: harness, root: root, baseRoot: checkouts.base, work: work, rounds: o.rounds,
		benchtime: o.benchtime, bench: o.bench, grammars: o.grammars}
	if checkouts.base != "" {
		c.baseRev = o.base + " (" + gitOutput(checkouts.base, "rev-parse", "--short", "HEAD") + ")"
	}
	source := checkouts.source
	harnessCommit := describeCommit(harness)
	var compare bytes.Buffer
	rejected := false
	save := func(name string, prov *provenance, body []byte, a *analysis) error {
		if a != nil {
			prov.extra = append(prov.extra, a.summary()...)
			rejected = rejected || a.rejected
			if a.rejected {
				name = strings.TrimSuffix(name, ".txt") + ".rejected.txt"
				fmt.Fprintf(os.Stderr, "%s: the A/A spread exceeds ±%.1f%%: the results are rejected\n", name, aaLimit*100)
			}
		}
		data := append(prov.header(), body...)
		fmt.Fprintln(os.Stderr, "saving", filepath.Join(dir, name))
		return os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
	newProv := func() *provenance {
		p := newProvenance(commit, lock, o.rounds, o.benchtime, c.arms(), c.baseRev)
		p.source, p.harness = source, harnessCommit
		return p
	}
	goPart := func(name, title string, suites []goSuite, cfg *timedConfig) error {
		prov := newProv()
		body, hashes, err := runGoSuites(cfg, suites)
		if err != nil {
			return err
		}
		prov.measured()
		for arm, h := range hashes {
			prov.binaries[arm] = h
		}
		a, err := analyze(armSamples(body, "ns/op"))
		if err != nil {
			return err
		}
		a.write(&compare, title, "ns/op")
		dists, units, err := analyzeDistributions(body)
		if err != nil {
			return err
		}
		for i, d := range dists {
			d.write(&compare, title+", "+units[i], units[i])
		}
		return save(name, prov, body, a)
	}
	if selected["go"] {
		name := o.resultsName("go", "results.txt")
		if err := goPart(name, "Go benchmarks ("+name+")", goSuites, c); err != nil {
			return err
		}
	}
	if selected["gen"] {
		g := *c
		g.bench = "."
		if err := goPart("results-gen.txt", "Generation and run-time compile (results-gen.txt)", genSuites, &g); err != nil {
			return err
		}
	}
	if selected["ts"] {
		prov := newProv()
		body, err := captureTS(c, prov, o.node)
		if err != nil {
			return err
		}
		prov.measured()
		a, err := analyze(armSamples(body, "ns/op"))
		if err != nil {
			return err
		}
		name := o.resultsName("ts", "results-ts.txt")
		a.write(&compare, "TypeScript ("+name+")", "ns/op")
		if err := save(name, prov, body, a); err != nil {
			return err
		}
	}
	if selected["matrix"] {
		prov := newProv()
		prov.arms = []string{armHead, armControl} // profilematrix compares generation options, not revisions
		prov.baseRev = ""
		body, err := captureMatrix(c)
		if err != nil {
			return err
		}
		prov.measured()
		a, err := analyze(matrixSamples(body))
		if err != nil {
			return err
		}
		name := o.resultsName("matrix", "results-matrix.txt")
		a.write(&compare, "Language grammars ("+name+")", "ns/pass")
		if err := save(name, prov, body, a); err != nil {
			return err
		}
	}
	if selected["wedge"] {
		for _, arm := range c.arms() {
			if arm != armControl {
				if err := prepareWedge(c, arm); err != nil {
					return err
				}
			}
		}
		name := o.resultsName("wedge", "results-wedge.txt")
		if err := goPart(name, "Wedge ("+name+")", wedgeSuites, c); err != nil {
			return err
		}
	}
	if selected["size"] {
		data, err := captureSizes(root, work)
		if err != nil {
			return err
		}
		path := filepath.Join(dir, "results-size.txt")
		if data == nil {
			fmt.Fprintf(os.Stderr, "sizes: not available at %s: it has no %s\n", commit, metricsCommand)
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if err := os.WriteFile(path, data, 0o644); err != nil {
			return err
		}
	}
	if compare.Len() > 0 {
		name := "results-compare.txt"
		if rejected {
			name = "results-compare.rejected.txt"
		}
		os.Stdout.Write(compare.Bytes())
		if err := os.WriteFile(filepath.Join(dir, name), compare.Bytes(), 0o644); err != nil {
			return err
		}
	}
	if rejected {
		return fmt.Errorf("%w: a part's A/A spread exceeds ±%.1f%% (see results-compare.rejected.txt); "+
			"rerun in a quieter window, or rerun the noisy rows with -bench or -grammars", errRejected, aaLimit*100)
	}
	return nil
}

// genSuites are the benchmarks of the generator and of the run-time compile of a grammar, which the
// gen part measures; goSuites are the other benchmarks of package bench, which the go part measures.
var (
	genSuites = []goSuite{{pkg: "./bench", pattern: "^(BenchmarkGeneration|BenchmarkRuntimeBackendCompile)$"}}
	goSuites  = []goSuite{{pkg: "./bench", pattern: ".", exclude: genSuites[0].pattern}}
)

// loadResults reads the Go results file and, next to it, the TypeScript and size results if they
// exist (nil otherwise: the document then leaves out the TypeScript section and says that the sizes
// are not available).
func loadResults(goPath string) (data, tsData, sizeData []byte, err error) {
	if data, err = os.ReadFile(goPath); err != nil {
		return nil, nil, nil, err
	}
	optional := func(name string) ([]byte, error) {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(goPath), name))
		if os.IsNotExist(err) {
			return nil, nil
		}
		return b, err
	}
	if tsData, err = optional("results-ts.txt"); err != nil {
		return nil, nil, nil, err
	}
	if sizeData, err = optional("results-size.txt"); err != nil {
		return nil, nil, nil, err
	}
	return data, tsData, sizeData, nil
}

// harnessRoot returns the root of this repository (the directory of its go.mod).
func harnessRoot() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/syntaxir/bench").Output()
	if err != nil {
		return "", fmt.Errorf("finding the repository (run inside it): %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// describeCommit names the commit of a checkout for the headers of the results, with a note if
// tracked files have uncommitted changes. It is determined once, before a capture writes any result
// file, so that all the files of one capture name the same state.
func describeCommit(root string) string {
	commit := gitOutput(root, "rev-parse", "--short", "HEAD")
	if gitOutput(root, "status", "--porcelain", "--untracked-files=no") != "" {
		commit += " (with uncommitted changes)"
	}
	return commit
}

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// loadAverage returns the 1, 5 and 15 minute load averages before the run, if the system tells.
func loadAverage() string {
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(data))
		if len(f) >= 3 {
			return strings.Join(f[:3], " ")
		}
	}
	if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
		return strings.Trim(strings.TrimSpace(string(out)), "{ }")
	}
	return ""
}
