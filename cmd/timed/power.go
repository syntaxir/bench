package main

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// The power guard. A timed capture is valid only on AC power with at least a floor of charge
// (BENCHLOCK_MIN_CHARGE, default 50, on AC too) for the whole run: on battery the system may lower
// performance, and charging a low battery adds heat. SyntaxIR's benchlock.sh refuses to start an
// exclusive job otherwise, reads the power every 30 seconds while the job runs into the file that
// SXIR_POWER_LOG names, and fails the job when a reading was bad. The harness checks again: it
// refuses to start on bad power, and each part's header records the readings before and after the
// part (power-start, power-end) and the verdict over them and over the log (power-check). On a Mac a
// failed probe, or a reading that names neither AC nor battery, is refused like battery. A system
// with no probe (not a Mac) may run, but its parts are invalid, as nothing checked their power. A
// part measured with -force is invalid. release refuses results with an invalid part.

// powerReading is one reading of the power state.
type powerReading struct {
	source string // "ac", "battery" or "unknown"
	charge int    // percent, or -1 when the system reports none (no battery)
	probe  string // "pmset", "failed" (pmset did not run), "none" (no probe on this system) or, in benchlock.sh's tests, "test"
}

// String returns the reading in the form of benchlock.sh's power log, "source=S charge=N|- probe=P".
func (r powerReading) String() string {
	c := "-"
	if r.charge >= 0 {
		c = strconv.Itoa(r.charge)
	}
	return "source=" + r.source + " charge=" + c + " probe=" + r.probe
}

// refusal returns why the reading does not allow a timed run, or "" when it does, in the words of
// benchlock.sh. A source that is neither AC nor battery is refused unless the system has no probe.
func (r powerReading) refusal(floor int) string {
	switch {
	case r.source != "ac" && r.source != "battery" && r.probe == "none":
		return ""
	case r.source != "ac" && r.source != "battery" && r.probe == "failed":
		return "on unknown power: the power probe failed"
	case r.source != "ac" && r.source != "battery":
		return "on unknown power: the probe named neither AC nor battery"
	case r.source == "battery" && r.charge >= 0:
		return fmt.Sprintf("on battery (%d%%)", r.charge)
	case r.source == "battery":
		return "on battery"
	case r.charge >= 0 && r.charge < floor:
		return fmt.Sprintf("at %d%% charge, under the floor of %d%%", r.charge, floor)
	}
	return ""
}

// unchecked returns why the reading leaves a timed part invalid: its refusal, or, on a system with
// no probe, that nothing checked the power. It returns "" for a reading that allows timing.
func (r powerReading) unchecked(floor int) string {
	if why := r.refusal(floor); why != "" {
		return why
	}
	if r.source != "ac" && r.source != "battery" {
		return "on unknown power: the system has no power probe"
	}
	return ""
}

var pmsetCharge = regexp.MustCompile(`\s(\d+)%;`)

// parsePmset reads the output of pmset -g batt.
func parsePmset(out string) powerReading {
	r := powerReading{source: "unknown", charge: -1, probe: "pmset"}
	switch {
	case strings.Contains(out, "drawing from 'AC Power'"):
		r.source = "ac"
	case strings.Contains(out, "drawing from 'Battery Power'"):
		r.source = "battery"
	}
	if m := pmsetCharge.FindStringSubmatch(out); m != nil {
		r.charge, _ = strconv.Atoi(m[1])
	}
	return r
}

// parseReading reads the source=, charge= and probe= fields of a line of benchlock.sh's power log,
// after its time. ok is false when the line has no source or no probe.
func parseReading(fields string) (r powerReading, ok bool) {
	r.charge = -1
	for _, f := range strings.Fields(fields) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "source":
			r.source, ok = v, v != ""
		case "charge":
			if n, err := strconv.Atoi(v); err == nil {
				r.charge = n
			}
		case "probe":
			r.probe = v
		}
	}
	return r, ok && r.probe != ""
}

// readPower reads the power state: pmset -g batt on macOS (probe=failed when it does not run), and
// source=unknown probe=none elsewhere. Tests replace it.
var readPower = func() powerReading {
	if runtime.GOOS != "darwin" {
		return powerReading{source: "unknown", charge: -1, probe: "none"}
	}
	out, err := pmsetBatt()
	if err != nil {
		return powerReading{source: "unknown", charge: -1, probe: "failed"}
	}
	return parsePmset(string(out))
}

// pmsetBatt returns the output of pmset -g batt. Tests replace it.
var pmsetBatt = func() ([]byte, error) { return exec.Command("pmset", "-g", "batt").Output() }

// powerFloor returns BENCHLOCK_MIN_CHARGE, the least charge in percent of a timed run, default 50.
func powerFloor() (int, error) {
	v := os.Getenv("BENCHLOCK_MIN_CHARGE")
	if v == "" {
		return 50, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 100 {
		return 0, fmt.Errorf("BENCHLOCK_MIN_CHARGE must be a percentage from 0 to 100, not %q", v)
	}
	return n, nil
}

// powerGuard is the power check of a capture.
type powerGuard struct {
	forced bool   // -force: no check, and every part is invalid
	floor  int    // BENCHLOCK_MIN_CHARGE
	log    string // the power log of the benchlock.sh job (SXIR_POWER_LOG)
}

// timedPower checks the power before a timed capture: it refuses on battery, under the floor, on a
// failed probe or an unrecognised source, and without the power log of a current benchlock.sh. With force it checks nothing, and the parts of the
// capture are invalid.
func timedPower(force bool) (*powerGuard, error) {
	floor, err := powerFloor()
	if err != nil {
		return nil, err
	}
	g := &powerGuard{forced: force, floor: floor}
	if force {
		return g, nil
	}
	if g.log = os.Getenv("SXIR_POWER_LOG"); g.log == "" {
		return nil, errors.New("SXIR_POWER_LOG is not set: the benchlock.sh that holds the lock predates the power guard; " +
			"run the timed capture under the current .agents/scripts/benchlock.sh of SyntaxIR")
	}
	if why := readPower().refusal(floor); why != "" {
		return nil, fmt.Errorf("a timed capture needs AC power and at least %d%% charge (BENCHLOCK_MIN_CHARGE), and the machine is %s", floor, why)
	}
	return g, nil
}

// check returns the power-check line of a part that ran between the readings start and end: "ok",
// or "invalid: " and the first bad reading, with the number of bad readings when there are more.
// The readings of the power log count too, so that a part that went on battery and back fails. A
// reading of unknown power is bad even where the system has no probe: nothing checked the part.
func (g *powerGuard) check(start, end powerReading) string {
	if g.forced {
		return "invalid: measured with -force, without the power guard"
	}
	var bad []string
	add := func(when string, r powerReading) {
		if why := r.unchecked(g.floor); why != "" {
			bad = append(bad, why+" "+when)
		}
	}
	add("at the start of the part", start)
	data, err := os.ReadFile(g.log)
	if err != nil {
		return "invalid: the power log cannot be read: " + err.Error()
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		when, fields, _ := strings.Cut(line, " ")
		r, ok := parseReading(fields)
		if !ok {
			return fmt.Sprintf("invalid: the power log has an unreadable line %q", line)
		}
		add("at "+when, r)
	}
	add("at the end of the part", end)
	switch len(bad) {
	case 0:
		return "ok"
	case 1:
		return "invalid: " + bad[0]
	}
	return fmt.Sprintf("invalid: %s, and %d more bad readings", bad[0], len(bad)-1)
}

// runPower returns the run-power figure of the results: "ok" when every timed file was measured
// under the power guard and passed it; "pre-method: " and the files without a lock line, which predate
// the timed method and the guard; "invalid: " and the files that failed it, measured with -force or
// without a power check. The meta keys of the files are those of renderDocWith: the Go results'
// unprefixed, the TypeScript and wedge results' prefixed with "ts." and "wedge.".
func runPower(meta map[string]string) string {
	var pre, invalid []string
	for _, part := range []struct{ prefix, file string }{{"", "results.txt"}, {"ts.", "results-ts.txt"}, {"wedge.", "results-wedge.txt"}} {
		if part.prefix != "" && !slices.ContainsFunc(slices.Collect(maps.Keys(meta)), func(k string) bool { return strings.HasPrefix(k, part.prefix) }) {
			continue // no such file
		}
		lock, check := meta[part.prefix+"lock"], meta[part.prefix+"power-check"]
		switch {
		case lock == "":
			pre = append(pre, part.file)
		case strings.HasPrefix(lock, "unlocked"):
			invalid = append(invalid, part.file+" measured with -force")
		case check == "":
			invalid = append(invalid, part.file+" has no power check")
		case check != "ok":
			invalid = append(invalid, part.file+" "+check)
		}
	}
	switch {
	case len(invalid) > 0:
		return "invalid: " + strings.Join(invalid, "; ")
	case len(pre) > 0:
		return "pre-method: " + strings.Join(pre, ", ")
	}
	return "ok"
}

// legacyReleases are the results releases made before the power guard, whose figures have no
// run-power; pin accepts them without it, and no other.
var legacyReleases = map[string]bool{"results-v1": true, "results-v2": true}

// releasable returns why results with the figure values may not be released or pinned, or "".
func releasable(values map[string]string, tag string) string {
	v, ok := values["run-power"]
	switch {
	case !ok && legacyReleases[tag]:
		return ""
	case !ok:
		return "the figures have no run-power, and only results-v1 and results-v2 predate the power guard"
	case strings.HasPrefix(v, "invalid"):
		return "run-power is " + v
	}
	return ""
}
