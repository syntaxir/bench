// Package calc is the calculator of grammars/calc.sxir written for participle: + and -
// below * / and %, prefix - and + below a right-associative ^, with parentheses. Participle builds
// the values of the grammar's structs; one level of precedence is one struct.
package calc

import (
	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

// Expr is a sum: a Term and the terms added to or subtracted from it, left to right.
type Expr struct {
	Left *Term     `parser:"@@"`
	Rest []*OpTerm `parser:"@@*"`
}

// OpTerm is an operator of a sum and its right operand.
type OpTerm struct {
	Op   string `parser:"@(\"+\" | \"-\")"`
	Term *Term  `parser:"@@"`
}

// Term is a product: a Prefix and the operands it is multiplied with, left to right.
type Term struct {
	Left *Prefix     `parser:"@@"`
	Rest []*OpPrefix `parser:"@@*"`
}

// OpPrefix is an operator of a product and its right operand.
type OpPrefix struct {
	Op string  `parser:"@(\"*\" | \"/\" | \"%\")"`
	X  *Prefix `parser:"@@"`
}

// Prefix is a prefix operation, or a power.
type Prefix struct {
	Op  string  `parser:"( @(\"-\" | \"+\")"`
	X   *Prefix `parser:"  @@ )"`
	Pow *Pow    `parser:"| @@"`
}

// Pow is a base raised to a power, right-associative; the exponent may be a prefix operation.
type Pow struct {
	Base *Primary `parser:"@@"`
	Exp  *Prefix  `parser:"( \"^\" @@ )?"`
}

// Primary is a number or a parenthesized expression.
type Primary struct {
	Number *string `parser:"  @Number"`
	Sub    *Expr   `parser:"| \"(\" @@ \")\""`
}

var parser = participle.MustBuild[Expr](
	participle.Lexer(lexer.MustSimple([]lexer.SimpleRule{
		{Name: "Number", Pattern: `[0-9]+(\.[0-9]+)?`},
		{Name: "Punct", Pattern: `[-+*/%^()]`},
		{Name: "Whitespace", Pattern: `[ \t\r\n]+`},
	})),
	participle.Elide("Whitespace"),
)

// Parse parses an expression.
func Parse(s string) (*Expr, error) { return parser.ParseString("", s) }
