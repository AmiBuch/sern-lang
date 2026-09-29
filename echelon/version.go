package echelon

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strconv"
)

// Version is one stored version of a key: an opaque value, the dot that
// uniquely identifies the write which produced it, and the causal context
// that write descended from. Deletes are stored as tombstones so that the
// delete itself can win against older versions during reconciliation.
//
// Splitting the dot out of the context is what makes this a *dotted* version
// vector (Preguiça et al. 2010). A plain vector clock folds the coordinator's
// new counter into the clock itself, so two writes made from the same context
// through the same coordinator come out strictly ordered and the older one is
// silently dropped. Keeping the dot separate means neither write's context
// covers the other's dot, so both survive as siblings.
//
// The context is a Context, not a VClock, for the second half of the same
// problem: a vector clock cannot record "I saw node-3:2 but not node-3:1", so
// flattening a client's view into one would have it claim writes it never saw.
// See Context and docs/09.
type Version struct {
	Value   []byte
	Dot     ClockEntry // uniquely identifies this write
	Context Context    // what this write descended from; excludes Dot
	Deleted bool
}

// FullContext is the version's complete causal history: its context plus its
// own dot. This is what clients see and hand back on the next put.
func (v Version) FullContext() Context {
	return v.Context.Add(v.Dot)
}

// dominates reports whether a was written in knowledge of b, i.e. whether
// a's causal context already contains b's dot. This is the whole of the DVV
// ordering: everything else follows from it.
//
// A version with no dot is never dominated. Hand-built versions and anything
// predating dots therefore show up as an extra sibling rather than vanishing.
func dominates(a, b Version) bool {
	return b.Dot.Counter > 0 && a.Context.Covers(b.Dot)
}

// Reconcile performs Dynamo's *syntactic* reconciliation: any version whose
// dot is already covered by another's context is dropped, and repeats of the
// same write are collapsed. What remains is a set of mutually concurrent
// siblings. Semantic reconciliation (merging siblings) is left to the
// application.
//
// Domination cannot cycle, so this never empties a non-empty set: a dot is
// minted strictly after the context it is paired with, so two versions cannot
// each have seen the other.
func Reconcile(vs []Version) []Version {
	vs = coalesce(vs)
	var out []Version
	for i, v := range vs {
		dominated := false
		for j, w := range vs {
			if i != j && dominates(w, v) {
				dominated = true
				break
			}
		}
		if !dominated {
			out = append(out, v)
		}
	}
	sortVersions(out)
	return out
}

// coalesce collapses repeats of the same write into one version whose context
// is the union of the copies' contexts.
//
// Two replicas can hold one write with different contexts, once ClockLimit
// truncation has run on one of them and not the other. Picking a copy by its
// position in the slice would then make Reconcile's result depend on argument
// order: two replicas would reconcile identical inputs to different sets,
// digest would disagree, and anti-entropy would repair them at each other
// forever. Merging is order-independent, and keeps the better-informed context.
func coalesce(vs []Version) []Version {
	out := make([]Version, 0, len(vs))
	at := make(map[string]int, len(vs))
	for _, v := range vs {
		k := v.identity()
		if i, seen := at[k]; seen {
			out[i].Context = MergeContexts(out[i].Context, v.Context)
			if v.Dot.Stamp > out[i].Dot.Stamp {
				out[i].Dot.Stamp = v.Dot.Stamp
			}
			continue
		}
		at[k] = len(out)
		out = append(out, v)
	}
	return out
}

// identity names the write a version came from. A dot is unique per write per
// key, so it is the whole identity; Stamp is excluded because it is only a
// truncation hint and two copies of one write may carry different ones.
// Dotless versions fall back to their contents, which at least collapses exact
// duplicates.
func (v Version) identity() string {
	if v.Dot.Counter > 0 {
		return "d\x00" + v.Dot.Node + "\x00" + strconv.FormatUint(v.Dot.Counter, 10)
	}
	return "v\x00" + string(v.Value) + "\x00" + strconv.FormatBool(v.Deleted) + "\x00" + v.Context.String()
}

// sortVersions gives siblings a deterministic order. Siblings from one
// coordinator share a context, so the dot breaks the tie.
func sortVersions(vs []Version) {
	sort.Slice(vs, func(i, j int) bool {
		li, lj := vs[i].FullContext().String(), vs[j].FullContext().String()
		if li != lj {
			return li < lj
		}
		if vs[i].Dot.Node != vs[j].Dot.Node {
			return vs[i].Dot.Node < vs[j].Dot.Node
		}
		return vs[i].Dot.Counter < vs[j].Dot.Counter
	})
}

// digest hashes a set of versions (used for Merkle leaves and to detect
// replicas that need read repair).
func digest(vs []Version) [32]byte {
	h := sha256.New()
	var tmp [8]byte
	for _, v := range vs {
		binary.BigEndian.PutUint64(tmp[:], uint64(len(v.Value)))
		h.Write(tmp[:])
		h.Write(v.Value)
		// The dot must be hashed too: two sibling sets that differ only by
		// dot would otherwise digest equal, and read repair and anti-entropy
		// would call divergent replicas in sync forever.
		h.Write([]byte(v.Dot.Node))
		binary.BigEndian.PutUint64(tmp[:], v.Dot.Counter)
		h.Write(tmp[:])
		for _, e := range v.Context.Clock {
			h.Write([]byte(e.Node))
			binary.BigEndian.PutUint64(tmp[:], e.Counter)
			h.Write(tmp[:])
		}
		// A separator before the loose dots, so "node-3 up to 2" and "node-3:2
		// past a gap" cannot hash alike. They are different states, and two
		// replicas holding one each must not look in sync.
		h.Write([]byte{0xff})
		for _, d := range v.Context.Dots {
			h.Write([]byte(d.Node))
			binary.BigEndian.PutUint64(tmp[:], d.Counter)
			h.Write(tmp[:])
		}
		if v.Deleted {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func cloneVersions(vs []Version) []Version {
	out := make([]Version, len(vs))
	for i, v := range vs {
		out[i] = Version{Value: append([]byte(nil), v.Value...), Dot: v.Dot, Context: v.Context.Clone(), Deleted: v.Deleted}
	}
	return out
}
