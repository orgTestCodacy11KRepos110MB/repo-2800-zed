package kernel

import (
	"github.com/brimdata/zed/compiler/ast/dag"
	"github.com/brimdata/zed/order"
	"github.com/brimdata/zed/pkg/field"
	"golang.org/x/exp/slices"
)

// NewKeyFilter creates a KeyFilter that contains a modified form of node where
// only predicates operating on key are kept. The underlying expression is
// rewritten in a manner so that results produced by the filter will always be
// a superset of the results produced by the parent filter (i.e., it will not
// filter values that would not be not also filtered by the original filter).
// Currently KeyFilter only recognizes simple key predicates against a literal
// value and using the comparators ==, >=, >, <, and <=; otherwise the predicate is
// ignored.
func NewKeyFilter(key field.Path, node dag.Expr) dag.Expr {
	e, _ := visitLeaves(node, func(cmp string, lhs *dag.This, rhs *dag.Literal) dag.Expr {
		if !key.Equal(lhs.Path) {
			return nil
		}
		//XXX do we use NewBinaryExpr consistently?
		return dag.NewBinaryExpr(cmp, lhs, rhs)
	})
	return e
}

func keyFilterToCroppedByExpr(k dag.Expr, o order.Which, prefix ...string) dag.Expr {
	return newKeyFilterExpr(k, o, prefix, true)
}

// SpanExpr creates an Expr that returns true if the KeyFilter has a value
// within a span of values. The span compared against must be a record with the
// fields "lower" and "upper", where "lower" is the inclusive lower bounder and
// "upper" is the exclusive upper bound.
func keyFilterToSpanExpr(k dag.Expr, o order.Which, prefix ...string) dag.Expr {
	return newKeyFilterExpr(k, o, prefix, false)
}

func newKeyFilterExpr(k dag.Expr, o order.Which, prefix []string, cropped bool) dag.Expr {
	lower := append(slices.Clone(prefix), "lower")
	upper := append(slices.Clone(prefix), "upper")
	if cropped {
		lower, upper = upper, lower
	}
	e, _ := visitLeaves(k, func(op string, this *dag.This, lit *dag.Literal) dag.Expr {
		switch op {
		case "==":
			if cropped {
				return &dag.Literal{Kind: "Literal", Value: "false"}
			}
			lhs := relativeToCompare("<=", &dag.This{Kind: "This", Path: lower}, lit, o)
			rhs := relativeToCompare(">=", &dag.This{Kind: "This", Path: upper}, lit, o)
			return dag.NewBinaryExpr("and", lhs, rhs)
		case "<", "<=":
			this.Path = lower
		case ">", ">=":
			this.Path = upper
		}
		return relativeToCompare(op, this, lit, o)
	})
	return e
}

func relativeToCompare(op string, lhs, rhs dag.Expr, o order.Which) *dag.BinaryExpr {
	nullsMax := &dag.Literal{Kind: "Literal", Value: "false"}
	if o == order.Asc {
		nullsMax.Value = "true"
	}
	lhs = &dag.Call{Kind: "Call", Name: "compare", Args: []dag.Expr{lhs, rhs, nullsMax}}
	return dag.NewBinaryExpr(op, lhs, &dag.Literal{Kind: "Literal", Value: "0"})
}

func visitLeaves(node dag.Expr, v func(cmp string, lhs *dag.This, rhs *dag.Literal) dag.Expr) (dag.Expr, bool) {
	e, ok := node.(*dag.BinaryExpr)
	if !ok {
		return nil, true
	}
	switch e.Op {
	case "and", "or":
		lhs, lok := visitLeaves(e.LHS, v)
		rhs, rok := visitLeaves(e.RHS, v)
		if !lok || !rok {
			return nil, false
		}
		if lhs == nil {
			if e.Op == "or" {
				return nil, false
			}
			return rhs, e.Op != "or"
		}
		if rhs == nil {
			if e.Op == "or" {
				return nil, false
			}
			return lhs, true
		}
		return dag.NewBinaryExpr(e.Op, lhs, rhs), true
	case "==", "<", "<=", ">", ">=":
		this, ok := e.LHS.(*dag.This)
		if !ok {
			return nil, true
		}
		rhs, ok := e.RHS.(*dag.Literal)
		if !ok {
			return nil, true
		}
		lhs := &dag.This{Kind: "This", Path: slices.Clone(this.Path)}
		return v(e.Op, lhs, rhs), true
	default:
		return nil, true
	}
}
