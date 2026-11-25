package workloads

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// manifest writes a manifest and the inputs a, b and c into a temporary directory.
func manifest(t *testing.T, text string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"ts/a.txt", "matrix/x/b.txt", "matrix/x/c.txt"} {
		path := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("input"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const valid = `{
  "version": 1,
  "ts": [{"name": "A", "grammar": "g/a.sxir", "input": "ts/a.txt"}],
  "matrix": [
    {"name": "x", "grammar": "g/x.sxir", "gen": ["-types"], "program": "parse", "inputs": ["matrix/x/b.txt"]},
    {"name": "x-stream", "grammar": "g/x.sxir", "gen": ["-types"], "extra": ["-stream"], "program": "stream", "inputs": ["matrix/x/c.txt"]}
  ]
}`

func TestLoad(t *testing.T) {
	dir := manifest(t, valid)
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.TS) != 1 || m.TS[0].Name != "A" || m.Path(m.TS[0].Input) != filepath.Join(dir, "ts", "a.txt") {
		t.Errorf("TS = %+v", m.TS)
	}
	if got := strings.Join(m.RowNames(), " "); got != "x x-stream" {
		t.Errorf("rows = %s", got)
	}
	if r, ok := m.Row("x-stream"); !ok || r.Program != "stream" || strings.Join(r.Extra, " ") != "-stream" {
		t.Errorf("row x-stream = %+v, %v", r, ok)
	}
	if _, ok := m.Row("y"); ok {
		t.Error("an unknown row was found")
	}
}

// TestLoadRefuses checks that a manifest of another version, with an unknown program, a missing
// input, a row without inputs or a row named twice is refused.
func TestLoadRefuses(t *testing.T) {
	for name, c := range map[string]struct{ old, new, want string }{
		"version":   {`"version": 1`, `"version": 2`, "version 2"},
		"program":   {`"program": "stream"`, `"program": "edit"`, "unknown program"},
		"input":     {`"matrix/x/c.txt"`, `"matrix/x/d.txt"`, "missing input"},
		"no inputs": {`"inputs": ["matrix/x/c.txt"]`, `"inputs": []`, "no inputs"},
		"twice":     {`"name": "x-stream"`, `"name": "x"`, "twice"},
	} {
		_, err := Load(manifest(t, strings.Replace(valid, c.old, c.new, 1)))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one containing %q", name, err, c.want)
		}
	}
}
