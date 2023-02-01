package optimizer

import (
	"context"
	"fmt"

	"github.com/brimdata/zed/compiler/ast/dag"
	"github.com/brimdata/zed/compiler/data"
	"github.com/brimdata/zed/compiler/kernel"
	"github.com/brimdata/zed/order"
	"github.com/brimdata/zed/pkg/field"
)

type Optimizer struct {
	ctx     context.Context
	entry   *dag.Sequential
	source  *data.Source
	layouts map[dag.Source]order.Layout
}

func New(ctx context.Context, entry *dag.Sequential, source *data.Source) *Optimizer {
	return &Optimizer{
		ctx:     ctx,
		entry:   entry,
		source:  source,
		layouts: make(map[dag.Source]order.Layout),
	}
}

func (o *Optimizer) Entry() *dag.Sequential {
	return o.entry
}

// OptimizeScan transforms the DAG by attempting to lift stateless operators
// from the downstream sequence into the trunk of each data source in the From
// operator at the entry point of the DAG.  Once these paths are lifted,
// it also attempts to move any candidate filtering operations into the
// source's pushdown predicate.  This should be called before ParallelizeScan().
// TBD: we need to do pushdown for search/cut to optimize columnar extraction.
func (o *Optimizer) OptimizeScan() error {
	if _, ok := o.entry.Ops[0].(*dag.From); !ok {
		return nil
	}
	seq := o.entry
	o.propagateScanOrder(seq, order.Nil)
	from := seq.Ops[0].(*dag.From)
	chain := seq.Ops[1:]
	layout, err := o.layoutOfFrom(from)
	if err != nil {
		return err
	}
	len, _, err := o.splittablePath(chain, layout)
	if err != nil {
		return err
	}
	if len > 0 {
		chain = chain[:len]
		for k := range from.Trunks {
			trunk := &from.Trunks[k]
			liftInto(trunk, copyOps(chain))
			pushDown(trunk)
		}
		seq.Delete(1, len)
	}
	// Add pool range query to pushdown (if available).
	// Also, check to see if we can add a range pruner when the pool-key is used
	// in a normal filtering operation.
	for k := range from.Trunks {
		trunk := &from.Trunks[k]
		if layout, ok := o.layouts[trunk.Source]; ok {
			addRangeToPushdown(trunk, layout.Primary())
			if pushdown, ok := trunk.Pushdown.(*dag.Filter); ok {
				trunk.KeyPruner = newRangePruner(pushdown.Expr, layout.Primary(), layout.Order)
			}
		}
	}
	return nil
}

func addRangeToPushdown(trunk *dag.Trunk, key field.Path) {
	rangeExpr := convertRangeScan(trunk.Source, key)
	if rangeExpr == nil {
		return
	}
	if trunk.Pushdown == nil {
		trunk.Pushdown = &dag.Filter{Kind: "Filter", Expr: rangeExpr}
		return
	}
	//XXX should pushdown just be a dag.Expr instead of dag.Op?  or maybe even dag.Boolean?
	filter := trunk.Pushdown.(*dag.Filter)
	filter.Expr = dag.NewBinaryExpr("and", rangeExpr, filter.Expr)
}

func convertRangeScan(source dag.Source, key field.Path) dag.Expr {
	p, ok := source.(*dag.Pool)
	if !ok {
		return nil
	}
	this := &dag.This{Kind: "This", Path: key}
	var lower, upper dag.Expr
	if p.ScanLower != nil {
		lower = dag.NewBinaryExpr(">=", this, p.ScanLower)
	}
	if p.ScanUpper != nil {
		upper = dag.NewBinaryExpr("<=", this, p.ScanUpper)
	}
	if lower == nil {
		return upper
	}
	if upper == nil {
		return lower
	}
	return dag.NewBinaryExpr("and", lower, upper)
}

// propagateScanOrder analyzes each trunk of the From input node and
// attempts to push the scan order of the data source into the first
// downstream aggregation.  (We could continue the analysis past that
// point but don't bother yet because we do not yet support any optimization
// past the first aggregation.)  If there are multiple trunks, we only
// propagate the scan order if its the same at egress of all of the trunks.
func (o *Optimizer) propagateScanOrder(op dag.Op, parent order.Layout) (order.Layout, error) {
	switch op := op.(type) {
	case *dag.From:
		var egress order.Layout
		for k := range op.Trunks {
			trunk := &op.Trunks[k]
			l, err := o.layoutOfSource(trunk.Source, parent)
			if err != nil {
				return order.Nil, err
			}
			l, err = o.propagateScanOrder(trunk.Seq, l)
			if err != nil {
				return order.Nil, err
			}
			if k == 0 {
				egress = l
			} else if !egress.Equal(l) {
				egress = order.Nil
			}
		}
		return egress, nil
	case *dag.Summarize:
		//XXX handle only primary key for now
		key := parent.Primary()
		for _, k := range op.Keys {
			if groupByKey := fieldOf(k.LHS); groupByKey.Equal(key) {
				rhsExpr := k.RHS
				rhs := fieldOf(rhsExpr)
				if rhs.Equal(key) || orderPreservingCall(rhsExpr, groupByKey) {
					op.InputSortDir = orderAsDirection(parent.Order)
					// Currently, the groupby operator will sort its
					// output according to the primary key, but we
					// should relax this and do an analysis here as
					// to whether the sort is necessary for the
					// downstream consumer.
					return parent, nil
				}
			}
		}
		// We'll live this as unknown for now even though the groupby
		// and not try to optimize downstream of the first groupby
		// unless there is an excplicit sort encountered.
		return order.Nil, nil
	case *dag.Sequential:
		if op == nil {
			return parent, nil
		}
		for _, op := range op.Ops {
			var err error
			parent, err = o.propagateScanOrder(op, parent)
			if err != nil {
				return order.Nil, err
			}
		}
		return parent, nil
	case *dag.Parallel:
		var egress order.Layout
		for k, op := range op.Ops {
			out, err := o.propagateScanOrder(op, parent)
			if err != nil {
				return order.Nil, err
			}
			if k == 0 {
				egress = out
			} else if !egress.Equal(out) {
				egress = order.Nil
			}
		}
		return egress, nil
	case *dag.Merge:
		layout := order.NewLayout(op.Order, nil)
		if this, ok := op.Expr.(*dag.This); ok {
			layout.Keys = field.List{this.Path}
		}
		if !layout.Equal(parent) {
			layout = order.Nil
		}
		return layout, nil
	default:
		return o.analyzeOp(op, parent)
	}
}

func (o *Optimizer) layoutOfSource(s dag.Source, parent order.Layout) (order.Layout, error) {
	layout, ok := o.layouts[s]
	if !ok {
		var err error
		layout, err = o.getLayout(s, parent)
		if err != nil {
			return order.Nil, err
		}
		o.layouts[s] = layout
	}
	if pool, ok := s.(*dag.Pool); ok {
		scanOrder, _ := order.ParseDirection(pool.ScanOrder)
		// If the requested scan order is the same order as the pool,
		// then we can use it.  Otherwise, the scan is going against
		// the grain and we don't yet have the logic to reverse the
		// scan of each object, though this should be relatively
		// easy to add.  See issue #2665.
		if scanOrder != order.Unknown && !scanOrder.HasOrder(layout.Order) {
			layout = order.Nil
		}
	}
	return layout, nil
}

func (o *Optimizer) getLayout(s dag.Source, parent order.Layout) (order.Layout, error) {
	switch s := s.(type) {
	case *dag.File:
		return s.Layout, nil
	case *dag.HTTP:
		return s.Layout, nil
	case *dag.Pool, *dag.LakeMeta, *dag.PoolMeta, *dag.CommitMeta:
		return o.source.Layout(o.ctx, s), nil
	case *dag.Pass:
		return parent, nil
	case *kernel.Reader:
		return s.Layout, nil
	default:
		return order.Nil, fmt.Errorf("unknown dag.Source type %T", s)
	}
}

// Parallelize takes a sequential operation and tries to
// parallelize it by splitting as much as possible of the sequence
// into n parallel branches. The boolean return argument indicates
// whether the flowgraph could be parallelized.
func (o *Optimizer) Parallelize(n int) error {
	replicas := n - 1
	if replicas < 1 {
		return fmt.Errorf("bad parallelization factor: %d", n)
	}
	if replicas > 50 {
		// XXX arbitrary circuit breaker
		return fmt.Errorf("parallelization factor too big: %d", n)
	}
	seq := o.entry
	from, ok := seq.Ops[0].(*dag.From)
	if !ok {
		return nil
	}
	trunks := poolTrunks(from)
	if len(trunks) == 1 {
		quietCuts(trunks[0])
		if err := o.parallelizeTrunk(seq, trunks[0], replicas); err != nil {
			return err
		}
	}
	return nil
}

func quietCuts(trunk *dag.Trunk) {
	if trunk.Seq == nil {
		return
	}
	for _, op := range trunk.Seq.Ops {
		if cut, ok := op.(*dag.Cut); ok {
			cut.Quiet = true
		}
	}
}

func poolTrunks(from *dag.From) []*dag.Trunk {
	var trunks []*dag.Trunk
	for k := range from.Trunks {
		trunk := &from.Trunks[k]
		if _, ok := trunk.Source.(*dag.Pool); ok {
			trunks = append(trunks, trunk)
		}
	}
	return trunks
}

func liftInto(trunk *dag.Trunk, branch []dag.Op) {
	if trunk.Seq == nil {
		trunk.Seq = &dag.Sequential{
			Kind: "Sequential",
		}
	}
	trunk.Seq.Ops = append(trunk.Seq.Ops, branch...)
}

func extend(trunk *dag.Trunk, op dag.Op) {
	if trunk.Seq == nil {
		trunk.Seq = &dag.Sequential{Kind: "Sequential"}
	}
	trunk.Seq.Append(op)
}

// pushDown attempts to move any filter from the front of the trunk's sequence
// into the PushDown field of the Trunk so that the runtime can push the
// filter predicate into the scanner.  This is a very simple optimization for now
// that works for only a single filter operator.  In the future, the pushown
// logic to handle arbitrary columnar operations will happen here, perhaps with
// some involvement from the DataAdaptor.
func pushDown(trunk *dag.Trunk) {
	seq := trunk.Seq
	if seq == nil || len(seq.Ops) == 0 {
		return
	}
	filter, ok := seq.Ops[0].(*dag.Filter)
	if !ok {
		return
	}
	seq.Ops = seq.Ops[1:]
	if len(seq.Ops) == 0 {
		trunk.Seq = nil
	}
	trunk.Pushdown = filter
}

// newRangePruner returns a new predicate based on the input predicate pred
// that when applied to an input value (i.e., "this") with fields from/to, returns
// true if comparisons in pred against literal values can for certain rule out
// that pred would be true for any value in the from/to range.  From/to are presumed
// to be ordered according to the order o.  This is used to prune metadata objects
// from a scan when we know the pool-key range of the object could not satisfy
// the filter predicate of any of the values in the object.
func newRangePruner(pred dag.Expr, fld field.Path, o order.Which) *dag.BinaryExpr {
	lower := &dag.This{Kind: "This", Path: field.New("from")}
	upper := &dag.This{Kind: "This", Path: field.New("to")}
	if o == order.Desc {
		lower, upper = upper, lower
	}
	e, _ := buildRangePruner(pred, fld, lower, upper)
	return e
}

func buildRangePruner(pred dag.Expr, fld field.Path, lower, upper *dag.This) (*dag.BinaryExpr, bool) {
	e, ok := pred.(*dag.BinaryExpr)
	if !ok {
		//XXX this doesn't work because you could have some other logic (like a func return bool)
		// that could rely on fld... e.g., a simple cast
		//return nil, true
		return nil, false
	}
	switch e.Op {
	case "and", "or":
		lhs, lok := buildRangePruner(e.LHS, fld, lower, upper)
		rhs, rok := buildRangePruner(e.RHS, fld, lower, upper)
		if !lok || !rok {
			return nil, false
		}
		if lhs == nil {
			return rhs, true
		}
		if rhs == nil {
			return lhs, true
		}
		// We "and" the two sides here as we can only prune if
		// both sub-expressions say it's ok.
		return dag.NewBinaryExpr("and", lhs, rhs), true
	case "==", "<", "<=", ">", ">=":
		this, literal, op := literalComparison(e)
		if this == nil {
			return nil, false
		}
		if !fld.Equal(this.Path) {
			// It's a literal comparison and we know fld isn't involved,
			// so we know this doesn't effect the prune decision and we
			// can return true here.
			return nil, true
		}
		// At this point, we know we can definitely run a pruning decision based
		// on the literal value we found, the comparison op, and the lower/upper bounds.
		//XXX this should use compare for cross-type comparisons...
		// XXX need to use lower/upper here
		return rangePrunerPred(op, literal, lower, upper), true
	default:
		return nil, false
	}
}

func rangePrunerPred(op string, literal *dag.Literal, lower, upper *dag.This) *dag.BinaryExpr {
	//XXX these should use the compare func (or it seems like the language should support this natively)
	switch op {
	case "<":
		// key < CONST
		return dag.NewBinaryExpr("<=", literal, lower)
	case "<=":
		// key <= CONST
		return dag.NewBinaryExpr("<", literal, lower)
	case ">":
		// key > CONST
		return dag.NewBinaryExpr(">=", literal, upper)
	case ">=":
		// key >= CONST
		return dag.NewBinaryExpr(">", literal, upper)
	case "==":
		// key == CONST
		return dag.NewBinaryExpr("or",
			dag.NewBinaryExpr("<", literal, lower),
			dag.NewBinaryExpr(">", literal, upper))
	}
	panic("rangePrunerPred unknown op " + op)
}

// XXX keep this for above
func relativeToCompare(op string, lhs, rhs dag.Expr, o order.Which) *dag.BinaryExpr {
	nullsMax := &dag.Literal{Kind: "Literal", Value: "false"}
	if o == order.Asc {
		nullsMax.Value = "true"
	}
	lhs = &dag.Call{Kind: "Call", Name: "compare", Args: []dag.Expr{lhs, rhs, nullsMax}}
	return dag.NewBinaryExpr(op, lhs, &dag.Literal{Kind: "Literal", Value: "0"})
}

func literalComparison(e *dag.BinaryExpr) (*dag.This, *dag.Literal, string) {
	switch lhs := e.LHS.(type) {
	case *dag.This:
		if rhs, ok := e.RHS.(*dag.Literal); ok {
			return lhs, rhs, e.Op
		}
	case *dag.Literal:
		if rhs, ok := e.RHS.(*dag.This); ok {
			return rhs, lhs, reverseComparator(e.Op)
		}
	}
	return nil, nil, ""
}

func reverseComparator(op string) string {
	switch op {
	case "==", "!=":
		return op
	case "<":
		return ">="
	case "<=":
		return ">"
	case ">":
		return "<="
	case ">=":
		return "<"
	}
	panic("unknown op")
}
