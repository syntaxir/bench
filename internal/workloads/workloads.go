// Package workloads reads the description of the timed workloads that a SyntaxIR checkout exports:
// its bench package writes, with
//
//	SXIR_BENCH_EXPORT=DIR go test ./bench -run '^TestExportWorkloads$'
//
// the manifest DIR/manifest.json and the inputs it names. The harness measures these grammars and
// inputs with these generation options, so that it measures what the checkout's own benchmarks
// measure without importing the packages that make them.
package workloads

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

// Version is the version of the manifest's format that this package reads.
const Version = 1

// Manifest is the description of the workloads, read from a directory that holds it and its inputs.
type Manifest struct {
	Version int `json:"version"`
	// TS are the workloads measured on the generated TypeScript parsers.
	TS []TS `json:"ts"`
	// Matrix are the rows of the language-grammar matrix.
	Matrix []Row `json:"matrix"`
	// Dir is the directory of the manifest, which the paths of the inputs are relative to.
	Dir string `json:"-"`
}

// TS is a workload of a generated TypeScript parser: a grammar of the checkout and an input.
type TS struct {
	Name    string `json:"name"`
	Grammar string `json:"grammar"` // relative to the root of the checkout
	Input   string `json:"input"`   // relative to the manifest's directory
}

// Row is a row of the language-grammar matrix: a grammar generated with the options Gen followed by
// Extra, and measured by Program on its inputs.
type Row struct {
	Name    string   `json:"name"`
	Grammar string   `json:"grammar"` // relative to the root of the checkout
	Gen     []string `json:"gen"`
	Extra   []string `json:"extra,omitempty"`
	// Program is parse (ParseValue on every input), incremental (one-digit edits of record 25000 of
	// a long CSV file with the generated Document) or stream (ParseStream of a long CSV file).
	Program string   `json:"program"`
	Inputs  []string `json:"inputs"` // relative to the manifest's directory
}

// Programs are the measuring programs that a row may name.
var Programs = []string{"parse", "incremental", "stream"}

// Export runs the export of the checkout at root into dir and reads the manifest. The go command
// runs with the environment of this process; env adds variables (for example GOWORK=off).
func Export(root, dir string, env ...string) (*Manifest, error) {
	cmd := exec.Command("go", "test", "./bench", "-run", "^TestExportWorkloads$", "-count=1")
	cmd.Dir = root
	cmd.Env = append(append(os.Environ(), env...), "SXIR_BENCH_EXPORT="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("exporting the workloads of %s (go test ./bench -run TestExportWorkloads): %w\n%s", root, err, out)
	}
	return Load(dir)
}

// Load reads the manifest of dir and checks it: its version, the programs of its rows and that every
// input it names exists.
func Load(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, err
	}
	m := &Manifest{}
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, "manifest.json"), err)
	}
	if m.Version != Version {
		return nil, fmt.Errorf("the workload manifest has version %d; this harness reads version %d", m.Version, Version)
	}
	m.Dir = dir
	var inputs []string
	for _, w := range m.TS {
		inputs = append(inputs, w.Input)
	}
	names := map[string]bool{}
	for _, r := range m.Matrix {
		if names[r.Name] {
			return nil, fmt.Errorf("the workload manifest has row %s twice", r.Name)
		}
		names[r.Name] = true
		if !slices.Contains(Programs, r.Program) {
			return nil, fmt.Errorf("row %s: unknown program %q", r.Name, r.Program)
		}
		if len(r.Inputs) == 0 {
			return nil, fmt.Errorf("row %s has no inputs", r.Name)
		}
		inputs = append(inputs, r.Inputs...)
	}
	for _, in := range inputs {
		if _, err := os.Stat(m.Path(in)); err != nil {
			return nil, fmt.Errorf("the workload manifest names a missing input: %w", err)
		}
	}
	return m, nil
}

// Path returns the path of an input that the manifest names.
func (m *Manifest) Path(rel string) string { return filepath.Join(m.Dir, filepath.FromSlash(rel)) }

// Row returns the matrix row of a name.
func (m *Manifest) Row(name string) (Row, bool) {
	for _, r := range m.Matrix {
		if r.Name == name {
			return r, true
		}
	}
	return Row{}, false
}

// RowNames lists the names of the matrix rows in the manifest's order.
func (m *Manifest) RowNames() []string {
	var names []string
	for _, r := range m.Matrix {
		names = append(names, r.Name)
	}
	return names
}
