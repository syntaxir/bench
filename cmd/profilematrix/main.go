// Command profilematrix measures what generation options do to the generated parsers of the
// language grammars of a SyntaxIR checkout. It generates every grammar once per variant, builds each
// parser into two programs (one for the size of a stripped binary, one to measure), runs the
// measuring program in fresh processes in rotated order, and prints what it recorded:
//
//	go run ./cmd/profilematrix -root ../syntaxir \
//	    -variant speed= -variant size='-optimize size' -variant nosharing='-no-opt method-sharing' \
//	    -control -rounds 8
//
// The rows, their grammars, generation options, measuring programs and inputs are those of the
// checkout's workload manifest (go test ./bench -run TestExportWorkloads with SXIR_BENCH_EXPORT;
// see internal/workloads), which -manifest names, or which profilematrix exports from -root into a
// temporary directory. The checkout is used through its sxir command and public packages only.
//
// A variant is NAME=ARGS, where ARGS are options of sxir gen added to the options of the row (they
// may be empty). The first variant is the baseline; the ratios are relative to it. -control adds a
// copy of the baseline under the name "control": its ratios show the noise of the machine, and the
// deterministic metrics must equal the baseline's.
//
// The rows of the manifest are the language grammars, measured by parsing every input that parses
// without an error (one pass parses all of them), and csv-incremental and csv-stream, which generate
// the CSV parser with -document or -stream and measure an edit in the middle of a 50000-record file
// followed by a reparse, and the parse of the file as a stream of records. A variant whose generated
// Go parser is byte-identical to the baseline's for a row is listed as "noeffect" and is neither
// built nor measured there (the control always is). Time is the minimum over the rounds, which makes
// it robust against other load but not immune; run it under the benchmark lock of the machine.
//
//	ns_op, b_op, allocs_op   testing.Benchmark of one pass
//	retained                 live heap held by the results of a pass, after three collections
//	rss                      peak resident set size of the measuring process
//	gc_share                 share of the CPU time that went to garbage collection
//	src_bytes, bin_bytes     size of parser.go and of a stripped program calling ParseValue
//	compile_ms               wall time of building that program with the parser not cached
//	ts_raw, ts_gzip          size of parser.ts (with -ts), generated with the same ARGS
//
// The output has one "cell GRAMMAR VARIANT METRIC VALUE" line per value, then one
// "sample GRAMMAR VARIANT ROUND ns_op VALUE" line per measurement, the time of each round from which
// ns_op is the minimum (go run ./cmd/timed analyzes these), then a table of ratios to the baseline
// per metric.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/syntaxir/bench/internal/workloads"
)

type variantFlags []string

func (v *variantFlags) String() string     { return strings.Join(*v, " ") }
func (v *variantFlags) Set(s string) error { *v = append(*v, s); return nil }

func main() {
	var variants variantFlags
	flag.Var(&variants, "variant", "NAME=ARGS: a variant, with options of sxir gen in ARGS (repeatable; the first is the baseline)")
	root := flag.String("root", "", "the root of the SyntaxIR checkout to measure (required)")
	manifest := flag.String("manifest", "", "a directory with the checkout's exported workload manifest (default: export it into a temporary directory)")
	grammars := flag.String("grammars", "", "rows to measure, comma-separated (default: every row of the manifest)")
	rounds := flag.Int("rounds", 8, "rounds of measurements; the minimum time is kept")
	benchtime := flag.String("benchtime", "300ms", "the -benchtime of each measurement")
	control := flag.Bool("control", false, "add a copy of the baseline named control")
	ts := flag.Bool("ts", false, "also record the sizes of the generated TypeScript parsers")
	work := flag.String("work", "", "directory for generated files (default: a temporary one, removed afterwards)")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "profilematrix: -root names the SyntaxIR checkout to measure")
		os.Exit(2)
	}
	cfg := config{
		Root: *root, Rounds: *rounds, Benchtime: *benchtime, Control: *control, TS: *ts, Work: *work,
	}
	if *grammars != "" {
		cfg.Grammars = strings.Split(*grammars, ",")
	}
	for _, v := range variants {
		name, args, ok := strings.Cut(v, "=")
		if !ok || name == "" {
			fmt.Fprintf(os.Stderr, "profilematrix: -variant %q: want NAME=ARGS\n", v)
			os.Exit(2)
		}
		cfg.Variants = append(cfg.Variants, variant{Name: name, Args: strings.Fields(args)})
	}
	if err := runWithManifest(cfg, *manifest); err != nil {
		fmt.Fprintln(os.Stderr, "profilematrix:", err)
		os.Exit(1)
	}
}

// runWithManifest runs the matrix with the workload manifest of dir, or with the checkout's
// exported into a temporary directory, which it removes afterwards, when dir is empty.
func runWithManifest(cfg config, dir string) error {
	if dir == "" {
		tmp, err := os.MkdirTemp("", "sxir-workloads-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if cfg.Manifest, err = workloads.Export(cfg.Root, tmp); err != nil {
			return err
		}
	} else {
		var err error
		if cfg.Manifest, err = workloads.Load(dir); err != nil {
			return err
		}
	}
	return run(cfg, os.Stdout, os.Stderr)
}
