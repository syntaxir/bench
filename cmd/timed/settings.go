package main

import (
	"bytes"
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/syntaxir/bench/internal/workloads"
)

// The settings part times each optimization flag of the checkout set against each profile, the
// per-setting timing of SyntaxIR's design record 080 ("Re-Measurement"). For each profile it runs
// cmd/profilematrix on every row of the manifest with the profile's own parsers as the baseline (the
// variant head, -optimize PROFILE), their control copy, and a variant per setting, named by its flag,
// with the options of the setting (workloads.Setting). It saves the output of each profile in
// results/settings/STAMP-PROFILE.txt, STAMP the UTC time of the save, with the provenance of the run
// and a "setting: FLAG VALUE STATE" header line per variant. SETTINGS.md is rendered from these
// files: for each profile and flag, the run that last covered it. A capture removes the files that
// no profile and flag still takes its last run from.

// settingsDir is the directory of the results that holds the files of the settings part.
const settingsDir = "settings"

// selectSettings returns the settings of the manifest that the capture times, by profile in the
// manifest's order: those that reach the generated Go parsers, narrowed to the flags and profiles
// named (comma-separated; empty for all).
func selectSettings(m *workloads.Manifest, flags, profiles string) ([]string, map[string][]workloads.Setting, error) {
	if len(m.Settings) == 0 {
		return nil, nil, errors.New("the checkout's workload manifest has no settings: it predates the settings part")
	}
	split := func(s string) []string {
		var out []string
		for _, f := range strings.Split(s, ",") {
			if f = strings.TrimSpace(f); f != "" {
				out = append(out, f)
			}
		}
		return out
	}
	wantFlags, wantProfiles := split(flags), split(profiles)
	for _, f := range wantFlags {
		i := slices.IndexFunc(m.Settings, func(v workloads.Setting) bool { return v.Flag == f })
		if i < 0 {
			return nil, nil, fmt.Errorf("-settings: the checkout has no flag %q", f)
		}
		if !m.Settings[i].Go {
			return nil, nil, fmt.Errorf("-settings: flag %s does not reach the generated Go parsers, or changes their output: the timing does not measure it", f)
		}
	}
	for _, p := range wantProfiles {
		if !slices.Contains(m.Profiles(), p) {
			return nil, nil, fmt.Errorf("-profiles: the checkout has no profile %q (it has %s)", p, strings.Join(m.Profiles(), ", "))
		}
	}
	var order []string
	byProfile := map[string][]workloads.Setting{}
	for _, v := range m.Settings {
		if !v.Go || len(wantFlags) > 0 && !slices.Contains(wantFlags, v.Flag) ||
			len(wantProfiles) > 0 && !slices.Contains(wantProfiles, v.Profile) {
			continue
		}
		if byProfile[v.Profile] == nil {
			order = append(order, v.Profile)
		}
		byProfile[v.Profile] = append(byProfile[v.Profile], v)
	}
	return order, byProfile, nil
}

// captureSettings runs cmd/profilematrix for one profile: the profile's own parsers as the baseline,
// their control and a variant per setting, on every row of the manifest, and returns its output.
func captureSettings(c *timedConfig, m *workloads.Manifest, profile string, vs []workloads.Setting) ([]byte, error) {
	cmd := exec.Command("go", "run", "./cmd/profilematrix", "-variant", armHead+"=-optimize "+profile, "-control",
		"-rounds", strconv.Itoa(c.rounds), "-benchtime", c.benchtime, "-root", c.root, "-manifest", m.Dir)
	for _, v := range vs {
		cmd.Args = append(cmd.Args, "-variant", v.Flag+"="+strings.Join(v.Args, " "))
	}
	cmd.Dir = c.harness
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	fmt.Fprintf(os.Stderr, "running %s\n", strings.Join(cmd.Args, " "))
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("running profilematrix for profile %s: %w", profile, err)
	}
	return out.Bytes(), nil
}

// settingsFileName returns the name of a file of the settings part, under the directory of the
// results: STAMP-PROFILE.txt, or with .rejected.txt for a run that the A/A judgment rejects and
// .invalid.txt for one measured without the lock or with a failed power check.
func settingsFileName(stamp time.Time, profile string, rejected, invalid bool) string {
	name := stamp.UTC().Format("20060102T150405Z") + "-" + profile
	switch {
	case rejected:
		name += ".rejected"
	case invalid:
		name += ".invalid"
	}
	return filepath.Join(settingsDir, name+".txt")
}

// settingsRun is a saved file of the settings part.
type settingsRun struct {
	file    string // relative to the directory of the results
	stamp   string
	profile string
	valid   bool // neither rejected nor invalid
	meta    map[string]string
	// settings are the variants of the run, by flag.
	settings map[string]settingEntry
	flags    []string
	// samples are the times of the rounds, by variant and row; cells the other values.
	samples  map[string]map[string][]float64
	cells    map[string]map[string]map[string]float64 // variant, row, metric
	noEffect map[string]map[string]bool               // variant, row
	rows     []string
}

type settingEntry struct {
	value int
	state string
}

// window returns the measurement window of the run's lock line, "window ID".
func (r *settingsRun) window() string {
	_, after, ok := strings.Cut(r.meta["lock"], "window ")
	if !ok {
		return "none"
	}
	id, _, _ := strings.Cut(after, " ")
	return id
}

// parseSettingsRun reads a file of the settings part.
func parseSettingsRun(file string, data []byte) (*settingsRun, error) {
	base := strings.TrimSuffix(filepath.Base(file), ".txt")
	r := &settingsRun{file: filepath.ToSlash(file), meta: map[string]string{}, settings: map[string]settingEntry{},
		samples: map[string]map[string][]float64{}, cells: map[string]map[string]map[string]float64{},
		noEffect: map[string]map[string]bool{}}
	r.valid = !strings.HasSuffix(base, ".rejected") && !strings.HasSuffix(base, ".invalid")
	base = strings.TrimSuffix(strings.TrimSuffix(base, ".rejected"), ".invalid")
	var ok bool
	if r.stamp, r.profile, ok = strings.Cut(base, "-"); !ok {
		return nil, fmt.Errorf("%s: not named STAMP-PROFILE.txt", file)
	}
	seenRow := map[string]bool{}
	addRow := func(row string) {
		if !seenRow[row] {
			seenRow[row] = true
			r.rows = append(r.rows, row)
		}
	}
	for _, l := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(strings.TrimPrefix(l, "# "), ": "); ok && strings.HasPrefix(l, "# ") {
			if k == "setting" {
				f := strings.Fields(v)
				if len(f) != 3 {
					return nil, fmt.Errorf("%s: setting line %q: want FLAG VALUE STATE", file, l)
				}
				n, err := strconv.Atoi(f[1])
				if err != nil {
					return nil, fmt.Errorf("%s: setting line %q: %w", file, l, err)
				}
				r.settings[f[0]] = settingEntry{value: n, state: f[2]}
				r.flags = append(r.flags, f[0])
				continue
			}
			r.meta[k] = v
			continue
		}
		f := strings.Fields(l)
		switch {
		case len(f) == 5 && f[0] == "cell":
			x, err := strconv.ParseFloat(f[4], 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %q: %w", file, l, err)
			}
			if r.cells[f[2]] == nil {
				r.cells[f[2]] = map[string]map[string]float64{}
			}
			if r.cells[f[2]][f[1]] == nil {
				r.cells[f[2]][f[1]] = map[string]float64{}
			}
			r.cells[f[2]][f[1]][f[3]] = x
			addRow(f[1])
		case len(f) == 6 && f[0] == "sample" && f[4] == "ns_op":
			x, err := strconv.ParseFloat(f[5], 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %q: %w", file, l, err)
			}
			if r.samples[f[2]] == nil {
				r.samples[f[2]] = map[string][]float64{}
			}
			r.samples[f[2]][f[1]] = append(r.samples[f[2]][f[1]], x)
		case len(f) == 3 && f[0] == "noeffect":
			if r.noEffect[f[2]] == nil {
				r.noEffect[f[2]] = map[string]bool{}
			}
			r.noEffect[f[2]][f[1]] = true
			addRow(f[1])
		}
	}
	if r.meta["profile"] != r.profile {
		return nil, fmt.Errorf("%s: the header names profile %q", file, r.meta["profile"])
	}
	if len(r.flags) == 0 {
		return nil, fmt.Errorf("%s: no setting lines", file)
	}
	return r, nil
}

// loadSettingsRuns reads the files of the settings part in dir (the directory of the results), in
// the order of their stamps. A directory without them holds no runs.
func loadSettingsRuns(dir string) ([]*settingsRun, error) {
	entries, err := os.ReadDir(filepath.Join(dir, settingsDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []*settingsRun
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		rel := filepath.Join(settingsDir, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			return nil, err
		}
		r, err := parseSettingsRun(rel, data)
		if err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	slices.SortStableFunc(runs, func(a, b *settingsRun) int { return cmp.Compare(a.stamp, b.stamp) })
	return runs, nil
}

// lastRuns returns, for each profile and flag ("PROFILE FLAG"), the valid run that last covered it.
func lastRuns(runs []*settingsRun) map[string]*settingsRun {
	last := map[string]*settingsRun{}
	for _, r := range runs {
		if !r.valid {
			continue
		}
		for _, f := range r.flags {
			last[r.profile+" "+f] = r // runs are in the order of their stamps
		}
	}
	return last
}

// pruneSettingsRuns removes the files of dir that no profile and flag takes its last run from: the
// valid runs that later ones cover entirely, and the rejected or invalid runs older than a valid run
// of their profile.
func pruneSettingsRuns(dir string) error {
	runs, err := loadSettingsRuns(dir)
	if err != nil {
		return err
	}
	used := map[*settingsRun]bool{}
	newestValid := map[string]string{}
	for _, r := range lastRuns(runs) {
		used[r] = true
		newestValid[r.profile] = max(newestValid[r.profile], r.stamp)
	}
	for _, r := range runs {
		if used[r] || !r.valid && r.stamp > newestValid[r.profile] {
			continue
		}
		fmt.Fprintln(os.Stderr, "removing", r.file, "which later runs supersede")
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(r.file))); err != nil {
			return err
		}
	}
	return nil
}

// settingSummary is what SETTINGS.md shows of a setting in its last run.
type settingSummary struct {
	changed, rows   int     // rows whose Go parser the setting changes, of the rows measured
	time            float64 // geomean of the variant's median time over the baseline's, over the changed rows
	bytes, allocs   float64 // the same of the bytes and allocations of a pass
	faster, slower  int     // changed rows with a claim, by the rules of the timed method
	noClaim, failed int     // changed rows without one, and rows without the samples to judge
}

// summarize judges a setting of a run against the run's baseline, row by row.
func (r *settingsRun) summarize(flag string) settingSummary {
	var s settingSummary
	var logT, logB, logA float64
	var nB, nA int
	for _, row := range r.rows {
		s.rows++
		if r.noEffect[flag][row] {
			continue
		}
		s.changed++
		h, c, v := r.samples[armHead][row], r.samples[armControl][row], r.samples[flag][row]
		if len(h) == 0 || len(c) == 0 || len(v) == 0 {
			s.failed++
			continue
		}
		ratio, lo, hi, ok := ratioCI(h, v, confidence)
		logT += math.Log(ratio)
		switch d := judge(median(c)/median(h), ratio, lo, hi, ok); {
		case strings.HasPrefix(string(d), "faster"):
			s.faster++
		case strings.HasPrefix(string(d), "slower"):
			s.slower++
		default:
			s.noClaim++
		}
		for metric, acc := range map[string]struct {
			sum *float64
			n   *int
		}{"b_op": {&logB, &nB}, "allocs_op": {&logA, &nA}} {
			hv, hok := r.cells[armHead][row][metric]
			vv, vok := r.cells[flag][row][metric]
			if hok && vok && hv > 0 && vv > 0 {
				*acc.sum += math.Log(vv / hv)
				*acc.n++
			}
		}
	}
	if judged := s.changed - s.failed; judged > 0 {
		s.time = math.Exp(logT / float64(judged))
	}
	if nB > 0 {
		s.bytes = math.Exp(logB / float64(nB))
	}
	if nA > 0 {
		s.allocs = math.Exp(logA / float64(nA))
	}
	return s
}

// renderSettings renders SETTINGS.md from the files of the settings part in dir.
func renderSettings(dir string) ([]byte, error) {
	runs, err := loadSettingsRuns(dir)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`# Settings timings

<!-- Generated by go run ./cmd/timed render from results/settings/; do not edit. -->

The time that each optimization flag of SyntaxIR costs or saves its generated Go parsers, in the
measurement window that last covered it. The settings part of the harness (` + "`go run ./cmd/timed -parts settings`" + `)
generates the rows of the checkout's workload manifest (the language grammars and the CSV
incremental and stream uses) with the flag set against a profile, off where the profile turns it
on and on otherwise, at the value in the table, and times them against the profile's own parsers
and their A/A control by SyntaxIR's [timed measurement
method](https://github.com/syntaxir/syntaxir/blob/main/docs/performance.md#timed-measurement-method).
The deterministic effects of each setting (sizes, allocations, retention, compiler steps) are in
the settings part of SyntaxIR's ` + "`bench/metrics`" + `, which any commit reproduces without a window.

- **Rows**: the rows whose generated Go parser the setting changes, of the rows; the others are
  not built or measured.
- **Time**: the geomean, over the changed rows, of the median time of a pass with the setting over
  that of the profile's parser: below 1 the setting makes the parser faster.
- **Claims**: the changed rows that the method lets one call faster or slower (an interval of the
  ratio without 1 and an effect beyond the row's A/A spread); the other rows have no claim.
- **Bytes**, **Allocs**: the same geomean of the bytes and of the allocations of a pass.
- **Window**: the date and the measurement window of the run, and its A/A geomean; **Commit**: the
  commit of SyntaxIR measured.

` + "`go run ./cmd/timed stale -main ../syntaxir -since DATE`" + ` lists the adopted and opt-in flags whose last
window is older than DATE, the start of the current milestone, or that no window has timed.
`)
	last := lastRuns(runs)
	if len(last) == 0 {
		b.WriteString("\nNo measurement window has timed the settings yet.\n")
		return b.Bytes(), nil
	}
	byProfile := map[string][]string{}
	for key := range last {
		p, f, _ := strings.Cut(key, " ")
		byProfile[p] = append(byProfile[p], f)
	}
	profiles := slices.Sorted(func(yield func(string) bool) {
		for p := range byProfile {
			if !yield(p) {
				return
			}
		}
	})
	// speed, the default profile, first.
	if i := slices.Index(profiles, "speed"); i > 0 {
		profiles = append([]string{"speed"}, slices.Delete(profiles, i, i+1)...)
	}
	ratio := func(x float64) string {
		if x == 0 {
			return "—"
		}
		return strconv.FormatFloat(x, 'f', 3, 64)
	}
	for _, p := range profiles {
		fmt.Fprintf(&b, "\n## Profile %s\n\n", p)
		b.WriteString("| Flag | Value | State | Rows | Time | Claims | Bytes | Allocs | Window | Commit |\n")
		b.WriteString("|:--|--:|:--|--:|--:|:--|--:|--:|:--|:--|\n")
		flags := byProfile[p]
		slices.Sort(flags)
		for _, f := range flags {
			r := last[p+" "+f]
			e := r.settings[f]
			s := r.summarize(f)
			claims := "—"
			if s.changed > 0 {
				claims = fmt.Sprintf("%d faster, %d slower, %d none", s.faster, s.slower, s.noClaim)
				if s.failed > 0 {
					claims += fmt.Sprintf(", %d unmeasured", s.failed)
				}
			}
			aa, _, _ := strings.Cut(r.meta["aa-geomean"], " ")
			fmt.Fprintf(&b, "| `%s` | %d | %s | %d of %d | %s | %s | %s | %s | %s %s (A/A %s) | %s |\n",
				f, e.value, e.state, s.changed, s.rows, ratio(s.time), claims, ratio(s.bytes), ratio(s.allocs),
				r.meta["date"], r.window(), aa, r.meta["commit"])
		}
	}
	return b.Bytes(), nil
}

// staleCmd lists the settings that the timing of the current milestone has not covered: the
// adopted and opt-in flags of a checkout that reach the generated Go parsers and whose last run in
// the results predates -since, or that no run has covered. It does not fail on them: the listing is
// read at the milestone review.
func staleCmd(args []string, w io.Writer) error {
	fl := flag.NewFlagSet("stale", flag.ExitOnError)
	main := fl.String("main", "", "a local SyntaxIR checkout, whose workload manifest names its flags and their states (required)")
	since := fl.String("since", "", "the start of the current milestone, YYYY-MM-DD (required)")
	dir := fl.String("dir", "", "directory of the results files (default: results/ of this repository)")
	fl.Parse(args)
	if *main == "" || *since == "" {
		return errors.New("stale: -main and -since are required")
	}
	if _, err := time.Parse("2006-01-02", *since); err != nil {
		return fmt.Errorf("stale: -since: %w", err)
	}
	if *dir == "" {
		harness, err := harnessRoot()
		if err != nil {
			return err
		}
		*dir = filepath.Join(harness, "results")
	}
	tmp, err := os.MkdirTemp("", "sxir-workloads-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	m, err := workloads.Export(*main, tmp)
	if err != nil {
		return err
	}
	runs, err := loadSettingsRuns(*dir)
	if err != nil {
		return err
	}
	return listStale(w, m, lastRuns(runs), *since)
}

// listStale writes the listing of staleCmd.
func listStale(w io.Writer, m *workloads.Manifest, last map[string]*settingsRun, since string) error {
	if len(m.Settings) == 0 {
		return errors.New("the checkout's workload manifest has no settings: it predates the settings part")
	}
	stale, current, untimed := 0, 0, 0
	for _, v := range m.Settings {
		if v.State != "adopted" && v.State != "opt-in" {
			continue
		}
		if !v.Go {
			untimed++
			continue
		}
		r := last[v.Profile+" "+v.Flag]
		switch {
		case r == nil:
			fmt.Fprintf(w, "%s %s=%d (%s): no window has timed it\n", v.Profile, v.Flag, v.Value, v.State)
		case r.meta["date"] < since:
			fmt.Fprintf(w, "%s %s=%d (%s): last timed %s in window %s\n", v.Profile, v.Flag, v.Value, v.State, r.meta["date"], r.window())
		default:
			current++
			continue
		}
		stale++
	}
	fmt.Fprintf(w, "%d adopted and opt-in settings not timed since %s, %d timed since; %d not timed at all (they do not reach the generated Go parsers or change their output)\n",
		stale, since, current, untimed)
	return nil
}
