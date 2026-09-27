package echelon

import (
	"crypto/sha256"
	"sort"
)

// MerkleTree is a hash tree over the keys of one ring range (§4.7).
//
// Leaves are buckets of keys; each internal node is the hash of its two
// children. Two replicas compare roots: if equal, the whole range is in
// sync after exchanging a single hash. If not, they descend only into the
// children whose hashes differ, so the data transferred is proportional to
// the amount of divergence rather than the size of the range.
//
// Levels[0] holds the root, Levels[d] holds 2^d hashes, and
// Levels[Depth] holds the leaves.
type MerkleTree struct {
	Depth  int
	Levels [][][32]byte
}

// leafIndex picks the bucket for a key: the top Depth bits of its hash.
func leafIndex(key string, depth int) int {
	if depth == 0 {
		return 0
	}
	return int(bucketHash(key) >> (64 - uint(depth)))
}

// BuildMerkle builds a tree from a set of keys and their versions.
func BuildMerkle(items map[string][]Version, depth int) *MerkleTree {
	leaves := 1 << depth
	buckets := make([][]string, leaves)
	for k := range items {
		i := leafIndex(k, depth)
		buckets[i] = append(buckets[i], k)
	}
	t := &MerkleTree{Depth: depth, Levels: make([][][32]byte, depth+1)}
	leafLevel := make([][32]byte, leaves)
	for i, keys := range buckets {
		if len(keys) == 0 {
			continue // empty bucket: zero hash
		}
		sort.Strings(keys)
		h := sha256.New()
		for _, k := range keys {
			h.Write([]byte(k))
			d := digest(items[k])
			h.Write(d[:])
		}
		copy(leafLevel[i][:], h.Sum(nil))
	}
	t.Levels[depth] = leafLevel
	for d := depth - 1; d >= 0; d-- {
		child := t.Levels[d+1]
		level := make([][32]byte, 1<<d)
		for i := range level {
			l, r := child[2*i], child[2*i+1]
			if l == ([32]byte{}) && r == ([32]byte{}) {
				continue
			}
			level[i] = sha256.Sum256(append(l[:], r[:]...))
		}
		t.Levels[d] = level
	}
	return t
}

// Root returns the root hash.
func (t *MerkleTree) Root() [32]byte { return t.Levels[0][0] }
