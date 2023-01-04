package meta

import (
	"bytes"
	"context"
	"fmt"
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

// XXX Lister enumerates all the ObjectRanges in a scan.  After we get this working,
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
	objects   []Slice
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

// XXX
func (l *Lister) Snapshot() commits.View {
	return l.snap
}

func (l *Lister) Pull(done bool) (zbuf.Batch, error) {
	fmt.Println("LISTER PULL")
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.objects == nil {
		l.objects, l.err = initObjectScan(l.snap, l.pool.Layout, l.filter)
		if l.err != nil {
			return nil, l.err
		}
	}
	if l.err != nil || len(l.objects) == 0 {
		return nil, l.err
	}
	fmt.Println("LISTER objects", len(l.objects))
	//XXX we could change this so a scan can appear inside of a subgraph and
	// be restarted after each time done is called (like head/tail work)
	if len(l.objects) == 0 {
		return nil, l.err
	}
	o := l.objects[0]
	l.objects = l.objects[1:]
	val, err := l.marshaler.Marshal(o)
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

type Slice struct {
	First  *zed.Value
	Last   *zed.Value
	Object *data.Object
}

type objectSlice struct {
	extent.Span
	object *data.Object
}

func initObjectScan(snap commits.View, layout order.Layout, filter zbuf.Filter) ([]Slice, error) {
	objects := snap.Select(nil, layout.Order)
	var f *expr.SpanFilter //XXX get rid of this
	if filter != nil {
		var err error
		f, err = filter.AsKeySpanFilter(layout.Primary(), layout.Order)
		if err != nil {
			return nil, err
		}
	}
	cmp := expr.NewValueCompareFn(layout.Order == order.Asc)
	var out []*objectSlice
	for _, obj := range objects {
		span := extent.NewGeneric(obj.First, obj.Last, cmp)
		if f == nil || !f.Eval(span.First(), span.Last()) {
			out = append(out, &objectSlice{span, obj})

		}
	}
	sort.Slice(out, func(i, j int) bool {
		return objectSliceLess(out[i], out[j])
	})
	var slices []Slice
	for _, o := range out {
		slices = append(slices, Slice{
			First:  o.First(),
			Last:   o.Last(),
			Object: o.object,
		})
	}
	return slices, nil
}

func objectSliceLess(a, b *objectSlice) bool {
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
