package exec

import (
	"fmt"

	"github.com/brimdata/zed"
	"github.com/brimdata/zed/lake/data"
	"github.com/brimdata/zed/order"
	"github.com/brimdata/zed/runtime/expr"
	"github.com/brimdata/zed/runtime/expr/extent"
	"github.com/brimdata/zed/zcode"
	"github.com/brimdata/zed/zson"
)

type Pruner struct {
	predicate expr.Evaluator // XXX Boolean?
	builder   zcode.Builder
	cmp       expr.CompareFn
	cols      []zed.Column
	ectx      expr.Context
	val       zed.Value
	zctx      *zed.Context
}

func NewPruner(p expr.Evaluator, o order.Which) *Pruner {
	return &Pruner{
		predicate: p,
		cmp:       expr.NewValueCompareFn(o == order.Asc),
		cols: []zed.Column{
			{Name: "lower"},
			{Name: "upper"},
		},
		ectx: expr.NewContext(),
		zctx: zed.NewContext(),
	}
}

// XXX seems like we should just marshal data.Object and change the upstream
// code to assume the data object instead of lower/upper struct
func (p *Pruner) Prune(o *data.Object) bool {
	span := extent.NewGeneric(o.First, o.Last, p.cmp)
	lower := span.First()
	upper := span.Last()
	p.cols[0].Type = lower.Type
	p.cols[1].Type = upper.Type
	if p.val.Type == nil || p.cols[0].Type != lower.Type || p.cols[1].Type != upper.Type {
		p.val.Type = p.zctx.MustLookupTypeRecord(p.cols)
	}
	p.builder.Reset()
	p.builder.Append(lower.Bytes)
	p.builder.Append(upper.Bytes)
	p.val.Bytes = p.builder.Bytes()
	val := p.predicate.Eval(p.ectx, &p.val)
	if val.Type != zed.TypeBool {
		panic(fmt.Errorf("result of SpanFilter not a boolean: %s", zson.FormatType(val.Type)))
	}
	return !zed.DecodeBool(val.Bytes)
}
