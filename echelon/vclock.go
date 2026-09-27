package echelon

import (
	"fmt"
	"sort"
	"strings"
)

// ClockEntry is one (node, counter) pair of a vector clock. Stamp records
// when the pair was last updated and is used only for truncation.
type ClockEntry struct {
	Node    string
	Counter uint64
	Stamp   int64
}

// VClock is a vector clock: a list of entries sorted by node name.
//
// Each time a node coordinates a write it increments its own counter.
// Comparing two clocks tells us whether one version is an ancestor of the
// other (and can be discarded) or whether they are concurrent siblings
// that the application must reconcile.
type VClock []ClockEntry

// Order is the result of comparing two clocks.
type Order int

const (
	Equal      Order = iota
	Before           // a happened before b: b supersedes a
	After            // a happened after b: a supersedes b
	Concurrent       // neither descends from the other: a conflict
)

func (o Order) String() string {
	return [...]string{"equal", "before", "after", "concurrent"}[o]
}

// Get returns node's counter (0 if absent).
func (v VClock) Get(node string) uint64 {
	i := sort.Search(len(v), func(i int) bool { return v[i].Node >= node })
	if i < len(v) && v[i].Node == node {
		return v[i].Counter
	}
	return 0
}

// Clone returns an independent copy.
func (v VClock) Clone() VClock { return append(VClock(nil), v...) }

// Increment returns a copy with node's counter bumped by one.
func (v VClock) Increment(node string, stamp int64) VClock {
	out := v.Clone()
	i := sort.Search(len(out), func(i int) bool { return out[i].Node >= node })
	if i < len(out) && out[i].Node == node {
		out[i].Counter++
		out[i].Stamp = stamp
		return out
	}
	out = append(out, ClockEntry{})
	copy(out[i+1:], out[i:])
	out[i] = ClockEntry{Node: node, Counter: 1, Stamp: stamp}
	return out
}

// WithCounter returns a copy with node's counter set to counter.
func (v VClock) WithCounter(node string, counter uint64, stamp int64) VClock {
	out := v.Clone()
	i := sort.Search(len(out), func(i int) bool { return out[i].Node >= node })
	if i < len(out) && out[i].Node == node {
		out[i].Counter = counter
		out[i].Stamp = stamp
		return out
	}
	out = append(out, ClockEntry{})
	copy(out[i+1:], out[i:])
	out[i] = ClockEntry{Node: node, Counter: counter, Stamp: stamp}
	return out
}

// Compare determines the causal relationship between a and b by walking
// both sorted entry lists in step (a merge join).
func Compare(a, b VClock) Order {
	aBigger, bBigger := false, false
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j >= len(b) || (i < len(a) && a[i].Node < b[j].Node):
			// node only in a (b has 0)
			if a[i].Counter > 0 {
				aBigger = true
			}
			i++
		case i >= len(a) || b[j].Node < a[i].Node:
			if b[j].Counter > 0 {
				bBigger = true
			}
			j++
		default:
			if a[i].Counter > b[j].Counter {
				aBigger = true
			} else if a[i].Counter < b[j].Counter {
				bBigger = true
			}
			i++
			j++
		}
	}
	switch {
	case aBigger && bBigger:
		return Concurrent
	case aBigger:
		return After
	case bBigger:
		return Before
	}
	return Equal
}

// Descends reports whether a is equal to or newer than b.
func (v VClock) Descends(o VClock) bool {
	c := Compare(v, o)
	return c == Equal || c == After
}

// Merge returns the pointwise maximum of a and b. A client that read
// several siblings writes back with the merged clock, which descends from
// all of them and therefore supersedes them.
func Merge(a, b VClock) VClock {
	out := make(VClock, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case j >= len(b) || (i < len(a) && a[i].Node < b[j].Node):
			out = append(out, a[i])
			i++
		case i >= len(a) || b[j].Node < a[i].Node:
			out = append(out, b[j])
			j++
		default:
			e := a[i]
			if b[j].Counter > e.Counter {
				e.Counter = b[j].Counter
			}
			if b[j].Stamp > e.Stamp {
				e.Stamp = b[j].Stamp
			}
			out = append(out, e)
			i++
			j++
		}
	}
	return out
}

// Truncate drops the oldest entries once the clock exceeds max entries.
// The paper (§4.4) does this to bound clock size; it can in rare cases
// make a descendant look concurrent, which only costs an extra sibling.
func (v VClock) Truncate(max int) VClock {
	if max <= 0 || len(v) <= max {
		return v
	}
	out := v.Clone()
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stamp < out[j].Stamp })
	out = out[len(out)-max:]
	sort.Slice(out, func(i, j int) bool { return out[i].Node < out[j].Node })
	return out
}

// String renders the clock as "node-1:2, node-3:1".
func (v VClock) String() string {
	parts := make([]string, len(v))
	for i, e := range v {
		parts[i] = fmt.Sprintf("%s:%d", e.Node, e.Counter)
	}
	return strings.Join(parts, ", ")
}
