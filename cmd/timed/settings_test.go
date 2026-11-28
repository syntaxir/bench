package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/syntaxir/bench/internal/workloads"
)

// settingsFile returns a file of the settings part for profile: the head and control arms and the
// variant guards (10% slower on json, equal on csv) over 20 rounds, and class-tables with no effect
// on csv and no samples on json.
func settingsFile(profile, date, window string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# date: %s\n# commit: abc1234\n# lock: exclusive x; window %s until 2026-11-28T10:00:00Z (settings)\n", date, window)
	fmt.Fprintf(&b, "# profile: %s\n# setting: guards 0 adopted\n# setting: class-tables 5 opt-in\n", profile)
	b.WriteString("# aa-geomean: 1.0010 (+0.10%, limit ±1.5%, accepted)\n")
	for _, row := range []string{"json", "csv"} {
		for _, v := range []string{"head", "control", "guards"} {
			bop := 1000.0
			if v == "guards" && row == "json" {
				bop = 1100
			}
			fmt.Fprintf(&b, "cell %s %s b_op %g\ncell %s %s allocs_op 10\n", row, v, bop, row, v)
		}
	}
	for round := range 20 {
		for _, row := range []string{"json", "csv"} {
			for _, v := range []string{"head", "control", "guards"} {
				ns := 1000.0 + float64(round%3)
				if v == "guards" && row == "json" {
					ns *= 1.1
				}
				fmt.Fprintf(&b, "sample %s %s %d ns_op %g\n", row, v, round+1, ns)
			}
		}
	}
	b.WriteString("\nno effect: the generated Go parser equals the baseline's\nnoeffect csv class-tables\n")
	return b.String()
}

func writeSettings(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, settingsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, settingsDir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSettingsFileName(t *testing.T) {
	stamp := time.Date(2026, 11, 28, 6, 30, 5, 0, time.FixedZone("JST", 9*3600))
	for _, c := range []struct {
		rejected, invalid bool
		want              string
	}{
		{false, false, "settings/20261127T213005Z-speed.txt"},
		{true, false, "settings/20261127T213005Z-speed.rejected.txt"},
		{false, true, "settings/20261127T213005Z-speed.invalid.txt"},
	} {
		if got := filepath.ToSlash(settingsFileName(stamp, "speed", c.rejected, c.invalid)); got != c.want {
			t.Errorf("settingsFileName(rejected %v, invalid %v) = %s, want %s", c.rejected, c.invalid, got, c.want)
		}
	}
}

// The last run of a profile and flag is the latest valid one; the summary judges each row of it.
func TestSettingsRunsAndSummary(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string]string{
		"20261101T000000Z-speed.txt":          settingsFile("speed", "2026-11-01", "w1"),
		"20261128T000000Z-speed.txt":          settingsFile("speed", "2026-11-28", "w2"),
		"20261129T000000Z-speed.rejected.txt": settingsFile("speed", "2026-11-29", "w3"),
		"20261101T000000Z-size.txt":           settingsFile("size", "2026-11-01", "w1"),
	})
	runs, err := loadSettingsRuns(dir)
	if err != nil {
		t.Fatal(err)
	}
	last := lastRuns(runs)
	if r := last["speed guards"]; r == nil || r.window() != "w2" {
		t.Fatalf("last run of speed guards = %+v, want the valid run of window w2", r)
	}
	if r := last["size class-tables"]; r == nil || r.window() != "w1" {
		t.Errorf("last run of size class-tables = %+v, want window w1", r)
	}
	r := last["speed guards"]
	g := r.summarize("guards")
	if g.rows != 2 || g.changed != 2 || g.slower != 1 || g.noClaim != 1 || g.faster != 0 || g.failed != 0 {
		t.Errorf("guards: %+v, want 2 of 2 rows changed, json slower and csv without a claim", g)
	}
	if want := 1.0488; g.time < want-0.001 || g.time > want+0.001 { // sqrt(1.1)
		t.Errorf("guards: time %.4f, want %.4f", g.time, want)
	}
	if want := 1.0488; g.bytes < want-0.001 || g.bytes > want+0.001 || g.allocs != 1 {
		t.Errorf("guards: bytes %.4f, allocs %.4f; want %.4f and 1", g.bytes, g.allocs, want)
	}
	if c := r.summarize("class-tables"); c.rows != 2 || c.changed != 1 || c.failed != 1 {
		t.Errorf("class-tables: %+v, want 1 of 2 rows changed and unmeasured", c)
	}
	doc, err := renderSettings(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Profile speed\n", "## Profile size\n",
		"| `guards` | 0 | adopted | 2 of 2 | 1.049 | 0 faster, 1 slower, 1 none | 1.049 | 1.000 | 2026-11-28 w2 (A/A 1.0010) | abc1234 |",
		"| `class-tables` | 5 | opt-in | 1 of 2 | — | 0 faster, 0 slower, 0 none, 1 unmeasured |",
	} {
		if !bytes.Contains(doc, []byte(want)) {
			t.Errorf("SETTINGS.md has no %q:\n%s", want, doc)
		}
	}
	if bytes.Index(doc, []byte("## Profile speed")) > bytes.Index(doc, []byte("## Profile size")) {
		t.Error("SETTINGS.md does not list speed first")
	}
}

// Pruning keeps the runs that a profile and flag takes its last timing from, and a rejected or
// invalid run newer than every valid run of its profile.
func TestPruneSettingsRuns(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string]string{
		"20261101T000000Z-speed.txt":          settingsFile("speed", "2026-11-01", "w1"),
		"20261102T000000Z-speed.rejected.txt": settingsFile("speed", "2026-11-02", "w2"),
		"20261128T000000Z-speed.txt":          settingsFile("speed", "2026-11-28", "w3"),
		"20261129T000000Z-speed.invalid.txt":  settingsFile("speed", "2026-11-29", "w4"),
		"20261101T000000Z-size.txt":           settingsFile("size", "2026-11-01", "w1"),
	})
	if err := pruneSettingsRuns(dir); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, settingsDir))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	want := []string{"20261101T000000Z-size.txt", "20261128T000000Z-speed.txt", "20261129T000000Z-speed.invalid.txt"}
	if !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}
}

func TestSelectSettings(t *testing.T) {
	m := &workloads.Manifest{Settings: []workloads.Setting{
		{Profile: "speed", Flag: "guards", Go: true},
		{Profile: "speed", Flag: "engine-only"},
		{Profile: "size", Flag: "guards", Go: true},
	}}
	profiles, by, err := selectSettings(m, "", "")
	if err != nil || !slices.Equal(profiles, []string{"speed", "size"}) || len(by["speed"]) != 1 || len(by["size"]) != 1 {
		t.Errorf("all: %v, %v, %v", profiles, by, err)
	}
	if profiles, _, err := selectSettings(m, "guards", "size"); err != nil || !slices.Equal(profiles, []string{"size"}) {
		t.Errorf("guards in size: %v, %v", profiles, err)
	}
	for flags, want := range map[string]string{"nope": "no flag", "engine-only": "does not reach"} {
		if _, _, err := selectSettings(m, flags, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("-settings %s: err = %v, want %q", flags, err, want)
		}
	}
	if _, _, err := selectSettings(m, "", "memory"); err == nil || !strings.Contains(err.Error(), "no profile") {
		t.Errorf("-profiles memory: err = %v", err)
	}
	if _, _, err := selectSettings(&workloads.Manifest{}, "", ""); err == nil || !strings.Contains(err.Error(), "predates") {
		t.Errorf("no settings: err = %v", err)
	}
}

// The listing names the adopted and opt-in settings timed before the milestone or never, and leaves
// out experiments and the current ones.
func TestListStale(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string]string{
		"20261101T000000Z-speed.txt": settingsFile("speed", "2026-11-01", "w1"),
		"20261201T000000Z-size.txt":  settingsFile("size", "2026-12-01", "w2"),
	})
	runs, err := loadSettingsRuns(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := &workloads.Manifest{Settings: []workloads.Setting{
		{Profile: "speed", Flag: "guards", State: "adopted", Go: true},
		{Profile: "size", Flag: "guards", State: "adopted", Go: true},
		{Profile: "speed", Flag: "new-flag", Value: 1, State: "opt-in", Go: true},
		{Profile: "speed", Flag: "trial", Value: 1, State: "experiment", Go: true},
		{Profile: "speed", Flag: "engine-only", State: "adopted"},
	}}
	var b bytes.Buffer
	if err := listStale(&b, m, lastRuns(runs), "2026-11-15"); err != nil {
		t.Fatal(err)
	}
	want := "speed guards=0 (adopted): last timed 2026-11-01 in window w1\n" +
		"speed new-flag=1 (opt-in): no window has timed it\n" +
		"2 adopted and opt-in settings not timed since 2026-11-15, 1 timed since; 1 not timed at all (they do not reach the generated Go parsers or change their output)\n"
	if b.String() != want {
		t.Errorf("listing:\n%s\nwant:\n%s", b.String(), want)
	}
	for _, since := range []string{"", "2026-11", "15.11.2026", "2026-13-01"} {
		if err := listStale(&b, m, lastRuns(runs), since); err == nil || !strings.Contains(err.Error(), "-since") {
			t.Errorf("-since %q: err = %v, want a refusal", since, err)
		}
	}
}
