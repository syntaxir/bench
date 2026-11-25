// Package json is the JSON grammar of grammars/json.sxir written for participle
// (RFC 8259): a lexer for strings, numbers and punctuation, and one struct per value kind. Strings
// and numbers keep their text: a string its quotes and escapes, as the lexer matched it.
package json

import (
	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

// Value is one JSON value: exactly one field is set.
type Value struct {
	Object *Object `parser:"  @@"`
	Array  *Array  `parser:"| @@"`
	String *string `parser:"| @String"`
	Number *string `parser:"| @Number"`
	Bool   *string `parser:"| @(\"true\" | \"false\")"`
	Null   *string `parser:"| @\"null\""`
}

// Object is a JSON object, its members in order.
type Object struct {
	Members []*Member `parser:"\"{\" ( @@ ( \",\" @@ )* )? \"}\""`
}

// Member is a member of an object.
type Member struct {
	Key   string `parser:"@String \":\""`
	Value *Value `parser:"@@"`
}

// Array is a JSON array.
type Array struct {
	Elements []*Value `parser:"\"[\" ( @@ ( \",\" @@ )* )? \"]\""`
}

var parser = participle.MustBuild[Value](
	participle.Lexer(lexer.MustSimple([]lexer.SimpleRule{
		{Name: "String", Pattern: `"([^"\\\x00-\x1f]|\\(["\\/bfnrt]|u[0-9a-fA-F]{4}))*"`},
		{Name: "Number", Pattern: `-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?`},
		{Name: "Keyword", Pattern: `true|false|null`},
		{Name: "Punct", Pattern: `[{}\[\],:]`},
		{Name: "Whitespace", Pattern: `[ \t\r\n]+`},
	})),
	participle.Elide("Whitespace"),
)

// Parse parses a JSON text.
func Parse(s string) (*Value, error) { return parser.ParseString("", s) }
