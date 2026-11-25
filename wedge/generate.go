// Package wedge compares SyntaxIR with other Go parser generators on grammars of the size of a
// domain-specific language: a calculator, a filter language and JSON, each written for every tool
// with the same language and the same output class, typed values. See README.md.
//
// The module is separate from SyntaxIR's, so the tools it compares are never dependencies of
// syntaxir.dev/sxir. It requires syntaxir.dev/sxir, whose public runtime the generated SyntaxIR
// parsers import, from a SyntaxIR checkout that a go.work names (see README.md). Regenerate the
// parsers with go generate in this directory: the SyntaxIR parsers come from that checkout's sxir.
package wedge

// SyntaxIR: the default form (limit controls on) and the form without them (-no-limits), whose
// difference is the overhead of the limits.
//go:generate go run syntaxir.dev/sxir/cmd/sxir gen -g grammars/calc.sxir -module github.com/syntaxir/bench/wedge/sxir/calc -lib import -types -o sxir/calc
//go:generate go run syntaxir.dev/sxir/cmd/sxir gen -g grammars/calc.sxir -module github.com/syntaxir/bench/wedge/sxir/calcplain -lib import -types -no-limits -o sxir/calcplain
//go:generate go run syntaxir.dev/sxir/cmd/sxir gen -g grammars/filter.sxir -module github.com/syntaxir/bench/wedge/sxir/filter -lib import -types -o sxir/filter
//go:generate go run syntaxir.dev/sxir/cmd/sxir gen -g grammars/filter.sxir -module github.com/syntaxir/bench/wedge/sxir/filterplain -lib import -types -no-limits -o sxir/filterplain
//go:generate go run syntaxir.dev/sxir/cmd/sxir gen -g grammars/json.sxir -module github.com/syntaxir/bench/wedge/sxir/json -lib import -types -o sxir/json
//go:generate go run syntaxir.dev/sxir/cmd/sxir gen -g grammars/json.sxir -module github.com/syntaxir/bench/wedge/sxir/jsonplain -lib import -types -no-limits -o sxir/jsonplain

// pigeon, at the version that go.mod pins, with the options that its documentation gives for the
// fastest parser: no debugging or memoization code, and ASCII-only character tests.
//go:generate go tool pigeon -optimize-parser -optimize-basic-latin -o pigeon/calc/parser.go pigeon/calc/calc.peg
//go:generate go tool pigeon -optimize-parser -optimize-basic-latin -o pigeon/filter/parser.go pigeon/filter/filter.peg
//go:generate go tool pigeon -optimize-parser -optimize-basic-latin -o pigeon/json/parser.go pigeon/json/json.peg

// participle needs no generation: its grammars are the struct tags in participle/.

// TODO(antlr): the ANTLR Go runtime column. Generating its parsers needs Java and the ANTLR tool jar,
// which are not installed here; add the grammars (antlr/*.g4), the pinned ANTLR version in the
// generated files' header and the generation lines once they are.
