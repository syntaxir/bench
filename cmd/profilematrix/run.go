package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/syntaxir/bench/internal/probe"
	"github.com/syntaxir/bench/internal/workloads"
)

type variant struct {
	Name string
	Args []string
}

type config struct {
	Root      string
	Manifest  *workloads.Manifest // the rows and their inputs, which the checkout at Root exports
	Grammars  []string            // the rows; default all rows of the manifest
	Variants  []variant
	Rounds    int
	Benchtime string
	Control   bool
	TS        bool
	Work      string // default: a temporary directory that is removed
}

// programs are the measuring programs of the rows, by the name the manifest gives them.
var programs = map[string]string{
	"parse":       benchMain,
	"incremental": incrementalMain,
	"stream":      streamMain,
}

// built is a generated parser with the programs built for it.
type built struct {
	grammar, variant string
	bench            string // the measuring program
}

// measurement is what the measuring program printed, and what the parent observed of the process.
type measurement struct {
	Inputs   int     `json:"inputs"`
	NS       float64 `json:"ns"`
	Bytes    int64   `json:"bytes"`
	Allocs   int64   `json:"allocs"`
	Retained int64   `json:"retained"`
	GCShare  float64 `json:"gc_share"`
	RSS      int64   `json:"-"`
}

func run(cfg config, out, log io.Writer) error {
	if len(cfg.Variants) == 0 {
		return fmt.Errorf("no -variant: give at least the baseline, for example -variant speed=")
	}
	if cfg.Rounds < 1 {
		return fmt.Errorf("-rounds must be at least 1")
	}
	variants := slices.Clone(cfg.Variants)
	if cfg.Control {
		variants = append(variants, variant{Name: "control", Args: variants[0].Args})
	}
	seen := map[string]bool{}
	for _, v := range variants {
		if seen[v.Name] {
			return fmt.Errorf("variant %q is given twice", v.Name)
		}
		seen[v.Name] = true
	}
	if cfg.Manifest == nil {
		return fmt.Errorf("no workload manifest")
	}
	grammars := cfg.Grammars
	if len(grammars) == 0 {
		grammars = cfg.Manifest.RowNames()
	}
	rows := map[string]workloads.Row{}
	for _, g := range grammars {
		r, ok := cfg.Manifest.Row(g)
		if !ok {
			return fmt.Errorf("the workload manifest has no row %q (it has %s)", g, strings.Join(cfg.Manifest.RowNames(), ", "))
		}
		rows[g] = r
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return err
	}
	work := cfg.Work
	if work == "" {
		if work, err = os.MkdirTemp("", "sxir-profilematrix-"); err != nil {
			return err
		}
		defer os.RemoveAll(work)
	} else if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	sxirBin, err := probe.BuildSXIR(root, work)
	if err != nil {
		return err
	}
	names := make([]string, len(variants))
	for i, v := range variants {
		names[i] = v.Name
	}
	c := cells{}
	programs := map[string]built{}
	noEffect := map[string]bool{} // by row and variant: the generated Go parser equals the baseline's
	for _, g := range grammars {
		r := rows[g]
		baseDir := ""
		for _, v := range variants {
			fmt.Fprintf(log, "generate %s %s\n", g, v.Name)
			dir, err := generate(root, sxirBin, work, g, r, v)
			if err != nil {
				return fmt.Errorf("%s %s: %w", g, v.Name, err)
			}
			if baseDir == "" {
				baseDir = dir
			}
			// The control is a copy of the baseline on purpose and is always measured.
			if v.Name != "control" && dir != baseDir {
				same, err := sameDir(baseDir, dir)
				if err != nil {
					return err
				}
				noEffect[g+"\x00"+v.Name] = same
			}
			if r.Program == "parse" && cfg.TS {
				if err := recordTS(root, sxirBin, work, r, v, c); err != nil {
					return fmt.Errorf("%s %s: %w", g, v.Name, err)
				}
			}
			if noEffect[g+"\x00"+v.Name] {
				fmt.Fprintf(log, "no effect %s %s\n", g, v.Name)
				continue
			}
			fmt.Fprintf(log, "build %s %s\n", g, v.Name)
			b, err := build(root, work, g, r, v, dir, c)
			if err != nil {
				return fmt.Errorf("%s %s: %w", g, v.Name, err)
			}
			programs[g+"\x00"+v.Name] = b
		}
	}
	best := map[string]measurement{} // by grammar and variant
	var samples []string             // the time of every round, in the order measured
	for round := 0; round < cfg.Rounds; round++ {
		for _, g := range grammars {
			var files []string
			for _, in := range rows[g].Inputs {
				files = append(files, cfg.Manifest.Path(in))
			}
			for _, name := range rotate(names, round) {
				if noEffect[g+"\x00"+name] {
					continue
				}
				fmt.Fprintf(log, "round %d %s %s\n", round+1, g, name)
				m, err := measure(programs[g+"\x00"+name].bench, cfg.Benchtime, files)
				if err != nil {
					return fmt.Errorf("%s %s: %w", g, name, err)
				}
				samples = append(samples, fmt.Sprintf("sample %s %s %d ns_op %s\n", g, name, round+1, formatValue(m.NS)))
				key := g + "\x00" + name
				if prev, ok := best[key]; ok {
					m = keepBest(prev, m)
				}
				best[key] = m
			}
		}
	}
	for key, m := range best {
		g, v, _ := strings.Cut(key, "\x00")
		c.set(g, v, "ns_op", m.NS)
		c.set(g, v, "b_op", float64(m.Bytes))
		c.set(g, v, "allocs_op", float64(m.Allocs))
		c.set(g, v, "retained", float64(m.Retained))
		c.set(g, v, "rss", float64(m.RSS))
		c.set(g, v, "gc_share", m.GCShare)
	}
	c.printCells(out, grammars, names)
	for _, l := range samples {
		if _, err := io.WriteString(out, l); err != nil {
			return err
		}
	}
	c.printRatios(out, grammars, names)
	printNoEffect(out, grammars, names, noEffect)
	return nil
}

// generate generates a variant of a row into a directory of work and returns the directory.
func generate(root, sxirBin, work, name string, r workloads.Row, v variant) (string, error) {
	genDir := filepath.Join(work, "gen", name, v.Name)
	if err := probe.GenerateGo(root, sxirBin, genDir, r.Grammar, r.Gen, append(slices.Clone(r.Extra), v.Args...)...); err != nil {
		return "", err
	}
	return genDir, nil
}

// sameDir reports whether two generated directories hold the same files with the same contents.
func sameDir(a, b string) (bool, error) {
	list := func(dir string) (map[string][]byte, error) {
		out := map[string][]byte{}
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, path)
			out[rel] = data
			return nil
		})
		return out, err
	}
	x, err := list(a)
	if err != nil {
		return false, err
	}
	y, err := list(b)
	if err != nil {
		return false, err
	}
	if len(x) != len(y) {
		return false, nil
	}
	for k, v := range x {
		if w, ok := y[k]; !ok || !bytes.Equal(v, w) {
			return false, nil
		}
	}
	return true, nil
}

// recordTS generates the TypeScript parser of a grammar and records its sizes.
func recordTS(root, sxirBin, work string, r workloads.Row, v variant, c cells) error {
	grammar := r.Name
	tsDir := filepath.Join(work, "ts", grammar, v.Name)
	if err := probe.GenerateTS(root, sxirBin, tsDir, r.Grammar, r.Gen, v.Args...); err != nil {
		return err
	}
	raw, gz, err := probe.SizeOfFile(filepath.Join(tsDir, "parser.ts"))
	if err != nil {
		return err
	}
	c.set(grammar, v.Name, "ts_raw", float64(raw))
	c.set(grammar, v.Name, "ts_gzip", float64(gz))
	return nil
}

// build records the sizes of a generated variant in c and builds the measuring program.
func build(root, work, name string, r workloads.Row, v variant, genDir string, c cells) (built, error) {
	raw, _, err := probe.SizeOfFile(filepath.Join(genDir, "parser.go"))
	if err != nil {
		return built{}, err
	}
	c.set(name, v.Name, "src_bytes", float64(raw))
	// The directory name of -D is unique to this build: the flag changes nothing in the output, but it
	// is part of the cache key of the package, so the package is compiled and not taken from the cache.
	uniq := fmt.Sprintf("-gcflags=sizeprobe/gen=-D=./%d", time.Now().UnixNano())
	start := time.Now()
	_, size, err := probe.BuildProgram(root, filepath.Join(work, "size", name, v.Name), genDir, probe.SizeMain, uniq)
	if err != nil {
		return built{}, err
	}
	c.set(name, v.Name, "compile_ms", float64(time.Since(start).Milliseconds()))
	c.set(name, v.Name, "bin_bytes", float64(size))
	bench, _, err := probe.BuildProgram(root, filepath.Join(work, "bench", name, v.Name), genDir, programs[r.Program])
	if err != nil {
		return built{}, err
	}
	return built{grammar: name, variant: v.Name, bench: bench}, nil
}

// printNoEffect lists the variants that generate the same Go parser as the baseline for a row; they
// are not built or measured.
func printNoEffect(w io.Writer, rows, variants []string, noEffect map[string]bool) {
	var lines []string
	for _, g := range rows {
		for _, v := range variants {
			if noEffect[g+"\x00"+v] {
				lines = append(lines, fmt.Sprintf("noeffect %s %s\n", g, v))
			}
		}
	}
	if len(lines) > 0 {
		fmt.Fprintln(w, "\nno effect: the generated Go parser equals the baseline's")
		for _, l := range lines {
			fmt.Fprint(w, l)
		}
	}
}

// keepBest combines two measurements of the same program: the minimum of each quantity, and the
// garbage collection share of the faster run.
func keepBest(a, b measurement) measurement {
	r := a
	if b.NS < a.NS {
		r.NS, r.GCShare = b.NS, b.GCShare
	}
	r.Bytes, r.Allocs, r.Retained = min(a.Bytes, b.Bytes), min(a.Allocs, b.Allocs), min(a.Retained, b.Retained)
	r.RSS = min(a.RSS, b.RSS)
	return r
}

// measure runs the measuring program once in a fresh process.
func measure(program, benchtime string, files []string) (measurement, error) {
	cmd := exec.Command(program, append([]string{benchtime}, files...)...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return measurement{}, fmt.Errorf("%s: %w\n%s", program, err, stderr.String())
	}
	var m measurement
	if err := json.Unmarshal([]byte(stdout.String()), &m); err != nil {
		return measurement{}, fmt.Errorf("%s printed %q: %w", program, stdout.String(), err)
	}
	if m.NS <= 0 || math.IsNaN(m.NS) {
		return measurement{}, fmt.Errorf("%s measured %v ns per pass", program, m.NS)
	}
	m.RSS = peakRSS(cmd.ProcessState)
	return m, nil
}
