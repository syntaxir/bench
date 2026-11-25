package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// wedgeModule is the module of the wedge benchmark of this repository (wedge/), which compares the
// parsers that SyntaxIR generates with those of other Go parser generators.
const wedgeModule = "github.com/syntaxir/bench/wedge"

// wedgeSuites are the benchmarks of the wedge part.
var wedgeSuites = []goSuite{{pkg: ".", pattern: "^BenchmarkWedge$", wedge: true}}

// prepareWedge copies the wedge module into the work directory for an arm (head or base), with a
// go.work that resolves syntaxir.dev/sxir to the arm's checkout, and regenerates its parsers there
// with go generate: the SyntaxIR parsers with the arm's sxir command, so that each arm measures the
// parsers that its own generator writes. The other tools' parsers are regenerated at the versions
// that wedge/go.mod pins, which leaves them as they are.
func prepareWedge(c *timedConfig, arm string) error {
	dir := filepath.Join(c.work, "wedge", arm)
	if err := copyTree(filepath.Join(c.harness, "wedge"), dir); err != nil {
		return err
	}
	work := wedgeWork(c.srcRoot(arm))
	if err := os.WriteFile(filepath.Join(dir, "go.work"), []byte(work), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "generating the wedge parsers for %s\n", arm)
	if _, err := execOut(dir, []string{"GOWORK=" + filepath.Join(dir, "go.work")}, "go", "generate", "."); err != nil {
		return fmt.Errorf("generating the wedge parsers for %s: %w", arm, err)
	}
	return nil
}

// wedgeWork returns the go.work of a copy of the wedge that resolves syntaxir.dev/sxir to the
// checkout root. The wedge's go.mod requires syntaxir.dev/sxir at v0.0.0, which no proxy serves,
// and the go command reads the go.mod of that version to load the module graph even when the
// workspace uses the checkout; the replace gives it the checkout's.
func wedgeWork(root string) string {
	return fmt.Sprintf("go 1.27.1\n\nuse (\n\t.\n\t%q\n)\n\nreplace syntaxir.dev/sxir v0.0.0 => %q\n", root, root)
}
