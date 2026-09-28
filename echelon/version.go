package echelon

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
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
type Version struct {
	Value   []byte
	Dot     ClockEntry // uniquely identifies this write
	Context VClock     // what this write descended from; excludes Dot
	Deleted bool
}

// FullClock is the version's complete causal history: its context plus its
// own dot. This is what clients see and hand back on the next put.
func (v Version) FullClock() VClock {
	if v.Dot.Counter == 0 {
		return v.Context
	}
	return Merge(v.Context, VClock{v.Dot})
}

// dominates reports whether a was written in knowledge of b, i.e. whether
// a's causal context already covers b's dot. This is the whole of the DVV
// ordering: everything else follows from it.
//
// A version with no dot is never dominated. Hand-built versions and anything
// predating dots therefore show up as an extra sibling rather than vanishing.
func dominates(a, b Version) bool {
	return b.Dot.Counter > 0 && a.Context.Get(b.Dot.Node) >= b.Dot.Counter
}

// Reconcile performs Dynamo's *syntactic* reconciliation: any version whose
// dot is already covered by another's context is dropped, and repeats of the
// same write are collapsed. What remains is a set of mutually concurrent
// siblings. Semantic reconciliation (merging siblings) is left to the
// application.
func Reconcile(vs []Version) []Version {
	var out []Version
	for i, v := range vs {
		dominated := false
		for j, w := range vs {
			if i == j {
				continue
			}
			if dominates(w, v) {
				dominated = true
			} else if w.Dot.Counter > 0 && w.Dot == v.Dot && j < i {
				dominated = true // the same write, seen twice
			}
			if dominated {
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

// sortVersions gives siblings a deterministic order. Siblings from one
// coordinator share a context, so the dot breaks the tie.
func sortVersions(vs []Version) {
	sort.Slice(vs, func(i, j int) bool {
		li, lj := vs[i].FullClock().String(), vs[j].FullClock().String()
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
		for _, e := range v.Context {
			h.Write([]byte(e.Node))
			binary.BigEndian.PutUint64(tmp[:], e.Counter)
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
