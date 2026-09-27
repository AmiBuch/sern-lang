package echelon

import (
	"crypto/sha256"
	"encoding/binary"
	"sort"
)

// Version is one stored version of a key: an opaque value plus the vector
// clock describing its history. Deletes are stored as tombstones so that
// the delete itself can win against older versions during reconciliation.
type Version struct {
	Value   []byte
	Clock   VClock
	Deleted bool
}

// Reconcile performs Dynamo's *syntactic* reconciliation: any version whose
// clock is strictly older than another's is dropped, and exact duplicates
// are collapsed. What remains is a set of mutually concurrent siblings.
// Semantic reconciliation (merging siblings) is left to the application.
func Reconcile(vs []Version) []Version {
	var out []Version
	for i, v := range vs {
		dominated := false
		for j, w := range vs {
			if i == j {
				continue
			}
			switch Compare(v.Clock, w.Clock) {
			case Before:
				dominated = true
			case Equal:
				if j < i { // keep the first of identical clocks
					dominated = true
				}
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

// sortVersions gives siblings a deterministic order.
func sortVersions(vs []Version) {
	sort.Slice(vs, func(i, j int) bool { return vs[i].Clock.String() < vs[j].Clock.String() })
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
		for _, e := range v.Clock {
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
		out[i] = Version{Value: append([]byte(nil), v.Value...), Clock: v.Clock.Clone(), Deleted: v.Deleted}
	}
	return out
}
