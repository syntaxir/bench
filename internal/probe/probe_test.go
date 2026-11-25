package probe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTSArgs(t *testing.T) {
	gen := []string{"-expectations", "-types", "-typed-only", "-no-limits", "-nodoc", "-lib", "import", "-module", "m", "-recognize"}
	if got, want := strings.Join(TSArgs(gen), " "), "-expectations -types -no-limits -recognize"; got != want {
		t.Errorf("TSArgs = %q, want %q", got, want)
	}
	if got := TSArgs(nil); len(got) != 0 {
		t.Errorf("TSArgs(nil) = %v", got)
	}
}

func TestSizeOfFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte(strings.Repeat("abc", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, gz, err := SizeOfFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if raw != 3000 || gz <= 0 || gz >= 100 {
		t.Errorf("raw %d, gzip %d: a repetitive file of 3000 bytes should compress to under 100", raw, gz)
	}
	if _, _, err := SizeOfFile(path + ".none"); err == nil {
		t.Errorf("a missing file has a size")
	}
}
