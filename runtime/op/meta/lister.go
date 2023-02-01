package meta

import (
	"bytes"
	"context"
	"sort"
	"sync"

	"github.com/brimdata/zed"
	"github.com/brimdata/zed/lake"
	"github.com/brimdata/zed/lake/commits"
	"github.com/brimdata/zed/lake/data"
	"github.com/brimdata/zed/order"
	"github.com/brimdata/zed/runtime/expr"
	"github.com/brimdata/zed/zbuf"
	"github.com/brimdata/zed/zson"
	"github.com/segmentio/ksuid"
	"golang.org/x/sync/errgroup"
)

// Lister enumerates all the data.Objects in a scan.  A Slicer downstream may
// optionally organize objects into non-overlapping partitions for merge on read.
// The optimizer may decide when partitions are necessary based on the order
// sensitivity of the downstream flowgraph.
type Lister struct {
	ctx       context.Context
	pool      *lake.Pool
	snap      commits.View
	pruner    expr.Evaluator
	group     *errgroup.Group
	marshaler *zson.MarshalZNGContext
	mu        sync.Mutex
	objects   []*data.Object
	err       error
}

var _ zbuf.Puller = (*Lister)(nil)

func NewSortedLister(ctx context.Context, zctx *zed.Context, r *lake.Root, pool *lake.Pool, commit ksuid.KSUID, pruner expr.Evaluator) (*Lister, error) {
	snap, err := pool.Snapshot(ctx, commit)
	if err != nil {
		return nil, err
	}
	return NewSortedListerFromSnap(ctx, zctx, r, pool, snap, pruner), nil
}

func NewSortedListerByID(ctx context.Context, zctx *zed.Context, r *lake.Root, poolID, commit ksuid.KSUID, pruner expr.Evaluator) (*Lister, error) {
	pool, err := r.OpenPool(ctx, poolID)
	if err != nil {
		return nil, err
	}
	return NewSortedLister(ctx, zctx, r, pool, commit, pruner)
}

func NewSortedListerFromSnap(ctx context.Context, zctx *zed.Context, r *lake.Root, pool *lake.Pool, snap commits.View, pruner expr.Evaluator) *Lister {
	m := zson.NewZNGMarshalerWithContext(zctx)
	m.Decorate(zson.StylePackage)
	return &Lister{
		ctx:       ctx,
		pool:      pool,
		snap:      snap,
		pruner:    pruner,
		group:     &errgroup.Group{},
		marshaler: m,
	}
}

func (l *Lister) Snapshot() commits.View {
	return l.snap
}

func (l *Lister) Pull(done bool) (zbuf.Batch, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.objects == nil {
		l.objects = initObjectScan(l.snap, l.pool.Layout)
	}
	if l.err != nil {
		return nil, l.err
	}
	for len(l.objects) != 0 {
		o := l.objects[0]
		l.objects = l.objects[1:]
		val, err := l.marshaler.Marshal(o)
		if err != nil {
			l.err = err
			return nil, err
		}
		if !l.prune(val) {
			return zbuf.NewArray([]zed.Value{*val}), nil
		}
	}
	return nil, nil
}

// XXX
func (l *Lister) prune(val *zed.Value) bool {
	// ectx nil
	result := l.pruner.Eval(nil, val)
	return result.Type == zed.TypeBool && zed.IsTrue(result.Bytes)
}

func initObjectScan(snap commits.View, layout order.Layout) []*data.Object {
	objects := snap.Select(nil, layout.Order)
	//XXX at some point sorting should be optional.
	sortObjects(objects, layout.Order)
	return objects
}

func sortObjects(objects []*data.Object, o order.Which) {
	cmp := expr.NewValueCompareFn(o, o == order.Asc) //XXX is nullsMax correct here?
	lessFunc := func(a, b *data.Object) bool {
		if cmp(&a.First, &b.First) < 0 {
			return true
		}
		if !bytes.Equal(a.First.Bytes, b.First.Bytes) {
			return false
		}
		if bytes.Equal(a.Last.Bytes, b.Last.Bytes) {
			// If the pool keys are equal for both the first and last values
			// in the object, we return false here so that the stable sort preserves
			// the commit order of the objects in the log. XXX we might want to
			// simply sort by commit timestamp for a more robust API that does not
			// presume commit-order in the object snapshot.
			return false
		}
		return cmp(&a.Last, &b.Last) < 0
	}
	sort.SliceStable(objects, func(i, j int) bool {
		return lessFunc(objects[i], objects[j])
	})
}
