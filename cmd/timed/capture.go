package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/syntaxir/bench/internal/probe"
	"github.com/syntaxir/bench/internal/workloads"
)

// tsModes are the entry points of a generated TypeScript parser that are measured, by the name
// they have in the results.
var tsModes = []struct{ name, entry string }{
	{"Parse", "parse"},
	{"ParseValue", "parseValue"},
	{"Recognize", "recognize"},
}

// captureTS generates a TypeScript parser for every workload of head's manifest in every arm (head's
// and base's with their own sxir, control's as a copy of head's), measures each entry point in the
// rounds of c in fresh Node processes (scripts/ts-parse.mjs of this repository), the arms in rotated
// order, and returns the results in benchfmt with the arm of each run. Every arm parses head's inputs.
func captureTS(c *timedConfig, prov *provenance, node string) ([]byte, error) {
	version, err := execOut(c.root, nil, node, "--version")
	if err != nil {
		return nil, err
	}
	v8, err := execOut(c.root, nil, node, "-p", "process.versions.v8")
	if err != nil {
		return nil, err
	}
	prov.extra = append(prov.extra, "node: "+strings.TrimSpace(version), "v8: "+strings.TrimSpace(v8),
		"count: "+fmt.Sprint(c.rounds))
	m, err := c.manifest()
	if err != nil {
		return nil, err
	}
	// The units that -bench selects, by the row name TS/MODE/WORKLOAD.
	type tsUnit struct {
		w workloads.TS
		m struct{ name, entry string }
	}
	var units []tsUnit
	var used []workloads.TS
	for _, w := range m.TS {
		selected := false
		for _, mode := range tsModes {
			ok, err := matchLevels(c.bench, "TS/"+mode.name+"/"+w.Name)
			if err != nil {
				return nil, err
			}
			if ok {
				units = append(units, tsUnit{w, mode})
				selected = true
			}
		}
		if selected {
			used = append(used, w)
		}
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("-bench %q selects no TypeScript row (TS/MODE/WORKLOAD)", c.bench)
	}
	notInBase := map[string]bool{}
	for _, arm := range c.arms() {
		dir := filepath.Join(c.work, "ts", arm)
		if arm == armControl {
			if err := copyTree(filepath.Join(c.work, "ts", armHead), dir); err != nil {
				return nil, err
			}
		} else {
			sxirBin, err := c.sxir(arm)
			if err != nil {
				return nil, err
			}
			for _, w := range used {
				if arm == armBase {
					if _, err := os.Stat(filepath.Join(c.srcRoot(arm), filepath.FromSlash(w.Grammar))); errors.Is(err, fs.ErrNotExist) {
						// The workload is newer than base: its rows are head's and control's alone,
						// and the analysis marks them as not in base.
						notInBase[w.Name] = true
						continue
					}
				}
				args := []string{"gen", "-lang", "ts", "-types", "-recognize", "-g", filepath.Join(c.srcRoot(arm), filepath.FromSlash(w.Grammar)), "-o", filepath.Join(dir, w.Name)}
				if _, err := execOut(c.srcRoot(arm), nil, sxirBin, args...); err != nil {
					return nil, err
				}
			}
		}
		h, err := treeHash(dir)
		if err != nil {
			return nil, err
		}
		prov.binaries[arm] = "ts=" + h
	}
	var b bytes.Buffer
	// Alternate the runs of the rounds so that drift of the machine affects every workload and arm alike.
	script := filepath.Join(c.harness, "scripts", "ts-parse.mjs")
	for round := 1; round <= c.rounds; round++ {
		for i, u := range units {
			w, mode := u.w, u.m
			{
				for _, arm := range rotated(c.arms(), round+i) {
					if arm == armBase && notInBase[w.Name] {
						continue
					}
					line, err := execOut(c.work, nil, node, "--experimental-strip-types", script,
						"--module", filepath.Join(c.work, "ts", arm, w.Name, "parser.ts"),
						"--input", m.Path(w.Input),
						"--name", mode.name+"/"+w.Name, "--entry", mode.entry)
					if err != nil {
						return nil, fmt.Errorf("%s %s in %s: %w", mode.name, w.Name, arm, err)
					}
					line = strings.TrimSpace(line)
					if !strings.HasPrefix(line, "BenchmarkTSParse/") {
						return nil, fmt.Errorf("%s %s in %s: unexpected output %q", mode.name, w.Name, arm, line)
					}
					line = "BenchmarkTS/" + strings.TrimPrefix(line, "BenchmarkTSParse/")
					fmt.Fprintf(os.Stderr, "round %d/%d %s %s\n", round, c.rounds, arm, line)
					fmt.Fprintf(&b, "arm: %s\nround: %d\n%s\n", arm, round, line)
				}
			}
		}
	}
	return b.Bytes(), nil
}

// copyTree copies the regular files under from to to.
func copyTree(from, to string) error {
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
}

// metricsCommand is the file of the command of a SyntaxIR checkout that reports its deterministic
// metrics, the generated sizes among them.
const metricsCommand = "bench/metrics/main.go"

// captureSizes collects the sizes of the generated parsers of the checkout at root with its
// go run ./bench/metrics collect -parts sizes, and returns the report. It returns nil when the
// checkout has no such command: the document then says that the sizes are not available.
func captureSizes(root, work string) ([]byte, error) {
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(metricsCommand))); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	out := filepath.Join(work, "sizes.txt")
	cmd := exec.Command("go", "run", "./bench/metrics", "collect", "-parts", "sizes", "-o", out)
	cmd.Dir = root
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	fmt.Fprintf(os.Stderr, "running %s in %s\n", strings.Join(cmd.Args, " "), root)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("collecting the sizes (go run ./bench/metrics collect -parts sizes): %w", err)
	}
	return os.ReadFile(out)
}

// execOut runs a command in dir, with extra environment variables, and returns its standard output.
func execOut(dir string, env []string, name string, args ...string) (string, error) {
	return probe.ExecOut(dir, env, name, args...)
}
