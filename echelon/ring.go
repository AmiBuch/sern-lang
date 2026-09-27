package echelon

import (
	"fmt"
	"sort"
)

// Token is one virtual node: a position on the ring owned by a physical node.
type Token struct {
	Pos   uint64
	Node  string
	VNode int
}

// Ring is a consistent-hashing ring (§4.2). The hash space [0, 2^64) is a
// circle; each physical node owns several tokens (virtual nodes) on it. A
// key is stored on the first node found walking clockwise from the key's
// hash, and replicated on the next distinct nodes after that (§4.3).
//
// Virtual nodes spread each machine's share of the ring into many small
// arcs, so load is balanced and a failed node's load is spread over many
// others instead of landing on one neighbour.
type Ring struct {
	Tokens []Token
	Nodes  []string
	VNodes int
}

// NewRing builds a ring for the given members. Token positions are derived
// from hash("node#vnode-i"), so every node that knows the same membership
// builds the identical ring without coordination.
func NewRing(nodes []string, vnodes int) *Ring {
	r := &Ring{Nodes: append([]string(nil), nodes...), VNodes: vnodes}
	sort.Strings(r.Nodes)
	for _, n := range r.Nodes {
		for i := 0; i < vnodes; i++ {
			r.Tokens = append(r.Tokens, Token{Pos: HashKey(fmt.Sprintf("%s#vnode-%d", n, i)), Node: n, VNode: i})
		}
	}
	sort.Slice(r.Tokens, func(i, j int) bool {
		if r.Tokens[i].Pos != r.Tokens[j].Pos {
			return r.Tokens[i].Pos < r.Tokens[j].Pos
		}
		return r.Tokens[i].Node < r.Tokens[j].Node
	})
	return r
}

// successor returns the index of the first token at or after pos,
// wrapping around the end of the ring.
func (r *Ring) successor(pos uint64) int {
	i := sort.Search(len(r.Tokens), func(i int) bool { return r.Tokens[i].Pos >= pos })
	if i == len(r.Tokens) {
		i = 0
	}
	return i
}

// walkFrom lists distinct physical nodes clockwise from token i.
func (r *Ring) walkFrom(i int) []string {
	seen := map[string]bool{}
	var out []string
	for k := 0; k < len(r.Tokens) && len(out) < len(r.Nodes); k++ {
		t := r.Tokens[(i+k)%len(r.Tokens)]
		if !seen[t.Node] {
			seen[t.Node] = true
			out = append(out, t.Node)
		}
	}
	return out
}

// Walk returns every physical node in ring order starting from key's
// position. The first N entries are the key's preference list; the rest
// are the fallbacks used by the sloppy quorum.
func (r *Ring) Walk(key string) []string {
	if len(r.Tokens) == 0 {
		return nil
	}
	return r.walkFrom(r.successor(HashKey(key)))
}

// PreferenceList returns the N nodes responsible for key.
func (r *Ring) PreferenceList(key string, n int) []string {
	w := r.Walk(key)
	if n < len(w) {
		w = w[:n]
	}
	return w
}

// Range is the arc (Start, End] of the ring ending at token Index.
// Every key in the arc has the same preference list.
type Range struct {
	Start, End uint64
	Index      int
}

// Contains reports whether pos falls in the arc, handling wrap-around.
func (rg Range) Contains(pos uint64) bool {
	switch {
	case rg.Start < rg.End:
		return pos > rg.Start && pos <= rg.End
	case rg.Start > rg.End: // arc wraps past zero
		return pos > rg.Start || pos <= rg.End
	default: // a single token owns the whole ring
		return true
	}
}

func (rg Range) String() string { return fmt.Sprintf("(%016x, %016x]", rg.Start, rg.End) }

// Ranges lists every arc of the ring.
func (r *Ring) Ranges() []Range {
	out := make([]Range, len(r.Tokens))
	for i, t := range r.Tokens {
		prev := r.Tokens[(i-1+len(r.Tokens))%len(r.Tokens)]
		out[i] = Range{Start: prev.Pos, End: t.Pos, Index: i}
	}
	return out
}

// ReplicasOf returns the N nodes that store the keys of a range.
func (r *Ring) ReplicasOf(rg Range, n int) []string {
	w := r.walkFrom(rg.Index)
	if n < len(w) {
		w = w[:n]
	}
	return w
}

// Ownership reports the fraction of the hash space for which each node is
// the first (coordinator) replica. With enough virtual nodes these even out.
func (r *Ring) Ownership() map[string]float64 {
	out := map[string]float64{}
	for _, rg := range r.Ranges() {
		var width uint64
		if len(r.Tokens) == 1 {
			out[r.Tokens[0].Node] = 1
			return out
		}
		width = rg.End - rg.Start // unsigned arithmetic wraps correctly
		out[r.Tokens[rg.Index].Node] += float64(width) / float64(^uint64(0))
	}
	return out
}
