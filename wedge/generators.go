package wedge

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// The generators of the calc and json inputs are those of SyntaxIR's benchmark inputs
// (internal/bench/inputs), so that the wedge parses the documents that SyntaxIR's own benchmarks
// parse. They are copied because the wedge uses SyntaxIR's public packages only.

// Inputs are generated from a fixed random seed so that results are reproducible.
func newRand() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

var words = []string{"alpha", "beta", "gamma", "delta", "日本語", "naïve", "x", "value", "item", "東京"}

// JSONInput returns about size bytes of JSON: an array of objects.
func JSONInput(size int) string {
	r := newRand()
	var b strings.Builder
	b.WriteString("[\n")
	for i := 0; b.Len() < size; i++ {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, `  {"id": %d, "name": "%s \"%s\"", "score": %d.%02d, "active": %t, "tags": [`,
			i, words[r.IntN(len(words))], words[r.IntN(len(words))], r.IntN(1000), r.IntN(100), r.IntN(2) == 0)
		for k := r.IntN(4); k > 0; k-- {
			fmt.Fprintf(&b, `"%s"`, words[r.IntN(len(words))])
			if k > 1 {
				b.WriteString(", ")
			}
		}
		fmt.Fprintf(&b, `], "parent": null, "pos": {"x": %d, "y": -%d.5e3}}`, r.IntN(100), r.IntN(100))
	}
	b.WriteString("\n]\n")
	return b.String()
}

// ArithInput returns an arithmetic expression with action expressions action expressions, nested parentheses and unary minus.
// It uses only operators that also form a valid Go expression.
func ArithInput(actionExprs int) string {
	r := newRand()
	ops := []string{"+", "-", "*", "/", "%"}
	var b strings.Builder
	depth := 0
	for i := 0; i < actionExprs; i++ {
		if i > 0 {
			b.WriteString(" " + ops[r.IntN(len(ops))] + " ")
		}
		for r.IntN(4) == 0 && depth < 40 {
			b.WriteString("(")
			depth++
		}
		if r.IntN(8) == 0 {
			b.WriteString("-")
		}
		fmt.Fprintf(&b, "%d", r.IntN(1000))
		for depth > 0 && r.IntN(3) == 0 {
			b.WriteString(")")
			depth--
		}
	}
	b.WriteString(strings.Repeat(")", depth))
	return b.String()
}
