package meta

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/brimdata/zed"
	"github.com/brimdata/zed/lake/data"
	"github.com/brimdata/zed/order"
	"github.com/brimdata/zed/runtime/expr"
	"github.com/brimdata/zed/runtime/expr/extent"
	"github.com/brimdata/zed/runtime/op"
	"github.com/brimdata/zed/zbuf"
	"github.com/brimdata/zed/zson"
	"github.com/segmentio/ksuid"
)

// Slicer implements an op that pulls metadata ObjectRanges and organizes
// them into overlapping ObjectRanges into a sequence of non-overlapping Partitions.
type Slicer struct {
	parent      zbuf.Puller
	pctx        *op.Context
	marshaler   *zson.MarshalZNGContext
	unmarshaler *zson.UnmarshalZNGContext
	objects     []*objectSpan
	cmp         expr.CompareFn
}

func NewSlicer(pctx *op.Context, parent zbuf.Puller, o order.Which) *Slicer {
	return &Slicer{
		parent:      parent,
		pctx:        pctx,
		marshaler:   zson.NewZNGMarshaler(),
		unmarshaler: zson.NewZNGUnmarshaler(),
		objects:     []*objectSpan{},
		cmp:         extent.CompareFunc(o),
	}
}

func (s *Slicer) Pull(done bool) (zbuf.Batch, error) {
	for {
		batch, err := s.parent.Pull(done)
		if err != nil {
			return nil, err
		}
		if batch == nil {
			return s.next(), nil
		}
		vals := batch.Values()
		if len(vals) != 1 {
			// We currently support only one object per batch.
			return nil, errors.New("system error: Searcher encountered multi-valued batch")
		}
		var o ObjectRange
		if err := s.unmarshaler.Unmarshal(&vals[0], &o); err != nil {
			return nil, err
		}
		os := &objectSpan{
			Span:   extent.NewGeneric(o.Object.First, o.Object.Last, s.cmp),
			object: &o,
		}
		if batch := s.stash(os); batch != nil {
			return batch, nil
		}
	}
}

func (s *Slicer) next() zbuf.Batch {

	//XXX TBD
	return nil
}

func (s *Slicer) stash(object *objectSpan) zbuf.Batch {
	if len(s.objects) == 0 {
		s.objects = append(s.objects, object)
		return nil
	}
	// We collect all the subsequent objects that overlap with the base object
	// until an objects start key XXX finish comment
	// XXX handle case where first==last and preserve order that data was written
	// into system when doing an in-order scan.
	base := s.objects[0]
	if object.After(base.Last()) {
		//XXX TBD
		// return what we have, but also recursively stash this object which could 
		// trigger another partition and so on.  change protocol here to accumulate 
		// batches in a slice/queue then return the batches from the queue.
		return s.next()
	}
	return nil
}

// partitionObjects takes a sorted set of data objects with possibly overlapping
// key ranges and returns an ordered list of Ranges such that none of the
// Ranges overlap with one another.  This is the straightforward computational
// geometry problem of merging overlapping intervals,
// e.g., https://www.geeksforgeeks.org/merging-intervals/
//
// XXX this algorithm doesn't quite do what we want because it continues
// to merge *anything* that overlaps.  It's easy to fix though.
// Issue #2538
func partitionObjects(objects []*data.Object, o order.Which) []Partition {
	return nil
	/*
		if len(objects) == 0 {
			return nil
		}
		cmp := extent.CompareFunc(o)
		spans := sortedObjectSpans(objects, cmp)
		var s stack
		s.pushObjectSpan(spans[0], cmp)
		for _, span := range spans[1:] {
			tos := s.tos()
			if span.Before(tos.Last()) {
				s.pushObjectSpan(span, cmp)
			} else {
				tos.Objects = append(tos.Objects, span.object)
				tos.Extend(span.Last())
			}
		}
		// On exit, the ranges in the stack are properly sorted so
		// we just turn the ranges back into partitions.
		partitions := make([]Partition, 0, len(s))
		for _, r := range s {
			partitions = append(partitions, Partition{
				First:   r.First(),
				Last:    r.Last(),
				Objects: r.Objects,
			})
		}
		return partitions
	*/
}

type objectSpan struct {
	extent.Span
	object *ObjectRange
}

/*
func sortedObjectSpans(objects []*data.Object, cmp expr.CompareFn) []objectSpan {
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

func objectSpanLess(a, b objectSpan) bool {
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

// A Partition is a logical view of the records within a time span, stored
// in one or more data objects.  This provides a way to return the list of
// objects that should be scanned along with a span to limit the scan
// to only the span involved.
type Partition struct {
	First   *zed.Value
	Last    *zed.Value
	Objects []*data.Object
}

func (p Partition) IsZero() bool {
	return p.Objects == nil
}

func (p Partition) FormatRangeOf(index int) string {
	o := p.Objects[index]
	return fmt.Sprintf("[%s-%s,%s-%s]", zson.String(p.First), zson.String(p.Last), zson.String(o.First), zson.String(o.Last))
}

func (p Partition) FormatRange() string {
	return fmt.Sprintf("[%s-%s]", zson.String(p.First), zson.String(p.Last))
}
