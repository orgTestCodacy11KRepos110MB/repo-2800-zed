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
	"github.com/brimdata/zed/runtime/expr/extent"
	"github.com/brimdata/zed/zbuf"
	"github.com/brimdata/zed/zson"
	"github.com/segmentio/ksuid"
	"golang.org/x/sync/errgroup"
)

// XXX Lister enumerates all the partitions in a scan.  After we get this working,
// we will modularize further by listing just the objects and having
// the slicer to organize objects into slices (formerly known as partitions).
type Lister struct {
	ctx       context.Context
	pool      *lake.Pool
	snap      commits.View
	filter    zbuf.Filter
	group     *errgroup.Group
	marshaler *zson.MarshalZNGContext
	mu        sync.Mutex
	parts     []Partition
	err       error
}

var _ zbuf.Puller = (*Lister)(nil)

func NewSortedLister(ctx context.Context, r *lake.Root, pool *lake.Pool, commit ksuid.KSUID, filter zbuf.Filter) (*Lister, error) {
	snap, err := pool.Snapshot(ctx, commit)
	if err != nil {
		return nil, err
	}
	return NewSortedListerFromSnap(ctx, r, pool, snap, filter), nil
}

func NewSortedListerByID(ctx context.Context, r *lake.Root, poolID, commit ksuid.KSUID, filter zbuf.Filter) (*Lister, error) {
	pool, err := r.OpenPool(ctx, poolID)
	if err != nil {
		return nil, err
	}
	return NewSortedLister(ctx, r, pool, commit, filter)
}

func NewSortedListerFromSnap(ctx context.Context, r *lake.Root, pool *lake.Pool, snap commits.View, filter zbuf.Filter) *Lister {
	return &Lister{
		ctx:       ctx,
		pool:      pool,
		snap:      snap,
		filter:    filter,
		group:     &errgroup.Group{},
		marshaler: zson.NewZNGMarshaler(),
	}
}

func (l *Lister) Snapshot() commits.View {
	return l.snap
}

func (l *Lister) Pull(done bool) (zbuf.Batch, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return nil, l.err
	}
	if l.parts == nil {
		l.parts, l.err = sortedPartitions(l.snap, l.pool.Layout, l.filter)
		if l.err != nil {
			return nil, l.err
		}
	}
	if len(l.parts) == 0 {
		return nil, l.err
	}
	part := l.parts[0]
	l.parts = l.parts[1:]
	val, err := l.marshaler.Marshal(part)
	if err != nil {
		l.err = err
		return nil, err
	}
	return zbuf.NewArray([]zed.Value{*val}), nil
}

// XXX change this to use spannedObject and do a sort too that used to be done
// by the partition logic.
func XXXfilterObjects(objects []*data.Object, filter *expr.SpanFilter, o order.Which) []*data.Object {
	cmp := expr.NewValueCompareFn(o == order.Asc)
	out := objects[:0]
	for _, obj := range objects {
		span := extent.NewGeneric(obj.First, obj.Last, cmp)
		if filter == nil || !filter.Eval(span.First(), span.Last()) {
			out = append(out, obj)
		}
	}

	return out
}

func filterSpannedObjects(objects []*data.Object, filter *expr.SpanFilter, o order.Which) []*ObjectSpan {
	cmp := expr.NewValueCompareFn(o == order.Asc)
	var out []*ObjectSpan
	for _, obj := range objects {
		span := extent.NewGeneric(obj.First, obj.Last, cmp)
		if filter == nil || !filter.Eval(span.First(), span.Last()) {
			out = append(out, &ObjectSpan{span, obj})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return objectSpanLess(out[i], out[j])
	})
	return out
}

type ObjectSpan struct {
	extent.Span
	object *data.Object
}

/*
func sortedObjectSpans(objects []*data.Object, cmp expr.CompareFn) []*objectSpan {
	spans := make([]objectSpan, 0, len(objects))
	for _, o := range objects {
		spans = append(spans, objectSpan{
			Span:   extent.NewGeneric(o.First, o.Last, cmp),
			object: o,
		})
	}
	sort.Slice(spans, func(i, j int) bool {
		return objectSpanLess(spans[i], spans[j])
	})
	return spans
}
*/

func objectSpanLess(a, b *ObjectSpan) bool {
	if b.Before(a.First()) {
		return true
	}
	if !bytes.Equal(a.First().Bytes, b.First().Bytes) {
		return false
	}
	if bytes.Equal(a.Last().Bytes, b.Last().Bytes) {
		if a.object.Count != b.object.Count {
			return a.object.Count < b.object.Count
		}
		return ksuid.Compare(a.object.ID, b.object.ID) < 0
	}
	return a.After(b.Last())
}

/*XXX replace this with slicer implementation

// sortedPartitions partitions all the data objects in snap overlapping
// span into non-overlapping partitions and sorts them by pool key and order.
func sortedPartitions(snap commits.View, layout order.Layout, filter zbuf.Filter) ([]Partition, error) {
	objects := snap.Select(nil, layout.Order)
	if filter != nil {
		f, err := filter.AsKeySpanFilter(layout.Primary(), layout.Order)
		if err != nil {
			return nil, err
		}
		objects = filterObjects(objects, f, layout.Order)
	}
	return partitionObjects(objects, layout.Order), nil
}
*/
