# SyntaxIR benchmarks

The timed measurements of [SyntaxIR](https://github.com/syntaxir/syntaxir), a Go parser framework,
and their results. The harness measures a SyntaxIR checkout through its public surface only: its
`sxir` command, `go test` of its `bench` package, and its public packages. It never imports
SyntaxIR's internal packages, so any commit of SyntaxIR that has the benchmarks can be measured and
compared with another.

[`RESULTS.md`](RESULTS.md) holds the latest results, and [`SETTINGS.md`](SETTINGS.md) the timing of
each optimization flag in the measurement window that last covered it. SyntaxIR's site shows the results of a
published release of this repository as its benchmarks page, and SyntaxIR's documents quote the
figures of that release.

## Contents

| Path | What it is |
|:--|:--|
| `cmd/timed` | The harness: runs the benchmarks of a checkout by the timed measurement method, saves the results and renders `RESULTS.md` and `figures.json` |
| `cmd/profilematrix` | Measures what generation options do to the generated parsers of the language grammars |
| `wedge/` | The wedge benchmark: SyntaxIR's generated parsers against other Go parser generators on the same grammars, a module of its own |
| `scripts/ts-parse.mjs` | Times a generated TypeScript parser under Node, one fresh process per measurement |
| `results/` | The saved results, each file with the provenance of its run |
| `RESULTS.md`, `figures.json`, `SETTINGS.md` | Generated from `results/`; never edited by hand |

## Method

The method is SyntaxIR's [timed measurement
method](https://github.com/syntaxir/syntaxir/blob/main/docs/performance.md#timed-measurement-method).
Every measurement runs in arms: head (the checkout), control (a byte-identical copy of head's
binaries, whose difference from head is the noise of the machine) and, with `-base`, base (the same
measurement built from another revision). Each round runs every benchmark once in every arm, each
run a fresh process, with the order of the arms rotated. A part whose geomean A/A ratio is further
than 1.5% from 1 is rejected; a row whose own A/A ratio is further is noisy and is not cited. A claim
against base needs a 95% interval of the ratio that excludes 1 and an effect larger than the row's
A/A spread.

## Running

Go 1.27.1 and Node.js 24 or later (for the TypeScript part):

```bash
go run ./cmd/timed -main ../syntaxir                       # measure a checkout as it is
go run ./cmd/timed -main ../syntaxir -base main -rounds 20 # compare with a revision
go run ./cmd/timed -main https://github.com/syntaxir/syntaxir.git -head v0.3.0
go run ./cmd/timed -main ../syntaxir -parts go -bench 'Parse/JSON' -rounds 2 -force
go run ./cmd/timed render                                  # rewrite RESULTS.md, figures.json, SETTINGS.md
```

`-main` names a local checkout or a Git URL; a URL is cloned into the user's cache directory.
Without `-head` a local checkout is measured as it is; any other revision is checked out into a
temporary worktree, which is removed when the run ends, fails or is interrupted. `-parts` selects
`go`, `gen`, `ts`, `matrix`, `wedge` and `size` (by default) and `settings`; `-bench` and `-grammars` rerun some rows into
`-rerun` files without rendering. `go doc ./cmd/timed` describes every file of `results/`.

## Settings

The `settings` part, which runs only when `-parts` names it, times each optimization flag of the
checkout against each profile: for every setting of the checkout's workload manifest that reaches
the generated Go parsers, `cmd/profilematrix` generates every row with the flag set against the
profile (off where the profile turns it on, on otherwise) and times it against the profile's own
parsers and their A/A control. Each profile's run is saved as `results/settings/STAMP-PROFILE.txt`
with its provenance, and `SETTINGS.md` shows, for each profile and flag, the run that last covered
it. It runs at each milestone review and before a release, over every setting, and for a proposed
state change of a flag, over that flag (`-settings`, and `-profiles` for some profiles). A capture
keeps only the runs that some flag still takes its last timing from, and a rejected or invalid run
newer than those.

```bash
go run ./cmd/timed -main ../syntaxir -parts settings -rounds 20               # every setting
go run ./cmd/timed -main ../syntaxir -parts settings -settings guards -rounds 20
go run ./cmd/timed stale -main ../syntaxir -since 2026-12-01                   # the milestone review
```

`stale` lists the adopted and opt-in flags of the checkout whose last window is older than
`-since`, the start of the current milestone, or that no window has timed. It does not fail: the
review reads it and times the flags it names in the next window.

A timed run needs the machine to itself: it runs only under the exclusive benchmark lock in a
declared measurement window (SyntaxIR's `.agents/scripts/benchlock.sh` and `benchwindow.sh`),
unless `-force`, which marks its results as unlocked and invalid, for local experiments only. Do
not measure on shared CI runners.

A timed run also needs mains power: AC with at least `BENCHLOCK_MIN_CHARGE` percent of charge
(default 50, on AC too) for the whole run. The lock refuses to start otherwise and reads the power
every 30 seconds while the run lasts, into the log it names in `SXIR_POWER_LOG`; the harness
refuses to start on bad power and records each part's readings and verdict in its header
(`power-start`, `power-end`, `power-check`). On a Mac, a failed `pmset` or a reading that names
neither AC nor battery counts as bad power. A part with a bad reading, measured on a system with
no power probe, or measured with `-force`, is invalid. The figure `run-power` sums the verdicts of the timed files: `ok`, `pre-method` for
files measured before the timed method, or `invalid`.

## Releases

A release tagged `results-vN` publishes `RESULTS.md` and `figures.json` of a commit of `main`:

```bash
go run ./cmd/timed release -tag results-v2
go run ./cmd/timed pin -main ../syntaxir -tag results-v2
```

`release` refuses a dirty tree, an `invalid` run-power, generated files that do not match
`results/`, and a commit that is not `main` of the remote. `pin` downloads the release into a
SyntaxIR checkout: it writes `site/benchmarks.json` with the SHA-256 of both assets, the release's
`figures.json` as `site/figures.json`, and the marked figures of SyntaxIR's documents, so that the
bump is one commit. It refuses an `invalid` run-power, and a release without one other than
`results-v1` and `results-v2`, which predate the guard.

## Development

```bash
go vet ./... && go test ./...                              # the harness
SXIR_MAIN=../syntaxir go test ./cmd/profilematrix          # with the tests that build parsers
cd wedge && go work init . ../../syntaxir && go work edit -replace syntaxir.dev/sxir@v0.0.0=../../syntaxir && go test ./... # the wedge, against a checkout
```

The root module uses the standard library only.

## Tools and licenses

| Component | License |
|:--|:--|
| This repository | Apache-2.0 ([`LICENCE`](LICENCE)) |
| [SyntaxIR](https://github.com/syntaxir/syntaxir), measured and generating the parsers of `wedge/sxir` | Apache-2.0 |
| [pigeon](https://github.com/mna/pigeon) v1.3.0, generating the parsers of `wedge/pigeon` | BSD-3-Clause |
| [participle](https://github.com/alecthomas/participle) v2.1.4, used by `wedge/participle` | MIT |

The versions of the wedge's tools are pinned in `wedge/go.mod`.
