package echelon

import "sort"

// Background maintenance. In a real Dynamo deployment these run
// continuously on timers; the simulator runs them when asked (Tick), so
// that you can watch each mechanism repair the cluster step by step.

// GossipStats reports a round of membership gossip.
type GossipStats struct {
	Exchanges int
	Failed    int
}

// Gossip runs rounds of push-pull membership gossip: every live node
// picks a random peer from its view and the two merge views.
func (c *Cluster) Gossip(rounds int) GossipStats {
	var st GossipStats
	for r := 0; r < rounds; r++ {
		for _, id := range c.liveIDs() {
			n, err := c.node(id)
			if err != nil {
				continue
			}
			var peers []string
			for _, p := range n.View().Active() {
				if p != id {
					peers = append(peers, p)
				}
			}
			if len(peers) == 0 {
				continue
			}
			peer := peers[c.randIntn(len(peers))]
			resp, err := c.net.Call(id, peer, Request{Kind: MsgGossip, View: n.View()})
			if err != nil {
				st.Failed++
				continue
			}
			n.Handle(Request{Kind: MsgGossip, View: resp.View})
			st.Exchanges++
			c.Stats.GossipExchanges.Add(1)
		}
	}
	return st
}

// HandoffStats reports hinted-handoff delivery.
type HandoffStats struct {
	Delivered int // keys handed back to their owners
	Pending   int // keys still waiting because the owner is unreachable
}

// Handoff makes every live node try to return the writes it accepted on
// behalf of unreachable nodes (§4.6). Delivered hints are deleted.
func (c *Cluster) Handoff() HandoffStats {
	var st HandoffStats
	for _, id := range c.liveIDs() {
		n, err := c.node(id)
		if err != nil {
			continue
		}
		hints := n.snapshotHints()
		for _, target := range sortedKeys(hints) {
			items := hints[target]
			if _, err := c.net.Call(id, target, Request{Kind: MsgPutBatch, Items: items}); err != nil {
				st.Pending += len(items)
				continue
			}
			n.dropHints(target, items)
			st.Delivered += len(items)
			c.Stats.HintsDelivered.Add(int64(len(items)))
		}
	}
	return st
}

// AntiEntropyStats reports one full anti-entropy pass.
type AntiEntropyStats struct {
	PairsChecked   int // (range, replica pair) comparisons attempted
	InSync         int // pairs whose Merkle roots already matched
	Repaired       int // pairs that exchanged data
	Unreachable    int // pairs skipped because a replica was unreachable
	LeavesDiffered int // Merkle leaves that differed across all pairs
	HashesSent     int // Merkle hashes exchanged
	KeysSent       int // key versions transferred
}

// AntiEntropy compares every pair of replicas for every ring range using
// Merkle trees and exchanges whatever differs (§4.7). This is what heals
// divergence that hinted handoff cannot, e.g. after a disk is wiped or a
// hint holder dies before delivering.
func (c *Cluster) AntiEntropy() AntiEntropyStats {
	var st AntiEntropyStats
	ring, err := c.ReferenceRing()
	if err != nil {
		return st
	}
	for _, rg := range ring.Ranges() {
		reps := ring.ReplicasOf(rg, c.cfg.N)
		for i := 0; i < len(reps); i++ {
			for j := i + 1; j < len(reps); j++ {
				a, b := reps[i], reps[j]
				if !c.net.IsUp(a) {
					a, b = b, a
				}
				st.PairsChecked++
				if !c.net.IsUp(a) {
					st.Unreachable++
					continue
				}
				c.syncRange(a, b, rg, &st)
			}
		}
	}
	c.Stats.AntiEntropyKeys.Add(int64(st.KeysSent))
	return st
}

// syncRange reconciles one range between replicas a (initiator) and b.
func (c *Cluster) syncRange(a, b string, rg Range, st *AntiEntropyStats) {
	na, err := c.node(a)
	if err != nil {
		return
	}
	depth := c.cfg.MerkleDepth
	mine := na.merkle(rg, depth)

	// Level 0: compare roots. One hash settles the common case.
	resp, err := c.net.Call(a, b, Request{Kind: MsgMerkle, Range: rg, Depth: depth, Level: 0, Indices: []int{0}})
	if err != nil {
		st.Unreachable++
		return
	}
	st.HashesSent++
	if resp.Hashes[0] == mine.Root() {
		st.InSync++
		return
	}

	// Descend only into subtrees whose hashes differ.
	frontier := []int{0}
	for level := 1; level <= depth; level++ {
		var children []int
		for _, f := range frontier {
			children = append(children, 2*f, 2*f+1)
		}
		resp, err := c.net.Call(a, b, Request{Kind: MsgMerkle, Range: rg, Depth: depth, Level: level, Indices: children})
		if err != nil {
			st.Unreachable++
			return
		}
		st.HashesSent += len(children)
		frontier = frontier[:0]
		for i, idx := range children {
			if resp.Hashes[i] != mine.Levels[level][idx] {
				frontier = append(frontier, idx)
			}
		}
	}
	st.LeavesDiffered += len(frontier)

	// Exchange the keys in the differing leaves, in both directions.
	theirs, err := c.net.Call(a, b, Request{Kind: MsgFetch, Range: rg, Depth: depth, Indices: frontier})
	if err != nil {
		st.Unreachable++
		return
	}
	ours := na.itemsInLeaves(rg, depth, frontier)
	na.Handle(Request{Kind: MsgPutBatch, Items: theirs.Items})
	if len(ours) > 0 {
		if _, err := c.net.Call(a, b, Request{Kind: MsgPutBatch, Items: ours}); err != nil {
			st.Unreachable++
			return
		}
	}
	st.KeysSent += len(theirs.Items) + len(ours)
	st.Repaired++
}

// TickStats combines one round of every maintenance task.
type TickStats struct {
	Gossip      GossipStats
	Handoff     HandoffStats
	AntiEntropy AntiEntropyStats
}

// Tick runs one gossip round, one hinted-handoff pass and one
// anti-entropy pass.
func (c *Cluster) Tick() TickStats {
	c.Settle()
	return TickStats{Gossip: c.Gossip(1), Handoff: c.Handoff(), AntiEntropy: c.AntiEntropy()}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
