package wedge

import (
	"strings"

	qcalc "github.com/syntaxir/bench/wedge/participle/calc"
	qfilter "github.com/syntaxir/bench/wedge/participle/filter"
	qjson "github.com/syntaxir/bench/wedge/participle/json"
)

// The canonical forms of participle's values fold the operands of each level as the grammar
// associates them: left to right, and ^ right to left.

func participleCalc(e *qcalc.Expr) string {
	s := participleTerm(e.Left)
	for _, r := range e.Rest {
		s = "(" + r.Op + " " + s + " " + participleTerm(r.Term) + ")"
	}
	return s
}

func participleTerm(t *qcalc.Term) string {
	s := participlePrefix(t.Left)
	for _, r := range t.Rest {
		s = "(" + r.Op + " " + s + " " + participlePrefix(r.X) + ")"
	}
	return s
}

func participlePrefix(p *qcalc.Prefix) string {
	if p.Pow != nil {
		return participlePow(p.Pow)
	}
	return "(" + p.Op + " " + participlePrefix(p.X) + ")"
}

func participlePow(p *qcalc.Pow) string {
	base := participlePrimary(p.Base)
	if p.Exp == nil {
		return base
	}
	return "(^ " + base + " " + participlePrefix(p.Exp) + ")"
}

func participlePrimary(p *qcalc.Primary) string {
	if p.Number != nil {
		return *p.Number
	}
	return participleCalc(p.Sub)
}

func participleJSON(v *qjson.Value) string {
	switch {
	case v.Object != nil:
		parts := make([]string, len(v.Object.Members))
		for i, m := range v.Object.Members {
			parts[i] = m.Key + ":" + participleJSON(m.Value)
		}
		return "{" + strings.Join(parts, ",") + "}"
	case v.Array != nil:
		parts := make([]string, len(v.Array.Elements))
		for i, e := range v.Array.Elements {
			parts[i] = participleJSON(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case v.String != nil:
		return *v.String
	case v.Number != nil:
		return *v.Number
	case v.Bool != nil:
		return *v.Bool
	case v.Null != nil:
		return *v.Null
	}
	return "?"
}

func participleFilter(o *qfilter.Or) string {
	s := participleAnd(o.Left)
	for _, r := range o.Rest {
		s = "(|| " + s + " " + participleAnd(r) + ")"
	}
	return s
}

func participleAnd(a *qfilter.And) string {
	s := participleNot(a.Left)
	for _, r := range a.Rest {
		s = "(&& " + s + " " + participleNot(r) + ")"
	}
	return s
}

func participleNot(p *qfilter.Prefix) string {
	if p.Not != nil {
		return "(! " + participleNot(p.Not) + ")"
	}
	if p.Operand.Sub != nil {
		return participleFilter(p.Operand.Sub)
	}
	c := p.Operand.Comparison
	if c.Values != nil {
		return "(in " + c.Field + " " + strings.Join(c.Values, " ") + ")"
	}
	return "(" + c.Op + " " + c.Field + " " + c.Value + ")"
}
