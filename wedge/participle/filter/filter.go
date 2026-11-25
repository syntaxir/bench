// Package filter is the filter language of bench/wedge/grammars/filter.sxir written for participle:
// comparisons and list membership of dotted fields, combined with !, && and ||, one struct per level
// of precedence.
package filter

import (
	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

// Or is a disjunction, left to right.
type Or struct {
	Left *And   `parser:"@@"`
	Rest []*And `parser:"( \"||\" @@ )*"`
}

// And is a conjunction, left to right.
type And struct {
	Left *Prefix   `parser:"@@"`
	Rest []*Prefix `parser:"( \"&&\" @@ )*"`
}

// Prefix is a negation or an operand.
type Prefix struct {
	Not     *Prefix  `parser:"  \"!\" @@"`
	Operand *Operand `parser:"| @@"`
}

// Operand is a comparison or a parenthesized filter.
type Operand struct {
	Comparison *Comparison `parser:"  @@"`
	Sub        *Or         `parser:"| \"(\" @@ \")\""`
}

// Comparison compares a field with a literal, or tests it for membership in a list.
type Comparison struct {
	Field  string   `parser:"@Field"`
	Op     string   `parser:"( @Op"`
	Value  string   `parser:"  @(String | Number | \"true\" | \"false\" | \"null\")"`
	Values []string `parser:"| \"in\" \"[\" @(String | Number | \"true\" | \"false\" | \"null\") ( \",\" @(String | Number | \"true\" | \"false\" | \"null\") )* \"]\" )"`
}

var parser = participle.MustBuild[Or](
	participle.Lexer(lexer.MustSimple([]lexer.SimpleRule{
		{Name: "String", Pattern: `"([^"\\]|\\.)*"`},
		{Name: "Number", Pattern: `-?[0-9]+(\.[0-9]+)?`},
		{Name: "Field", Pattern: `[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*`},
		{Name: "Op", Pattern: `==|!=|<=|>=|<|>|~`},
		{Name: "Logic", Pattern: `&&|\|\|`},
		{Name: "Punct", Pattern: `[!()\[\],]`},
		{Name: "Whitespace", Pattern: `[ \t\r\n]+`},
	})),
	participle.Elide("Whitespace"),
	participle.UseLookahead(2),
)

// Parse parses a filter.
func Parse(s string) (*Or, error) { return parser.ParseString("", s) }
