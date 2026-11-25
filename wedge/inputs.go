package wedge

import (
	"fmt"
	"math/rand/v2"
	"strings"
)

// FilterInput returns a filter of clauses comparisons and membership tests, combined with && and ||,
// some negated and some parenthesized, from a fixed seed.
func FilterInput(clauses int) string {
	r := rand.New(rand.NewPCG(3, 4))
	fields := []string{"status", "age", "user.role", "user.name", "region", "score", "tags.primary", "deleted"}
	ops := []string{"==", "!=", "<", "<=", ">", ">=", "~"}
	literal := func() string {
		switch r.IntN(5) {
		case 0:
			return fmt.Sprintf("%d", r.IntN(1000))
		case 1:
			return fmt.Sprintf("-%d.%d", r.IntN(100), r.IntN(10))
		case 2:
			return []string{"true", "false", "null"}[r.IntN(3)]
		case 3:
			return `"say \"hi\""`
		}
		return fmt.Sprintf(`"v%d"`, r.IntN(100))
	}
	var b strings.Builder
	open := 0
	for i := 0; i < clauses; i++ {
		if i > 0 {
			b.WriteString([]string{" && ", " || "}[r.IntN(2)])
		}
		if r.IntN(6) == 0 {
			b.WriteString("!")
		}
		if r.IntN(5) == 0 && open < 8 {
			b.WriteString("(")
			open++
		}
		f := fields[r.IntN(len(fields))]
		if r.IntN(4) == 0 {
			vs := make([]string, 1+r.IntN(4))
			for k := range vs {
				vs[k] = literal()
			}
			fmt.Fprintf(&b, "%s in [%s]", f, strings.Join(vs, ", "))
		} else {
			fmt.Fprintf(&b, "%s %s %s", f, ops[r.IntN(len(ops))], literal())
		}
		for open > 0 && r.IntN(3) == 0 {
			b.WriteString(")")
			open--
		}
	}
	b.WriteString(strings.Repeat(")", open))
	return b.String()
}

// Size is an input size of the wedge.
type Size struct {
	Name  string
	Bytes int
}

// Sizes are the input sizes: a short input of at most 1 KB, the latency case, and a long one of about
// 128 KB, the throughput case.
var Sizes = []Size{{"short", 1 << 10}, {"long", 128 << 10}}

// Input returns the largest input of g's generator that is at most size bytes long.
func (g *Grammar) Input(size int) string {
	lo, hi := 1, size // every generator makes at least n bytes
	if len(g.gen(lo)) > size {
		return ""
	}
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if len(g.gen(mid)) <= size {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return g.gen(lo)
}

// generators make the inputs of each grammar by a size parameter that the input grows with.
var generators = map[string]func(int) string{
	"calc":   ArithInput,
	"json":   JSONInput,
	"filter": FilterInput,
}
