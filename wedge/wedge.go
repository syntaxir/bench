package wedge

import (
	qcalc "github.com/syntaxir/bench/wedge/participle/calc"
	qfilter "github.com/syntaxir/bench/wedge/participle/filter"
	qjson "github.com/syntaxir/bench/wedge/participle/json"
	pcalc "github.com/syntaxir/bench/wedge/pigeon/calc"
	pfilter "github.com/syntaxir/bench/wedge/pigeon/filter"
	pjson "github.com/syntaxir/bench/wedge/pigeon/json"
	scalc "github.com/syntaxir/bench/wedge/sxir/calc"
	scalcplain "github.com/syntaxir/bench/wedge/sxir/calcplain"
	sfilter "github.com/syntaxir/bench/wedge/sxir/filter"
	sfilterplain "github.com/syntaxir/bench/wedge/sxir/filterplain"
	sjson "github.com/syntaxir/bench/wedge/sxir/json"
	sjsonplain "github.com/syntaxir/bench/wedge/sxir/jsonplain"
)

// Parser is one tool's parser for one grammar. All of them produce the same output class, typed
// values: the tool's own values of the grammar, built during the parse.
type Parser struct {
	Tool string
	// Parse parses an input into the tool's values and discards them: what the benchmarks time.
	Parse func(string) error
	// Canon parses an input and prints its values in a form shared by the tools, which the
	// equivalence test compares.
	Canon func(string) (string, error)
}

// Grammar is a grammar of the wedge with its parsers, the SyntaxIR parser first.
type Grammar struct {
	Name    string
	Parsers []Parser
	gen     func(int) string
}

func discard[T any](_ T, err error) error { return err }

func canon[T any](f func(T) string) func(T, error) (string, error) {
	return func(v T, err error) (string, error) {
		if err != nil {
			return "", err
		}
		return f(v), nil
	}
}

// Grammars are the grammars of the wedge. sxir is the generated parser in its default form, with
// limit controls; sxir_nolimits is the same grammar generated with -no-limits.
var Grammars = []*Grammar{
	{Name: "calc", gen: generators["calc"], Parsers: []Parser{
		{"sxir", func(s string) error { return discard(scalc.ParseValue(s)) }, func(s string) (string, error) { return canon(sxirCalc)(scalc.ParseValue(s)) }},
		{"sxir_nolimits", func(s string) error { return discard(scalcplain.ParseValue(s)) }, nil},
		{"pigeon", func(s string) error { return discard(pcalc.Parse("", []byte(s))) }, func(s string) (string, error) { return canon(pigeonCalc)(pcalc.Parse("", []byte(s))) }},
		{"participle", func(s string) error { return discard(qcalc.Parse(s)) }, func(s string) (string, error) { return canon(participleCalc)(qcalc.Parse(s)) }},
	}},
	{Name: "filter", gen: generators["filter"], Parsers: []Parser{
		{"sxir", func(s string) error { return discard(sfilter.ParseValue(s)) }, func(s string) (string, error) { return canon(sxirFilter)(sfilter.ParseValue(s)) }},
		{"sxir_nolimits", func(s string) error { return discard(sfilterplain.ParseValue(s)) }, nil},
		{"pigeon", func(s string) error { return discard(pfilter.Parse("", []byte(s))) }, func(s string) (string, error) { return canon(pigeonFilter)(pfilter.Parse("", []byte(s))) }},
		{"participle", func(s string) error { return discard(qfilter.Parse(s)) }, func(s string) (string, error) { return canon(participleFilter)(qfilter.Parse(s)) }},
	}},
	{Name: "json", gen: generators["json"], Parsers: []Parser{
		{"sxir", func(s string) error { return discard(sjson.ParseValue(s)) }, func(s string) (string, error) { return canon(sxirJSON)(sjson.ParseValue(s)) }},
		{"sxir_nolimits", func(s string) error { return discard(sjsonplain.ParseValue(s)) }, nil},
		{"pigeon", func(s string) error { return discard(pjson.Parse("", []byte(s))) }, func(s string) (string, error) { return canon(pigeonJSON)(pjson.Parse("", []byte(s))) }},
		{"participle", func(s string) error { return discard(qjson.Parse(s)) }, func(s string) (string, error) { return canon(participleJSON)(qjson.Parse(s)) }},
	}},
}
