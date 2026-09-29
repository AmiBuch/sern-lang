package echelon

import (
	"fmt"
	"sort"
	"strings"
)

// Context is a causal context: the set of writes that a client, or a version,
// has actually seen.
//
// Clock holds the contiguous prefix per node — "everything node-3 coordinated
// for this key up to counter 2". Dots holds the individual writes that sit
// *past a gap*, which a version vector cannot express: a vector entry can say
// "everything up to 2", never "2 but not 1".
//
// That distinction is the whole point of the type. A dot is one event; a vector
// entry is a range. A client reading from a replica that missed a write sees a
// gap, and if the context it gets back cannot hold that gap, it claims to have
// seen the missing write — and its next put supersedes a version nobody ever
// read. docs/09 has the failure worked through.
//
// This is the Dotted Causal Container of Gonçalves et al., "Concise Server-Wide
// Causality Management for Eventually Consistent Data Stores" (DAIS 2015). That
// paper's Bitmapped Version Vector stores the loose dots as a base counter plus
// a bitmap; a sorted list is the simpler equivalent and is small for the same
// reason (see Dots).
//
// The zero Context is a valid empty context.
type Context struct {
	Clock VClock // contiguous prefix per node

	// Dots are writes seen past a gap, sorted by (Node, Counter). This stays
	// short in practice: a coordinator never skips a counter, so gaps only ever
	// come from *delivery*, and a dot folds into Clock the moment the write
	// below it arrives.
	Dots []ClockEntry
}

// Covers reports whether this context contains dot d — whether whoever held
// this context had actually seen that write. Under a plain vector clock this
// was a ">=" against the node's counter, which answers a different question:
// "is the counter high enough", not "did you see it".
func (c Context) Covers(d ClockEntry) bool {
	if d.Counter == 0 {
		return false
	}
	if c.Clock.Get(d.Node) >= d.Counter {
		return true
	}
	// Dots is expected to be empty or hold one or two entries, so a scan beats
	// a search.
	for _, x := range c.Dots {
		if x.Node == d.Node && x.Counter == d.Counter {
			return true
		}
	}
	return false
}

// Gapped reports whether the context holds any write past a gap.
func (c Context) Gapped() bool { return len(c.Dots) > 0 }

// Add returns the context extended with one dot. A zero dot adds nothing.
func (c Context) Add(d ClockEntry) Context {
	if d.Counter == 0 {
		return c
	}
	return Context{Clock: c.Clock, Dots: append(append([]ClockEntry(nil), c.Dots...), d)}.normalized()
}

// MergeContexts returns the union of two causal contexts: everything either
// side had seen. A read over several siblings hands the client this.
//
// Merging can *close* a gap, which is why it normalizes: one side's loose dot
// node-3:2 becomes ordinary prefix once the other side supplies node-3:1.
func MergeContexts(a, b Context) Context {
	clock := Merge(a.Clock, b.Clock)
	if len(a.Dots) == 0 && len(b.Dots) == 0 {
		return Context{Clock: clock}
	}
	return Context{Clock: clock, Dots: append(append([]ClockEntry(nil), a.Dots...), b.Dots...)}.normalized()
}

// Max is the highest counter this context holds for node, whether or not every
// dot below it is present. The coordinator uses it to pick a counter it has
// never issued (Node.nextCounter).
func (c Context) Max(node string) uint64 {
	m := c.Clock.Get(node)
	for _, d := range c.Dots {
		if d.Node == node && d.Counter > m {
			m = d.Counter
		}
	}
	return m
}

// Includes reports whether this context contains everything o does.
//
// Used by the tests to check that a context handed to a client never claims a
// write the responding replicas did not actually hold — the invariant this type
// exists to keep.
func (c Context) Includes(o Context) bool {
	for _, e := range o.Clock {
		// Where o's prefix reaches further than ours, each dot in between has
		// to be covered individually; ours may hold them as loose dots. The
		// loop runs over the difference only.
		for i := c.Clock.Get(e.Node) + 1; i <= e.Counter; i++ {
			if !c.Covers(ClockEntry{Node: e.Node, Counter: i}) {
				return false
			}
		}
	}
	for _, d := range o.Dots {
		if !c.Covers(d) {
			return false
		}
	}
	return true
}

// Truncate bounds the context to max entries in total, dropping the oldest by
// Stamp, the same cap VClock.Truncate applies to a plain clock (§4.4).
//
// Dropping either a prefix entry or a loose dot only ever *lowers* coverage, so
// the cost is a version that survives as a sibling when it could have been
// superseded — never a version dropped when it should have survived. Truncation
// cannot lose a write in either direction.
func (c Context) Truncate(max int) Context {
	if max <= 0 || len(c.Clock)+len(c.Dots) <= max {
		return c
	}
	type tagged struct {
		e   ClockEntry
		dot bool
	}
	all := make([]tagged, 0, len(c.Clock)+len(c.Dots))
	for _, e := range c.Clock {
		all = append(all, tagged{e: e})
	}
	for _, d := range c.Dots {
		all = append(all, tagged{e: d, dot: true})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].e.Stamp < all[j].e.Stamp })
	all = all[len(all)-max:]
	var out Context
	for _, t := range all {
		if t.dot {
			out.Dots = append(out.Dots, t.e)
		} else {
			out.Clock = append(out.Clock, t.e)
		}
	}
	// Clock must stay sorted by node: Get binary-searches it.
	sort.Slice(out.Clock, func(i, j int) bool { return out.Clock[i].Node < out.Clock[j].Node })
	sortDots(out.Dots)
	// Folding can only trade a dot for a prefix entry, so the total holds.
	return out.normalized()
}

// Clone returns an independent copy.
func (c Context) Clone() Context {
	out := Context{Clock: c.Clock.Clone()}
	if len(c.Dots) > 0 {
		out.Dots = append([]ClockEntry(nil), c.Dots...)
	}
	return out
}

// String renders the context as "node-1:1, +node-3:4". A "+" marks a dot seen
// past a gap — node-3:4 without node-3:3. A context with no gaps prints exactly
// as a plain vector clock does, which is why the examples' output is unchanged.
func (c Context) String() string {
	parts := make([]string, 0, len(c.Clock)+len(c.Dots))
	for _, e := range c.Clock {
		parts = append(parts, fmt.Sprintf("%s:%d", e.Node, e.Counter))
	}
	for _, d := range c.Dots {
		parts = append(parts, fmt.Sprintf("+%s:%d", d.Node, d.Counter))
	}
	return strings.Join(parts, ", ")
}

// normalized restores the invariant that every dot lies past a gap
// (Counter > Clock.Get(Node)+1), with no duplicates.
//
// A dot contiguous with the prefix is folded into Clock. That is exact — "up to
// 1" plus the dot 2 covers precisely the set "up to 2" — so normalizing never
// widens the context, and it keeps Dots empty whenever nothing is missing.
func (c Context) normalized() Context {
	if len(c.Dots) == 0 {
		return Context{Clock: c.Clock}
	}
	dots := append([]ClockEntry(nil), c.Dots...)
	sortDots(dots)
	clock := c.Clock.Clone()
	var loose []ClockEntry
	// Ascending order makes one pass enough: once a dot is past a gap, every
	// later dot for that node is further past it still.
	for _, d := range dots {
		if n := len(loose); n > 0 && loose[n-1].Node == d.Node && loose[n-1].Counter == d.Counter {
			continue // repeat of a dot we already kept
		}
		switch {
		case d.Counter <= clock.Get(d.Node):
			// already covered by the prefix (or a repeat of one we folded in)
		case d.Counter == clock.Get(d.Node)+1:
			clock = clock.WithCounter(d.Node, d.Counter, max(clock.stampOf(d.Node), d.Stamp))
		default:
			loose = append(loose, d)
		}
	}
	return Context{Clock: clock, Dots: loose}
}

func sortDots(ds []ClockEntry) {
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].Node != ds[j].Node {
			return ds[i].Node < ds[j].Node
		}
		return ds[i].Counter < ds[j].Counter
	})
}
