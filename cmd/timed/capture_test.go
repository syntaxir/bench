package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tsTableWorkloads are the workloads of the TypeScript table of the document.
var tsTableWorkloads = []string{"JSON", "CSV", "XML", "Arith_OperatorsRule", "Arith_LeftRec", "Minilang", "Outline"}

// syntheticTS returns TypeScript results for every workload and mode, with the given number of runs.
func syntheticTS(runs int) string {
	var b strings.Builder
	b.WriteString("# date: 2026-02-03\n# commit: abc\n# node: v24.0.0\n# v8: 13.0\n# cores: 8\n")
	for r := 0; r < runs; r++ {
		for _, w := range tsTableWorkloads {
			for _, m := range tsModes {
				fmt.Fprintf(&b, "BenchmarkTS/%s/%s 10 %d ns/op %d.50 MB/s\n", m.name, w, 1_000_000*(r+1), r+1)
			}
		}
	}
	return b.String()
}

// syntheticSizes returns a report of go run ./bench/metrics collect -parts sizes, its metrics sorted
// by key as the command writes them.
func syntheticSizes() string {
	var b strings.Builder
	b.WriteString("# sxir deterministic metrics (go run ./bench/metrics; docs/performance.md)\n" +
		"# commit 0123456789abcdef0123456789abcdef01234567 clean\n# go go1.27.1 darwin/arm64\n# parts sizes\n")
	for _, p := range []string{"cel", "duckdb", "json", "runtime"} {
		for _, m := range []string{"go-binary", "go-binary-embed", "go-lib", "go-source", "ts-gzip", "ts-lib-gzip", "ts-lib-raw", "ts-raw"} {
			if p == "runtime" && m != "go-lib" && m != "ts-lib-raw" && m != "ts-lib-gzip" {
				continue
			}
			fmt.Fprintf(&b, "size %s %s 12345 lower\n", p, m)
		}
	}
	return b.String()
}

func goResults(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(resultsDir, "results.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRenderTypeScriptAndSizeSections(t *testing.T) {
	doc, err := renderAll(goResults(t), []byte(syntheticTS(3)), []byte(syntheticSizes()))
	if err != nil {
		t.Fatal(err)
	}
	got := string(doc)
	for _, want := range []string{
		"### Generated TypeScript (Node v24.0.0)",
		"The generated TypeScript parsers were measured with Node v24.0.0.",
		"Each figure is the median of 3 runs",
		"| JSON | 2.00 ms (2.5 MB/s) |", // the median of 1, 2 and 3 ms and of 1.5, 2.5 and 3.5 MB/s
		"### Generated Parser Size",
		"(commit 012345678, go1.27.1)",
		"| duckdb | 12.3 KB | 12.3 KB | 12.3 KB | 12.3 KB | 12.3 KB | 12.3 KB | 12.3 KB | 12.3 KB |",
		"weighs 12.3 KB, 12.3 KB gzipped",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("document lacks %q", want)
		}
	}
	// The TypeScript benchmarks have no memory figures and stay out of the table of Go results.
	if strings.Contains(got, "| `TS/") {
		t.Error("the table of all results lists TypeScript benchmarks")
	}
}

// TestRenderWithoutTypeScriptOrSizes checks that without TypeScript results the document leaves
// their section out, and that without sizes it says that they are not available at the commit of
// the run instead of showing any.
func TestRenderWithoutTypeScriptOrSizes(t *testing.T) {
	doc, err := renderAll(goResults(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doc), "TypeScript") || strings.Contains(string(doc), "| Go source |") {
		t.Error("the document shows sections whose results were not given")
	}
	r, err := parseResults(goResults(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := "Sizes: not available at " + r.meta["commit"] + "."; !strings.Contains(string(doc), want) {
		t.Errorf("the document lacks %q", want)
	}
}

func TestRenderRejectsIncompleteTypeScriptResults(t *testing.T) {
	var kept []string
	for _, l := range strings.Split(syntheticTS(3), "\n") {
		if !strings.HasPrefix(l, "BenchmarkTS/Recognize/XML ") {
			kept = append(kept, l)
		}
	}
	_, err := renderAll(goResults(t), []byte(strings.Join(kept, "\n")), nil)
	if err == nil || !strings.Contains(err.Error(), "TS/Recognize/XML") {
		t.Errorf("got %v, want an error naming TS/Recognize/XML", err)
	}

	lines := strings.Split(syntheticTS(3), "\n")
	dropped := false
	kept = nil
	for _, l := range lines {
		if !dropped && strings.HasPrefix(l, "BenchmarkTS/Parse/JSON ") {
			dropped = true
			continue
		}
		kept = append(kept, l)
	}
	_, err = renderAll(goResults(t), []byte(strings.Join(kept, "\n")), nil)
	if err == nil || !strings.Contains(err.Error(), "TS/Parse/JSON has 2 runs, want 3") {
		t.Errorf("got %v, want a missing-run error", err)
	}
}

func TestRenderRejectsForeignTypeScriptNames(t *testing.T) {
	_, err := renderAll(goResults(t), []byte("BenchmarkParse/JSON/closure/codepoints 1 10 ns/op\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "does not start with TS/") {
		t.Errorf("got %v", err)
	}
}

func TestRenderRejectsMissingSize(t *testing.T) {
	var kept []string
	for _, l := range strings.Split(syntheticSizes(), "\n") {
		if l != "size cel ts-gzip 12345 lower" {
			kept = append(kept, l)
		}
	}
	_, err := renderAll(goResults(t), nil, []byte(strings.Join(kept, "\n")))
	if err == nil || !strings.Contains(err.Error(), "size cel ts-gzip") {
		t.Errorf("got %v, want an error naming size cel ts-gzip", err)
	}
}

func TestParseBenchLinesWithoutBenchmem(t *testing.T) {
	line := "BenchmarkTS/Parse/A 5 100 ns/op 2.00 MB/s\n"
	r, err := parseBenchLines([]byte(line), false)
	if err != nil {
		t.Fatal(err)
	}
	if a := r.bench["TS/Parse/A"]; a == nil || a.ns != 100 || a.mbs != 2 || a.bytesPerOp != 0 {
		t.Errorf("TS/Parse/A: %+v", a)
	}
	if _, err := parseResults([]byte(line)); err == nil || !strings.Contains(err.Error(), "missing required B/op") {
		t.Errorf("parseResults accepted a line without B/op: %v", err)
	}
	if _, err := parseBenchLines([]byte("BenchmarkTS/Parse/A 5 MB/s\n"), false); err == nil {
		t.Error("accepted a line without a time")
	}
}

func TestParseSizes(t *testing.T) {
	meta := map[string]string{}
	got, order, err := parseSizes([]byte("# commit 0123456789abcdef dirty\n# go go1.27.1 linux/amd64\n"+
		"size json go-source 10 lower\nsize json ts-raw 20 lower\nsize runtime go-lib 5 lower\nsize xml go-source 30 lower\n"), meta)
	if err != nil {
		t.Fatal(err)
	}
	if got["json"]["go-source"] != 10 || got["json"]["ts-raw"] != 20 || got["runtime"]["go-lib"] != 5 {
		t.Errorf("sizes %v", got)
	}
	if strings.Join(order, " ") != "json xml" {
		t.Errorf("parsers %v, want json xml: in their order, without the whole library", order)
	}
	if meta["size.commit"] != "012345678 (with uncommitted changes)" || meta["size.go"] != "go1.27.1" {
		t.Errorf("meta %v", meta)
	}
	for name, data := range map[string]string{
		"duplicate":    "size json ts-raw 1 lower\nsize json ts-raw 2 lower\n",
		"short":        "size json ts-raw 1\n",
		"not a number": "size json ts-raw many lower\n",
		"empty":        "# commit abc clean\n",
	} {
		if _, _, err := parseSizes([]byte(data), map[string]string{}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestLoadResultsTreatsSiblingFilesAsOptional(t *testing.T) {
	dir := t.TempDir()
	goPath := filepath.Join(dir, "results.txt")
	if err := os.WriteFile(goPath, []byte("go"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ts, sizes, err := loadResults(goPath)
	if err != nil || ts != nil || sizes != nil {
		t.Fatalf("ts %q, sizes %q, err %v", ts, sizes, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "results-ts.txt"), []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ts, _, err = loadResults(goPath); err != nil || string(ts) != "ts" {
		t.Fatalf("ts %q, err %v", ts, err)
	}
	if _, _, _, err := loadResults(filepath.Join(dir, "missing.txt")); err == nil {
		t.Error("missing Go results: no error")
	}
}

// TestHeadersShareTheCommitDescription checks that a header names the commit it is given, so that
// the files of one capture agree even though the capture itself changes tracked files.
func TestHeadersShareTheCommitDescription(t *testing.T) {
	a := newProvenance("abc123", "", 1, "1s", nil, "").header()
	b := newProvenance("abc123", "", 2, "2s", nil, "").header()
	for _, h := range []string{string(a), string(b)} {
		if !strings.Contains(h, "# commit: abc123\n") {
			t.Errorf("header lacks the given commit:\n%s", h)
		}
	}
}

// TestDescribeCommitMarksUncommittedChanges checks that a checkout is named by its commit, with a
// note once a tracked file has changed.
func TestDescribeCommitMarksUncommittedChanges(t *testing.T) {
	repo := gitRepo(t)
	file := filepath.Join(repo, "f")
	if err := os.WriteFile(file, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "f"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "two"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	short := gitOutput(repo, "rev-parse", "--short", "HEAD")
	if got := describeCommit(repo); got != short || short == "unknown" {
		t.Errorf("describeCommit = %q, want %q", got, short)
	}
	if err := os.WriteFile(file, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := describeCommit(repo), short+" (with uncommitted changes)"; got != want {
		t.Errorf("describeCommit = %q, want %q", got, want)
	}
}

// syntheticWedge returns wedge results for every grammar, input and tool, with the given number of
// runs, in the head arm and a control arm that the document ignores.
func syntheticWedge(runs int) string {
	var b strings.Builder
	b.WriteString("# date: 2026-02-04\n# commit: def\n")
	for _, arm := range []string{"head", "control"} {
		fmt.Fprintf(&b, "arm: %s\n", arm)
		for r := 0; r < runs; r++ {
			for _, g := range []string{"calc", "filter", "json"} {
				for _, s := range []string{"short", "long"} {
					for i, tool := range []string{"sxir", "sxir_nolimits", "pigeon", "participle"} {
						fmt.Fprintf(&b, "BenchmarkWedge/%s/%s/%s-16 100 %d ns/op 1.00 MB/s %d B/op %d allocs/op\n", g, s, tool, 1000*(i+1), 64*(i+1), i+1)
					}
				}
			}
		}
	}
	return b.String()
}

// TestRenderWedgeSection checks that the wedge results add their tables, with the factors relative
// to SyntaxIR's default form, and stay out of the table of the Go results; and that a wedge run that
// lacks a row fails the rendering.
func TestRenderWedgeSection(t *testing.T) {
	doc, _, err := renderDocWith(goResults(t), nil, nil, []byte(syntheticWedge(3)))
	if err != nil {
		t.Fatal(err)
	}
	got := string(doc)
	for _, want := range []string{
		"### Other Go Parser Generators",
		"(commit def, measured\non 2026-02-04, the median of 3 runs)",
		"| json | long | 1.00 µs | 2.00 µs (2.00×) | 3.00 µs (3.00×) | 4.00 µs (4.00×) |",
		"| calc | short | 64.0 B / 1 | 128 B / 2 | 192 B / 3 | 256 B / 4 |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("document lacks %q", want)
		}
	}
	if strings.Contains(got, "| `Wedge/") {
		t.Error("the table of all results lists wedge benchmarks")
	}
	without, _, err := renderDocWith(goResults(t), nil, nil, nil)
	if err != nil || strings.Contains(string(without), "Other Go Parser Generators") {
		t.Errorf("without wedge results the section is shown (err %v)", err)
	}
	var kept []string
	for _, l := range strings.Split(syntheticWedge(3), "\n") {
		if !strings.HasPrefix(l, "BenchmarkWedge/filter/long/pigeon-") {
			kept = append(kept, l)
		}
	}
	if _, _, err := renderDocWith(goResults(t), nil, nil, []byte(strings.Join(kept, "\n"))); err == nil || !strings.Contains(err.Error(), "Wedge/filter/long/pigeon") {
		t.Errorf("a wedge run without a row: err = %v", err)
	}
}
