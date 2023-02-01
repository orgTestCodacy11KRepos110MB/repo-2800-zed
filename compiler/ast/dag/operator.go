package dag

// This module is derived from the GO AST design pattern in
// https://golang.org/pkg/go/ast/
//
// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

import (
	"github.com/brimdata/zed/order"
	"github.com/brimdata/zed/pkg/field"
	"github.com/segmentio/ksuid"
)

type Op interface {
	OpNode()
}

var PassOp = &Pass{Kind: "Pass"}

// Ops

type (
	Cut struct {
		Kind  string       `json:"kind" unpack:""`
		Args  []Assignment `json:"args"`
		Quiet bool         `json:"quiet"`
	}
	Drop struct {
		Kind string `json:"kind" unpack:""`
		Args []Expr `json:"args"`
	}
	Explode struct {
		Kind string `json:"kind" unpack:""`
		Args []Expr `json:"args"`
		Type string `json:"type"`
		As   Expr   `json:"as"`
	}
	Filter struct {
		Kind string `json:"kind" unpack:""`
		Expr Expr   `json:"expr"`
	}
	Fuse struct {
		Kind string `json:"kind" unpack:""`
	}
	Head struct {
		Kind  string `json:"kind" unpack:""`
		Count int    `json:"count"`
	}
	Join struct {
		Kind     string       `json:"kind" unpack:""`
		Style    string       `json:"style"`
		LeftKey  Expr         `json:"left_key"`
		RightKey Expr         `json:"right_key"`
		Args     []Assignment `json:"args"`
	}
	Merge struct {
		Kind  string      `json:"kind" unpack:""`
		Expr  Expr        `json:"expr"`
		Order order.Which `json:"order"`
	}
	Parallel struct {
		Kind string `json:"kind" unpack:""`
		Ops  []Op   `json:"ops"`
	}
	Pass struct {
		Kind string `json:"kind" unpack:""`
	}
	Pick struct {
		Kind string       `json:"kind" unpack:""`
		Args []Assignment `json:"args"`
	}
	Put struct {
		Kind string       `json:"kind" unpack:""`
		Args []Assignment `json:"args"`
	}
	Rename struct {
		Kind string       `json:"kind" unpack:""`
		Args []Assignment `json:"args"`
	}
	Sequential struct {
		Kind   string  `json:"kind" unpack:""`
		Consts []Def   `json:"consts"`
		Funcs  []*Func `json:"funcs"`
		Ops    []Op    `json:"ops"`
	}
	Shape struct {
		Kind string `json:"kind" unpack:""`
	}
	Sort struct {
		Kind       string      `json:"kind" unpack:""`
		Args       []Expr      `json:"args"`
		Order      order.Which `json:"order"`
		NullsFirst bool        `json:"nullsfirst"`
	}
	Summarize struct {
		Kind         string       `json:"kind" unpack:""`
		Limit        int          `json:"limit"`
		Keys         []Assignment `json:"keys"`
		Aggs         []Assignment `json:"aggs"`
		InputSortDir int          `json:"input_sort_dir,omitempty"`
		PartialsIn   bool         `json:"partials_in,omitempty"`
		PartialsOut  bool         `json:"partials_out,omitempty"`
	}
	Switch struct {
		Kind  string `json:"kind" unpack:""`
		Expr  Expr   `json:"expr"`
		Cases []Case `json:"cases"`
	}
	Tail struct {
		Kind  string `json:"kind" unpack:""`
		Count int    `json:"count"`
	}
	Top struct {
		Kind  string `json:"kind" unpack:""`
		Limit int    `json:"limit"`
		Args  []Expr `json:"args"`
		Flush bool   `json:"flush"`
	}
	Let struct {
		Kind string `json:"kind" unpack:""`
		Defs []Def  `json:"defs"`
		Over *Over  `json:"over"`
	}
	Over struct {
		Kind  string      `json:"kind" unpack:""`
		Exprs []Expr      `json:"exprs"`
		Scope *Sequential `json:"scope"`
	}
	Uniq struct {
		Kind  string `json:"kind" unpack:""`
		Cflag bool   `json:"cflag"`
	}
	Yield struct {
		Kind  string `json:"kind" unpack:""`
		Exprs []Expr `json:"exprs"`
	}
)

// Input structure

type (
	From struct {
		Kind   string  `json:"kind" unpack:""`
		Trunks []Trunk `json:"trunks"`
	}

	// A Trunk is the path into a DAG for any input source.  It contains
	// the source to scan as well as the sequential operators to apply
	// to the scan before being joined, merged, or output.  A DAG can be
	// just one Trunk or an assembly of different Trunks mixed in using
	// the From Op.  The Trunk is the one place where the optimizer places
	// pushed down predicates so the runtime can move the pushed down
	// operators into each scan scheduler for each source when the runtime
	// is built.  When computation is distribtued over the network, the
	// optimized pushdown is naturally carried in the serialized DAG via
	// each Trunk.
	Trunk struct {
		Kind      string      `json:"kind" unpack:""`
		Source    Source      `json:"source"`
		Seq       *Sequential `json:"seq"`
		Pushdown  Op          `json:"pushdown"`
		KeyPruner Expr        `json:"key_pruner"`
	}

	// Leaf sources

	File struct {
		Kind   string       `json:"kind" unpack:""`
		Path   string       `json:"path"`
		Format string       `json:"format"`
		Layout order.Layout `json:"layout"`
	}
	HTTP struct {
		Kind   string       `json:"kind" unpack:""`
		URL    string       `json:"url"`
		Format string       `json:"format"`
		Layout order.Layout `json:"layout"`
	}
	Pool struct {
		Kind      string      `json:"kind" unpack:""`
		ID        ksuid.KSUID `json:"id"`
		Commit    ksuid.KSUID `json:"commit"`
		Delete    bool        `json:"delete"`
		ScanLower Expr        `json:"scan_lower"`
		ScanUpper Expr        `json:"scan_upper"`
		ScanOrder string      `json:"scan_order"`
	}
	PoolMeta struct {
		Kind string      `json:"kind" unpack:""`
		ID   ksuid.KSUID `json:"id"`
		Meta string      `json:"meta"`
	}
	CommitMeta struct {
		Kind      string      `json:"kind" unpack:""`
		Pool      ksuid.KSUID `json:"pool"`
		Commit    ksuid.KSUID `json:"branch"`
		Meta      string      `json:"meta"`
		ScanLower Expr        `json:"scan_lower"`
		ScanUpper Expr        `json:"scan_upper"`
		ScanOrder string      `json:"scan_order"`
	}
	LakeMeta struct {
		Kind string `json:"kind" unpack:""`
		Meta string `json:"meta"`
	}
)

type Source interface {
	Source()
}

func (*File) Source()       {}
func (*HTTP) Source()       {}
func (*Pool) Source()       {}
func (*LakeMeta) Source()   {}
func (*PoolMeta) Source()   {}
func (*CommitMeta) Source() {}
func (*Pass) Source()       {}

// A From node can be a DAG entrypoint or an operator.  When it appears
// as an operator it mixes its single parent in with other Trunks to
// form a parallel structure whose output must be joined or merged.

func (*From) OpNode() {}

// Various Op fields

type (
	Agg struct {
		Kind  string `json:"kind" unpack:""`
		Name  string `json:"name"`
		Expr  Expr   `json:"expr"`
		Where Expr   `json:"where"`
	}
	Case struct {
		Expr Expr `json:"expr"`
		Op   Op   `json:"op"`
	}
	Def struct {
		Name string `json:"name"`
		Expr Expr   `json:"expr"`
	}
	Method struct {
		Name string `json:"name"`
		Args []Expr `json:"args"`
	}
)

func (*Sequential) OpNode() {}
func (*Parallel) OpNode()   {}
func (*Switch) OpNode()     {}
func (*Sort) OpNode()       {}
func (*Cut) OpNode()        {}
func (*Pick) OpNode()       {}
func (*Drop) OpNode()       {}
func (*Head) OpNode()       {}
func (*Tail) OpNode()       {}
func (*Pass) OpNode()       {}
func (*Filter) OpNode()     {}
func (*Uniq) OpNode()       {}
func (*Summarize) OpNode()  {}
func (*Top) OpNode()        {}
func (*Put) OpNode()        {}
func (*Rename) OpNode()     {}
func (*Fuse) OpNode()       {}
func (*Join) OpNode()       {}
func (*Shape) OpNode()      {}
func (*Explode) OpNode()    {}
func (*Over) OpNode()       {}
func (*Let) OpNode()        {}
func (*Yield) OpNode()      {}
func (*Merge) OpNode()      {}

func (seq *Sequential) IsEntry() bool {
	if len(seq.Ops) == 0 {
		return false
	}
	switch op := seq.Ops[0].(type) {
	case *From:
		return true
	case *Parallel:
		if len(op.Ops) == 0 {
			return false
		}
		for _, o := range op.Ops {
			if s, ok := o.(*Sequential); !ok || !s.IsEntry() {
				return false
			}
		}
		return true
	}
	return false
}

func (seq *Sequential) Prepend(front Op) {
	seq.Ops = append([]Op{front}, seq.Ops...)
}

func (seq *Sequential) Append(op Op) {
	seq.Ops = append(seq.Ops, op)
}

func (seq *Sequential) Delete(at, length int) {
	seq.Ops = append(seq.Ops[0:at], seq.Ops[at+length:]...)
}

func FanIn(op Op) int {
	switch op := op.(type) {
	case *Sequential:
		return FanIn(op.Ops[0])
	case *Join:
		return 2
	}
	return 1
}

func FilterToOp(e Expr) *Filter {
	return &Filter{
		Kind: "Filter",
		Expr: e,
	}
}

func (t *This) String() string {
	return field.Path(t.Path).String()
}
