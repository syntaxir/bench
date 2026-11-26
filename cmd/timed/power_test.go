package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// stubPower makes readPower return the readings in turn, the last one from then on.
func stubPower(t *testing.T, readings ...powerReading) {
	t.Helper()
	old := readPower
	t.Cleanup(func() { readPower = old })
	readPower = func() powerReading {
		r := readings[0]
		if len(readings) > 1 {
			readings = readings[1:]
		}
		return r
	}
}

var (
	ac100   = powerReading{source: "ac", charge: 100, probe: "pmset"}
	ac30    = powerReading{source: "ac", charge: 30, probe: "pmset"}
	bat80   = powerReading{source: "battery", charge: 80, probe: "pmset"}
	failed  = powerReading{source: "unknown", charge: -1, probe: "failed"}
	noProbe = powerReading{source: "unknown", charge: -1, probe: "none"}
)

func TestParsePmset(t *testing.T) {
	for _, c := range []struct {
		out  string
		want powerReading
	}{
		{"Now drawing from 'AC Power'\n -InternalBattery-0 (id=22282339)\t51%; charging; 1:46 remaining present: true\n", powerReading{"ac", 51, "pmset"}},
		{"Now drawing from 'Battery Power'\n -InternalBattery-0 (id=1)\t8%; discharging; 0:20 remaining present: true\n", powerReading{"battery", 8, "pmset"}},
		{"Now drawing from 'AC Power'\n", powerReading{"ac", -1, "pmset"}}, // a Mac without a battery
		{"Now drawing from 'UPS Power'\n", powerReading{"unknown", -1, "pmset"}},
		{"", powerReading{"unknown", -1, "pmset"}},
	} {
		if got := parsePmset(c.out); got != c.want {
			t.Errorf("parsePmset(%q) = %+v, want %+v", c.out, got, c.want)
		}
	}
}

// On macOS a pmset that fails reads as a failed probe, which refuses; elsewhere there is no probe.
func TestReadPower(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if got := readPower(); got != noProbe {
			t.Errorf("readPower off macOS: %+v", got)
		}
		return
	}
	old := pmsetBatt
	t.Cleanup(func() { pmsetBatt = old })
	pmsetBatt = func() ([]byte, error) { return nil, errors.New("no pmset") }
	if got := readPower(); got != failed {
		t.Errorf("readPower with a failing pmset: %+v", got)
	}
	pmsetBatt = func() ([]byte, error) {
		return []byte("Now drawing from 'AC Power'\n -InternalBattery-0 (id=1)\t77%; charging; present: true\n"), nil
	}
	if got := readPower(); got != (powerReading{"ac", 77, "pmset"}) {
		t.Errorf("readPower on AC: %+v", got)
	}
}

func TestPowerRefusal(t *testing.T) {
	for _, c := range []struct {
		r    powerReading
		want string
	}{
		{bat80, "on battery (80%)"},
		{powerReading{"battery", -1, "pmset"}, "on battery"},
		{ac30, "at 30% charge, under the floor of 50%"},
		{powerReading{"ac", 50, "pmset"}, ""},
		{powerReading{"ac", -1, "pmset"}, ""},
		{failed, "on unknown power: the power probe failed"},
		{powerReading{"unknown", 80, "pmset"}, "on unknown power: the probe named neither AC nor battery"},
		{noProbe, ""},
	} {
		if got := c.r.refusal(50); got != c.want {
			t.Errorf("%v: refusal %q, want %q", c.r, got, c.want)
		}
	}
	// A reading prints as benchlock.sh writes it, and reads back.
	for _, r := range []powerReading{ac100, bat80, {"ac", -1, "pmset"}, failed, noProbe} {
		if got, ok := parseReading(r.String()); !ok || got != r {
			t.Errorf("parseReading(%q) = %+v, %v", r.String(), got, ok)
		}
	}
	if _, ok := parseReading("probe=pmset"); ok {
		t.Error("a reading without a source parsed")
	}
	if _, ok := parseReading("source=ac charge=100"); ok {
		t.Error("a reading without a probe parsed")
	}
}

// A timed capture refuses to start on battery, under the floor (on AC too), on a failed probe and
// without the power log of a current benchlock.sh; a system with no probe starts; -force starts
// unchecked.
func TestTimedPower(t *testing.T) {
	log := filepath.Join(t.TempDir(), "power.log")
	t.Setenv("BENCHLOCK_MIN_CHARGE", "")
	t.Setenv("SXIR_POWER_LOG", "")
	stubPower(t, bat80)
	if g, err := timedPower(true); err != nil || !g.forced {
		t.Errorf("-force: %+v, %v", g, err)
	}
	if _, err := timedPower(false); err == nil || !strings.Contains(err.Error(), "predates the power guard") {
		t.Errorf("without SXIR_POWER_LOG: %v", err)
	}
	t.Setenv("SXIR_POWER_LOG", log)
	if _, err := timedPower(false); err == nil || !strings.Contains(err.Error(), "on battery (80%)") {
		t.Errorf("on battery: %v", err)
	}
	stubPower(t, ac30)
	if _, err := timedPower(false); err == nil || !strings.Contains(err.Error(), "at 30% charge, under the floor of 50%") {
		t.Errorf("under the floor: %v", err)
	}
	stubPower(t, failed)
	if _, err := timedPower(false); err == nil || !strings.Contains(err.Error(), "the power probe failed") {
		t.Errorf("a failed probe: %v", err)
	}
	stubPower(t, noProbe)
	if _, err := timedPower(false); err != nil {
		t.Errorf("no probe: %v", err)
	}
	stubPower(t, ac30)
	t.Setenv("BENCHLOCK_MIN_CHARGE", "20")
	if g, err := timedPower(false); err != nil || g.floor != 20 || g.log != log {
		t.Errorf("above a lower floor: %+v, %v", g, err)
	}
	t.Setenv("BENCHLOCK_MIN_CHARGE", "x")
	if _, err := timedPower(false); err == nil {
		t.Error("an invalid floor: no error")
	}
}

// The verdict over a part counts the readings before and after it and every reading of the log, so a
// part that went on battery and came back is invalid; -force is invalid.
func TestPowerCheck(t *testing.T) {
	log := filepath.Join(t.TempDir(), "power.log")
	g := &powerGuard{floor: 50, log: log}
	set := func(lines ...string) {
		if err := os.WriteFile(log, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	good := "2026-11-26T10:00:00 source=ac charge=100 probe=pmset"
	set(good, good)
	if got := g.check(ac100, ac100); got != "ok" {
		t.Errorf("all on AC: %q", got)
	}
	set(good, "2026-11-26T10:00:30 source=battery charge=70 probe=pmset", good)
	if got := g.check(ac100, ac100); got != "invalid: on battery (70%) at 2026-11-26T10:00:30" {
		t.Errorf("battery between: %q", got)
	}
	set(good)
	if got := g.check(bat80, ac30); got != "invalid: on battery (80%) at the start of the part, and 1 more bad readings" {
		t.Errorf("bad start and end: %q", got)
	}
	if got := g.check(ac100, ac30); got != "invalid: at 30% charge, under the floor of 50% at the end of the part" {
		t.Errorf("bad end: %q", got)
	}
	set(good, "garbage")
	if got := g.check(ac100, ac100); !strings.HasPrefix(got, "invalid: the power log has an unreadable line") {
		t.Errorf("unreadable log: %q", got)
	}
	if got := (&powerGuard{floor: 50, log: log + ".missing"}).check(ac100, ac100); !strings.HasPrefix(got, "invalid: the power log cannot be read") {
		t.Errorf("missing log: %q", got)
	}
	// Unknown power is never ok: a probe that failed in the run, or a system with no probe.
	set(good, "2026-11-26T10:00:30 source=unknown charge=- probe=failed", good)
	if got := g.check(ac100, ac100); got != "invalid: on unknown power: the power probe failed at 2026-11-26T10:00:30" {
		t.Errorf("probe failure between: %q", got)
	}
	set("2026-11-26T10:00:00 source=unknown charge=- probe=none")
	if got := g.check(noProbe, noProbe); got != "invalid: on unknown power: the system has no power probe at the start of the part, and 2 more bad readings" {
		t.Errorf("no probe: %q", got)
	}
	set(good)
	if got := g.check(ac100, failed); got != "invalid: on unknown power: the power probe failed at the end of the part" {
		t.Errorf("probe failure at the end: %q", got)
	}
	if got := (&powerGuard{forced: true}).check(ac100, ac100); got != "invalid: measured with -force, without the power guard" {
		t.Errorf("-force: %q", got)
	}
}

// The header of a part records the power before and after it and the guard's verdict; a part that
// measures no time has no verdict.
func TestProvenancePower(t *testing.T) {
	log := filepath.Join(t.TempDir(), "power.log")
	if err := os.WriteFile(log, []byte("2026-11-26T10:00:00 source=ac charge=100 probe=pmset\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubPower(t, ac100, ac30)
	p := newProvenance("abc", "exclusive x 1", 1, "1s", nil, "")
	p.guard = &powerGuard{floor: 50, log: log}
	p.measured()
	h := string(p.header())
	for _, want := range []string{"# power-start: source=ac charge=100 probe=pmset\n", "# power-end: source=ac charge=30 probe=pmset\n",
		"# power-check: invalid: at 30% charge, under the floor of 50% at the end of the part\n"} {
		if !strings.Contains(h, want) {
			t.Errorf("header lacks %q:\n%s", want, h)
		}
	}
	stubPower(t, ac100)
	q := newProvenance("abc", "", 1, "1s", nil, "")
	q.measured()
	if h := string(q.header()); strings.Contains(h, "# power-check:") || !strings.Contains(h, "# power-end: source=ac charge=100 probe=pmset\n") {
		t.Errorf("a part without the guard:\n%s", h)
	}
}

func TestRunPower(t *testing.T) {
	ok := map[string]string{"lock": "exclusive x 1; window w", "power-check": "ok"}
	with := func(base map[string]string, kv ...string) map[string]string {
		m := map[string]string{"date": "2026-11-26", "cpu": "M3"}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	for _, c := range []struct {
		name string
		meta map[string]string
		want string
	}{
		{"before the method", with(nil, "ts.date", "2026-11-13"), "pre-method: results.txt, results-ts.txt"},
		{"all guarded", with(ok, "ts.lock", "exclusive x 1", "ts.power-check", "ok", "wedge.lock", "exclusive x 1", "wedge.power-check", "ok"), "ok"},
		{"no TypeScript file", with(ok), "ok"},
		{"TypeScript from before the method", with(ok, "ts.date", "2026-11-13"), "pre-method: results-ts.txt"},
		{"-force", with(nil, "lock", "unlocked (-force): not a result to cite", "power-check", "invalid: measured with -force, without the power guard"), "invalid: results.txt measured with -force"},
		{"no power check", with(nil, "lock", "exclusive x 1"), "invalid: results.txt has no power check"},
		{"battery", with(ok, "ts.lock", "exclusive x 1", "ts.power-check", "invalid: on battery (70%) at t"), "invalid: results-ts.txt invalid: on battery (70%) at t"},
		{"invalid wins", with(nil, "ts.lock", "unlocked (-force)"), "invalid: results-ts.txt measured with -force"},
	} {
		if got := runPower(c.meta); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestReleasable(t *testing.T) {
	for _, c := range []struct {
		values map[string]string
		tag    string
		ok     bool
	}{
		{map[string]string{"run-power": "ok"}, "results-v3", true},
		{map[string]string{"run-power": "pre-method: results.txt"}, "results-v3", true},
		{map[string]string{"run-power": "invalid: results.txt measured with -force"}, "results-v3", false},
		{map[string]string{"run-power": "invalid: results.txt measured with -force"}, "results-v2", false},
		{map[string]string{}, "results-v2", true},
		{map[string]string{}, "results-v1", true},
		{map[string]string{}, "results-v3", false},
	} {
		if why := releasable(c.values, c.tag); (why == "") != c.ok {
			t.Errorf("%v %s: %q", c.values, c.tag, why)
		}
	}
}

// pin refuses a release whose power verdict is invalid, and a new release without one, before it
// writes anything into the checkout.
func TestPinRefusesUnguardedResults(t *testing.T) {
	for _, c := range []struct {
		tag, figures, want string
	}{
		{"results-v3", `{"figures": {"run-commit": "abc", "run-power": "invalid: results.txt measured with -force"}}`, "run-power is invalid"},
		{"results-v3", `{"figures": {"run-commit": "abc"}}`, "the figures have no run-power"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/figures.json") {
				w.Write([]byte(c.figures))
				return
			}
			w.Write([]byte("# results\n"))
		}))
		main := t.TempDir()
		err := pinCmd([]string{"-main", main, "-tag", c.tag, "-repo", srv.URL})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: %v, want %q", c.tag, c.figures, err, c.want)
		}
		if _, err := os.Stat(filepath.Join(main, filepath.FromSlash(pinFile))); !os.IsNotExist(err) {
			t.Errorf("%s: the pin was written: %v", c.figures, err)
		}
	}
}
