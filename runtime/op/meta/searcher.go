package meta

import (
	"context"
	"errors"

	"github.com/brimdata/zed"
	"github.com/brimdata/zed/lake"
	"github.com/brimdata/zed/lake/commits"
	"github.com/brimdata/zed/lake/data"
	"github.com/brimdata/zed/lake/index"
	"github.com/brimdata/zed/lake/seekindex"
	"github.com/brimdata/zed/order"
	"github.com/brimdata/zed/runtime/expr"
	"github.com/brimdata/zed/runtime/expr/extent"
	"github.com/brimdata/zed/runtime/op"
	"github.com/brimdata/zed/zbuf"
	"github.com/brimdata/zed/zson"
)

// Searcher implements an op that pulls metadata objects and applies a
// a search index lookup to each object dropping the objects not found by
// the index lookup.
type Searcher struct {
	parent      zbuf.Puller
	filter      zbuf.Filter
	pctx        *op.Context
	pool        *lake.Pool
	snap        commits.View
	marshaler   *zson.MarshalZNGContext
	unmarshaler *zson.UnmarshalZNGContext
}

func NewSearcher(pctx *op.Context, parent zbuf.Puller, pool *lake.Pool, snap commits.View, filter zbuf.Filter) *Searcher {
	return &Searcher{
		pctx:        pctx,
		parent:      parent,
		filter:      filter,
		pool:        pool,
		snap:        snap,
		marshaler:   zson.NewZNGMarshaler(),
		unmarshaler: zson.NewZNGUnmarshaler(),
	}
}

//XXX seekindex range should be fetched by the sequence scanner

type ObjectRange struct {
	data.Object
	Range *seekindex.Range
}

func (s *Searcher) Pull(done bool) (zbuf.Batch, error) {
	// XXX start with inefficient index lookup per Pull then add a semaphore
	// to parallelize the lookups while maintaining object order.

	// Pull the next object from the parent lister.
	// set up the next scanner to pull from.
	batch, err := s.parent.Pull(done)
	if batch == nil || err != nil {
		return nil, err
	}
	vals := batch.Values()
	if len(vals) != 1 {
		// We currently support only one object per batch.
		return nil, errors.New("system error: Searcher encountered multi-valued batch")
	}
	var object data.Object
	if err := s.unmarshaler.Unmarshal(&vals[0], &object); err != nil {
		return nil, err
	}
	// XXX move ObjectRange into objectRange as return value?
	r, err := objectRange(s.pctx.Context, s.pool, s.snap, s.filter, &object)
	if err != nil {
		return nil, err
	}
	val, err := s.marshaler.Marshal(ObjectRange{object, r})
	if err != nil {
		return nil, err
	}
	return zbuf.NewArray([]zed.Value{*val}), nil
}

func objectRange(ctx context.Context, pool *lake.Pool, snap commits.View, filter zbuf.Filter, o *data.Object) (*seekindex.Range, error) {
	var indexSpan extent.Span
	var cropped *expr.SpanFilter
	//XXX this is suboptimal because we traverse every index rule of every object
	// even though we should know what rules we need upstream by analyzing the
	// type of index lookup we're doing and select only the rules needed
	if filter != nil {
		if idx := index.NewFilter(pool.Storage(), pool.IndexPath, filter); idx != nil {
			rules, err := snap.LookupIndexObjectRules(o.ID) // XXX move this into metadata
			if err != nil && !errors.Is(err, commits.ErrNotFound) {
				return nil, err
			}
			if len(rules) > 0 {
				indexSpan, err = idx.Apply(ctx, o.ID, rules)
				if err != nil || indexSpan == nil {
					return nil, err
				}
			}
		}
		var err error
		cropped, err = filter.AsKeyCroppedByFilter(pool.Layout.Primary(), pool.Layout.Order)
		if err != nil {
			return nil, err
		}
	}
	cmp := expr.NewValueCompareFn(pool.Layout.Order == order.Asc)
	span := extent.NewGeneric(o.First, o.Last, cmp)
	if indexSpan != nil || cropped != nil && cropped.Eval(span.First(), span.Last()) {
		// There's an index available or the object's span is cropped by
		// p.filter, so use the seek index to find the range to scan.
		spanFilter, err := filter.AsKeySpanFilter(pool.Layout.Primary(), pool.Layout.Order)
		if err != nil {
			return nil, err
		}
		r, err := data.LookupSeekRange(ctx, pool.Storage(), pool.DataPath, o, cmp, spanFilter, indexSpan)
		if err != nil {
			return nil, err
		}
		return &r, nil
	}
	// Scan the entire object.
	return &seekindex.Range{End: o.Size}, nil
}
