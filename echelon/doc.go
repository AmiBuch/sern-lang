// Package echelon is a simulated Dynamo-style key-value store, following
// "Dynamo: Amazon's Highly Available Key-value Store" (DeCandia et al.,
// SOSP 2007). In the show, ECHELON is the surveillance system SERN uses to
// intercept D-Mails; here it is the distributed database built into Sern.
//
// Paper section → file:
//
//	§4.2 Partitioning (consistent hashing, virtual nodes)  ring.go
//	§4.3 Replication (preference lists)                     ring.go
//	§4.4 Data versioning (vector clocks)                    vclock.go, version.go
//	§4.5 get()/put() with R/W quorums                       coordinator.go
//	§4.6 Sloppy quorum + hinted handoff                     coordinator.go, maintenance.go
//	§4.7 Anti-entropy with Merkle trees                     merkle.go, maintenance.go
//	§4.8 Membership via gossip                              membership.go, maintenance.go
//	§5   Read repair                                        coordinator.go
//
// Everything below the Transport interface is written as if nodes were
// separate machines exchanging messages; the simulator delivers those
// messages in-process and can crash nodes or partition the network.
package echelon

import (
	"crypto/md5"
	"encoding/binary"
	"hash/fnv"
)

// HashKey places a key (or a virtual node) on the ring. Dynamo uses MD5 to
// produce a 128-bit identifier; we keep the top 64 bits.
func HashKey(s string) uint64 {
	sum := md5.Sum([]byte(s))
	return binary.BigEndian.Uint64(sum[:8])
}

// bucketHash picks a Merkle leaf for a key. It is independent of the ring
// hash so that keys spread evenly across leaves within a single range.
//
// Leaves are chosen by the hash's *top* bits. FNV-1a alone mixes the last
// bytes of a key into its high bits very weakly, so keys such as
// "worldline-41" and "worldline-42" all landed in the same leaf. The
// finalizer below (from MurmurHash3) spreads every input bit across all
// output bits.
func bucketHash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return mix64(h.Sum64())
}

func mix64(x uint64) uint64 {
	x ^= x >> 33
	x *= 0xff51afd7ed558ccd
	x ^= x >> 33
	x *= 0xc4ceb9fe1a85ec53
	x ^= x >> 33
	return x
}
