package wedge

import (
	"testing"
)

// corpus holds hand-written inputs of each grammar: valid ones that reach every construct, and
// invalid ones that every tool must reject.
var corpus = map[string]struct{ valid, invalid []string }{
	"calc": {
		valid: []string{
			"1", "1.5", " 1 + 2 ", "1 - 2 - 3", "2 * 3 + 4", "2 + 3 * 4", "8 / 4 / 2", "7 % 3",
			"-1", "+1", "- -1", "-2 * 3", "2 * -3", "2 ^ 3", "2 ^ 3 ^ 2", "-2 ^ 2", "2 ^ -3",
			"2 ^ -3 ^ 2", "2 ^ -3 * 4", "(1 + 2) * 3", "((1))", "1+2*3-4/5%6", "\t1\n+\r\n2",
		},
		invalid: []string{"", "+", "1 +", "(1", "1)", "1 2", "1..2", ".5", "2 ^", "2 ^ *3", "a", "1 + * 2"},
	},
	"filter": {
		valid: []string{
			`a == 1`, `a.b.c != "x"`, `a < -1.5`, `a <= 2`, `a > 3`, `a >= 4`, `name ~ "jo.*"`,
			`a == true`, `a == false`, `a == null`, `role in ["admin"]`, `role in ["a", "b", 3, null]`,
			`a == 1 && b == 2`, `a == 1 || b == 2 && c == 3`, `(a == 1 || b == 2) && c == 3`,
			`!a == 1`, `!!a == 1`, `!(a == 1 && b == 2)`, `s == "esc \"q\" \\"`, ` ( a == 1 ) `,
		},
		invalid: []string{
			"", "a", "a ==", "== 1", "a == 1 &&", "a = 1", "a in []", "a in [1,]", "(a == 1",
			"a == 1)", "a == 1 & b == 2", "a == x", "1 == 1", `a == "open`,
		},
	},
	"json": {
		valid: []string{
			`null`, `true`, `false`, `0`, `-0.5e+3`, `"s"`, `"esc \" \\ \/ \u00e9"`, `[]`, `{}`,
			` [1, "a", null] `, `{"a": {"b": [true, false]}, "c": -1}`, `[[[]]]`, "{\n\t\"k\" : 1\r\n}",
		},
		invalid: []string{
			"", "nul", "[1,]", "{,}", `{"a" 1}`, `{1: 2}`, "01", "1.", ".5", "+1", `"tab	in"`,
			`"\x"`, "[1 2]", "{} {}", "tru",
		},
	},
}

// TestEquivalence checks, before any timing, that every tool parses the same language into the same
// values: on the hand-written corpus and on the generated short and long inputs, every parser accepts
// the valid inputs and rejects the invalid ones, and the canonical forms of the values agree with
// SyntaxIR's.
func TestEquivalence(t *testing.T) {
	for _, g := range Grammars {
		c := corpus[g.Name]
		valid := append([]string(nil), c.valid...)
		for _, s := range Sizes {
			in := g.Input(s.Bytes)
			if in == "" {
				t.Fatalf("%s has no %s input", g.Name, s.Name)
			}
			valid = append(valid, in)
		}
		for _, in := range valid {
			want, err := g.Parsers[0].Canon(in)
			if err != nil {
				t.Errorf("%s: %s rejects %.60q: %v", g.Name, g.Parsers[0].Tool, in, err)
				continue
			}
			for _, p := range g.Parsers[1:] {
				if p.Canon == nil {
					if err := p.Parse(in); err != nil {
						t.Errorf("%s: %s rejects %.60q: %v", g.Name, p.Tool, in, err)
					}
					continue
				}
				got, err := p.Canon(in)
				if err != nil {
					t.Errorf("%s: %s rejects %.60q: %v", g.Name, p.Tool, in, err)
				} else if got != want {
					t.Errorf("%s: %s on %.60q:\n got %.200s\nwant %.200s", g.Name, p.Tool, in, got, want)
				}
			}
		}
		for _, in := range c.invalid {
			for _, p := range g.Parsers {
				if err := p.Parse(in); err == nil {
					t.Errorf("%s: %s accepts %q", g.Name, p.Tool, in)
				}
			}
		}
	}
}

// TestInputs checks the input sizes: the short input is at most 1 KB and the long one at most 128 KB,
// each more than half of its size.
func TestInputs(t *testing.T) {
	for _, g := range Grammars {
		for _, s := range Sizes {
			if n := len(g.Input(s.Bytes)); n > s.Bytes || n <= s.Bytes/2 {
				t.Errorf("%s %s: %d bytes", g.Name, s.Name, n)
			}
		}
	}
}

// BenchmarkWedge parses the short and the long input of every grammar with every tool, into each
// tool's typed values. Run TestEquivalence first: a time is comparable only when the values agree.
func BenchmarkWedge(b *testing.B) {
	for _, g := range Grammars {
		for _, s := range Sizes {
			in := g.Input(s.Bytes)
			for _, p := range g.Parsers {
				b.Run(g.Name+"/"+s.Name+"/"+p.Tool, func(b *testing.B) {
					b.SetBytes(int64(len(in)))
					b.ReportAllocs()
					for b.Loop() {
						if err := p.Parse(in); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}
