package wedge

import (
	"fmt"
	"strings"

	pcalc "github.com/syntaxir/bench/wedge/pigeon/calc"
	pfilter "github.com/syntaxir/bench/wedge/pigeon/filter"
	pjson "github.com/syntaxir/bench/wedge/pigeon/json"
)

func pigeonCalc(v any) string {
	switch v := v.(type) {
	case pcalc.Number:
		return string(v)
	case *pcalc.Binary:
		return "(" + v.Op + " " + pigeonCalc(v.Left) + " " + pigeonCalc(v.Right) + ")"
	case *pcalc.Unary:
		return "(" + v.Op + " " + pigeonCalc(v.X) + ")"
	}
	return fmt.Sprintf("?%T", v)
}

func pigeonJSON(v any) string {
	switch v := v.(type) {
	case *pjson.Object:
		parts := make([]string, len(v.Members))
		for i, m := range v.Members {
			parts[i] = `"` + string(m.Key) + `":` + pigeonJSON(m.Value)
		}
		return "{" + strings.Join(parts, ",") + "}"
	case *pjson.Array:
		parts := make([]string, len(v.Elements))
		for i, e := range v.Elements {
			parts[i] = pigeonJSON(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case pjson.String:
		return `"` + string(v) + `"`
	case pjson.Number:
		return string(v)
	case pjson.Bool:
		return string(v)
	case pjson.Null:
		return string(v)
	}
	return fmt.Sprintf("?%T", v)
}

func pigeonFilter(v any) string {
	switch v := v.(type) {
	case *pfilter.Compare:
		return "(" + v.Op + " " + v.Field + " " + v.Value + ")"
	case *pfilter.In:
		return "(in " + v.Field + " " + strings.Join(v.Values, " ") + ")"
	case *pfilter.Logic:
		return "(" + v.Op + " " + pigeonFilter(v.Left) + " " + pigeonFilter(v.Right) + ")"
	case *pfilter.Not:
		return "(" + v.Op + " " + pigeonFilter(v.X) + ")"
	}
	return fmt.Sprintf("?%T", v)
}
