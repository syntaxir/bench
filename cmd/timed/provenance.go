package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// provenance is what the header of every results file records about the run: what was measured
// (the commit and where it comes from, the binaries) and by what harness, on what (toolchain, machine, power and thermal state), how
// (benchtime, rounds, arms, the GC settings the processes ran with) and in what conditions (the
// lock and window held, the load before and after).
type provenance struct {
	commit    string
	source    string // the repository and revision of the checkout: its public address, never a local path
	harness   string // the commit of this repository
	rounds    int
	benchtime string
	arms      []string
	baseRev   string
	lock      string
	loadStart string
	loadEnd   string            // the load right after the last sample (measured); read at header if unset
	binaries  map[string]string // by arm: the SHA-256 of its binaries
	extra     []string          // more "key: value" lines (node version, the analysis)

	guard      *powerGuard  // the power guard of a timed capture; nil for a part that measures no time
	powerStart powerReading // the power when the part began
	powerEnd   *powerReading
	powerCheck string // the verdict of the guard over the part, set by measured
}

// newProvenance records what is known before a capture runs.
func newProvenance(commit, lock string, rounds int, benchtime string, arms []string, baseRev string) *provenance {
	return &provenance{commit: commit, rounds: rounds, benchtime: benchtime, arms: arms, baseRev: baseRev,
		lock: lock, loadStart: loadAverage(), binaries: map[string]string{}, powerStart: readPower()}
}

// measured records the load and the power right after the last sample of a capture, before its
// analysis and saving, as those after the run, and the power guard's verdict over the part.
func (p *provenance) measured() {
	p.loadEnd = loadAverage()
	end := readPower()
	p.powerEnd = &end
	if p.guard != nil {
		p.powerCheck = p.guard.check(p.powerStart, end)
	}
}

// loadAfter returns the load after a run as the header records it. The values are the 1, 5 and 15
// minute averages, which the system updates every few seconds, so after a short run they can equal
// the load before it (before); the line then says so, because it shows no load of the run itself.
func loadAfter(load, before string) string {
	if load != "" && load == before {
		return load + " (unchanged: the 1, 5 and 15 min averages were not updated during the run)"
	}
	return load
}

// header returns the header, written after the run so that it holds the load after it.
func (p *provenance) header() []byte {
	var b bytes.Buffer
	line := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "# %s: %s\n", k, v)
		}
	}
	goVersion, _ := exec.Command("go", "env", "GOVERSION").Output()
	line("date", time.Now().Format("2006-01-02"))
	line("commit", p.commit)
	line("source", p.source)
	line("harness", p.harness)
	line("go", strings.TrimSpace(string(goVersion)))
	line("cores", strconv.Itoa(runtime.NumCPU()))
	end := p.loadEnd
	if end == "" {
		end = loadAverage()
	}
	line("load", p.loadStart)
	line("load-after", loadAfter(end, p.loadStart))
	line("os", osVersion())
	line("hw", cmdLine("sysctl", "-n", "machdep.cpu.brand_string"))
	line("gomaxprocs", envOr("GOMAXPROCS", fmt.Sprintf("%d (default)", runtime.NumCPU())))
	line("gogc", envOr("GOGC", "100 (default)"))
	line("gomemlimit", envOr("GOMEMLIMIT", "off (default)"))
	line("goexperiment", goExperiment())
	line("power", powerState())
	line("power-start", p.powerStart.String())
	if p.powerEnd != nil {
		line("power-end", p.powerEnd.String())
	}
	line("power-check", p.powerCheck)
	line("thermal", thermalState())
	line("benchtime", p.benchtime)
	if p.rounds > 0 {
		line("rounds", strconv.Itoa(p.rounds))
	}
	arms := strings.Join(p.arms, ", ")
	if p.baseRev != "" {
		arms += " (base " + p.baseRev + ")"
	}
	line("arms", arms)
	for _, arm := range p.arms {
		line("binary-"+arm, p.binaries[arm])
	}
	line("lock", p.lock)
	for _, e := range p.extra {
		k, v, _ := strings.Cut(e, ": ")
		line(k, v)
	}
	return b.Bytes()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// cmdLine returns the first line of a command's output, or "" when it fails.
func cmdLine(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	l, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(l)
}

func goExperiment() string {
	if v := cmdLine("go", "env", "GOEXPERIMENT"); v != "" {
		return v
	}
	return "none"
}

func osVersion() string {
	if v := cmdLine("sw_vers", "-productVersion"); v != "" {
		return "macOS " + v + " (" + cmdLine("sw_vers", "-buildVersion") + ")"
	}
	return cmdLine("uname", "-sr")
}

// powerState returns the power source and the low power mode, where the system tells.
func powerState() string {
	src := ""
	if out, err := exec.Command("pmset", "-g", "batt").Output(); err == nil {
		if _, rest, ok := strings.Cut(string(out), "drawing from '"); ok {
			src, _, _ = strings.Cut(rest, "'")
		}
	}
	low := ""
	if out, err := exec.Command("pmset", "-g").Output(); err == nil {
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Fields(l); len(f) == 2 && f[0] == "lowpowermode" {
				low = "lowpowermode " + f[1]
			}
		}
	}
	switch {
	case src != "" && low != "":
		return src + ", " + low
	case src != "":
		return src
	case low != "":
		return low
	}
	return "unknown"
}

// thermalState returns the CPU speed limit that the system reports, or its thermal warning.
func thermalState() string {
	out, err := exec.Command("pmset", "-g", "therm").Output()
	if err != nil {
		return "unknown"
	}
	var parts []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "Note: ")
		if strings.HasPrefix(l, "CPU_Speed_Limit") || strings.Contains(l, "warning level") {
			parts = append(parts, l)
		}
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, "; ")
}

// fileHash returns the SHA-256 of a file, abbreviated to 16 hex digits.
func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])[:16], nil
}

// treeHash returns the SHA-256 of the files under dir, by their relative paths and contents,
// abbreviated to 16 hex digits.
func treeHash(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// timedLock checks that a timed capture runs alone: under the exclusive heavy-run lock of the
// machine's run queue (.agents/scripts/benchlock.sh of SyntaxIR, which names its holder in
// SXIR_BENCHLOCK) in a declared measurement window (.agents/scripts/benchwindow.sh), which pauses the
// test slots and the shared jobs. It returns the "lock" line of the header. With force it measures
// anyway and the header says so.
func timedLock(force bool) (string, error) {
	if force {
		return "unlocked (-force): not a result to cite", nil
	}
	const how = "run it as `<syntaxir>/.agents/scripts/benchwindow.sh open <reason>` and then " +
		"`<syntaxir>/.agents/scripts/benchlock.sh <tag> go run ./cmd/timed ...`, or pass -force for an unlocked experiment"
	state := os.Getenv("SXIR_AGENT_STATE")
	holder := os.Getenv("SXIR_BENCHLOCK")
	if state == "" || holder == "" {
		return "", fmt.Errorf("a timed capture needs the exclusive benchmark lock and a measurement window: %s", how)
	}
	owner, err := os.ReadFile(filepath.Join(state, "benchlock", "owner"))
	if err != nil || strings.TrimSpace(string(owner)) != holder {
		return "", fmt.Errorf("the exclusive benchmark lock is not held by %q: %s", holder, how)
	}
	w, err := readWindow(state)
	if err != nil {
		return "", err
	}
	if w == nil {
		return "", fmt.Errorf("no measurement window is open: %s", how)
	}
	return fmt.Sprintf("exclusive %s; window %s until %s (%s)", holder, w.id, w.until.Format(time.RFC3339), w.reason), nil
}

// window is a declared measurement window: the record $SXIR_AGENT_STATE/window that benchwindow.sh
// writes, one line of "key=value" fields with reason last.
type window struct {
	id, reason string
	until      time.Time
}

// readWindow returns the open window, or nil when none is open or it has expired.
func readWindow(state string) (*window, error) {
	data, err := os.ReadFile(filepath.Join(state, "window"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(data))
	w := &window{}
	head, reason, _ := strings.Cut(line, " reason=")
	w.reason = reason
	for _, f := range strings.Fields(head) {
		k, v, _ := strings.Cut(f, "=")
		switch k {
		case "id":
			w.id = v
		case "until":
			s, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("window record %q: until=%q", line, v)
			}
			w.until = time.Unix(s, 0)
		}
	}
	if w.until.IsZero() {
		return nil, fmt.Errorf("window record %q has no until=", line)
	}
	if !time.Now().Before(w.until) {
		return nil, nil
	}
	return w, nil
}
