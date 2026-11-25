package wedge

import (
	"fmt"
	"strings"

	scalc "github.com/syntaxir/bench/wedge/sxir/calc/types"
	sfilter "github.com/syntaxir/bench/wedge/sxir/filter/types"
	sjson "github.com/syntaxir/bench/wedge/sxir/json/types"
)

func sxirCalc(v scalc.Expr) string {
	switch v := v.(type) {
	case *scalc.Number:
		return v.Value
	case *scalc.Binary:
		return "(" + v.Op.Value + " " + sxirCalc(v.Left) + " " + sxirCalc(v.Right) + ")"
	case *scalc.Unary:
		return "(" + v.Op.Value + " " + sxirCalc(v.X) + ")"
	}
	return fmt.Sprintf("?%T", v)
}

func sxirJSON(v sjson.Value) string {
	switch v := v.(type) {
	case *sjson.Object:
		parts := make([]string, len(v.Members))
		for i, m := range v.Members {
			parts[i] = `"` + m.Key.Value + `":` + sxirJSON(m.Value)
		}
		return "{" + strings.Join(parts, ",") + "}"
	case *sjson.Array:
		parts := make([]string, len(v.Elements))
		for i, e := range v.Elements {
			parts[i] = sxirJSON(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case *sjson.String:
		return `"` + v.Value + `"`
	case *sjson.Number:
		return v.Value
	case *sjson.Bool:
		return v.Value
	case *sjson.Null:
		return v.Value
	}
	return fmt.Sprintf("?%T", v)
}

func sxirFilter(v sfilter.Expr) string {
	switch v := v.(type) {
	case *sfilter.Compare:
		return "(" + v.Op.Value + " " + v.Field.Value + " " + v.Value.Value + ")"
	case *sfilter.In:
		vs := make([]string, len(v.Values))
		for i, x := range v.Values {
			vs[i] = x.Value
		}
		return "(in " + v.Field.Value + " " + strings.Join(vs, " ") + ")"
	case *sfilter.Logic:
		return "(" + v.Op.Value + " " + sxirFilter(v.Left) + " " + sxirFilter(v.Right) + ")"
	case *sfilter.Not:
		return "(" + v.Op.Value + " " + sxirFilter(v.X) + ")"
	}
	return fmt.Sprintf("?%T", v)
}
