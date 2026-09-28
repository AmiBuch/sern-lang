package echelon

import (
	"sort"
	"sync"
)

// Node is one storage node. Its methods are only ever invoked through
// Handle, i.e. as if a message had arrived over the network.
type Node struct {
	ID     string
	vnodes int

	mu    sync.Mutex
	data  map[string][]Version            // the node's local store
	hints map[string]map[string][]Version // intended owner -> key -> versions
	view  View
	ring  *Ring
	ringF string

	// issued remembers, per key, the highest clock counter this node has
	// handed out as a coordinator, so it never issues the same one twice.
	issued map[string]uint64
}

func newNode(id string, vnodes int, view View) *Node {
	return &Node{
		ID:     id,
		vnodes: vnodes,
		data:   map[string][]Version{},
		hints:  map[string]map[string][]Version{},
		view:   view,
		issued: map[string]uint64{},
	}
}

// nextCounter returns the counter this node should put in its own clock
// entry for a new version of key: one past anything it has seen or issued.
func (n *Node) nextCounter(key string, fromContext uint64) uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	next := max(fromContext, n.issued[key])
	for _, v := range n.data[key] {
		if v.Dot.Node == n.ID {
			next = max(next, v.Dot.Counter)
		}
		next = max(next, v.Context.Get(n.ID))
	}
	next++
	n.issued[key] = next
	return next
}

// Ring returns the ring as this node currently believes it to be,
// rebuilt only when its membership view changes.
func (n *Node) Ring() *Ring {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ringLocked()
}

func (n *Node) ringLocked() *Ring {
	if f := n.view.fingerprint(); n.ring == nil || f != n.ringF {
		n.ring = NewRing(n.view.Active(), n.vnodes)
		n.ringF = f
	}
	return n.ring
}

// View returns a copy of the node's membership view.
func (n *Node) View() View {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.view.Clone()
}

// storeLocked merges incoming versions into the local store, keeping only
// versions not superseded by another (syntactic reconciliation).
func (n *Node) storeLocked(key string, vs []Version) {
	merged := Reconcile(append(cloneVersions(n.data[key]), cloneVersions(vs)...))
	n.data[key] = merged
}

// Handle processes one request. This is the node's entire "server".
func (n *Node) Handle(req Request) Response {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch req.Kind {
	case MsgPing:
		return Response{}

	case MsgPut:
		if req.HintFor != "" && req.HintFor != n.ID {
			// Sloppy quorum: we are standing in for a node that is down.
			// Keep the write aside, tagged with its real owner.
			if n.hints[req.HintFor] == nil {
				n.hints[req.HintFor] = map[string][]Version{}
			}
			h := n.hints[req.HintFor]
			h[req.Key] = Reconcile(append(h[req.Key], cloneVersions(req.Versions)...))
			return Response{}
		}
		n.storeLocked(req.Key, req.Versions)
		return Response{}

	case MsgPutBatch:
		for k, vs := range req.Items {
			n.storeLocked(k, vs)
		}
		return Response{}

	case MsgGet:
		return Response{Versions: cloneVersions(n.data[req.Key])}

	case MsgGossip:
		n.view.Merge(req.View)
		return Response{View: n.view.Clone()}

	case MsgMerkle:
		t := BuildMerkle(n.itemsInRangeLocked(req.Range), req.Depth)
		level := t.Levels[req.Level]
		out := make([][32]byte, len(req.Indices))
		for i, idx := range req.Indices {
			if idx >= 0 && idx < len(level) {
				out[i] = level[idx]
			}
		}
		return Response{Hashes: out}

	case MsgFetch:
		want := map[int]bool{}
		for _, i := range req.Indices {
			want[i] = true
		}
		out := map[string][]Version{}
		for k, vs := range n.itemsInRangeLocked(req.Range) {
			if want[leafIndex(k, req.Depth)] {
				out[k] = cloneVersions(vs)
			}
		}
		return Response{Items: out}
	}
	return Response{}
}

func (n *Node) itemsInRangeLocked(rg Range) map[string][]Version {
	out := map[string][]Version{}
	for k, vs := range n.data {
		if rg.Contains(HashKey(k)) {
			out[k] = vs
		}
	}
	return out
}

// ---- inspection helpers used by the cluster (not part of the protocol) ----

func (n *Node) snapshotHints() map[string]map[string][]Version {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string]map[string][]Version{}
	for target, m := range n.hints {
		out[target] = map[string][]Version{}
		for k, vs := range m {
			out[target][k] = cloneVersions(vs)
		}
	}
	return out
}

// dropHints removes delivered hints, unless newer ones arrived meanwhile.
func (n *Node) dropHints(target string, delivered map[string][]Version) {
	n.mu.Lock()
	defer n.mu.Unlock()
	h := n.hints[target]
	for k, vs := range delivered {
		if digest(h[k]) == digest(vs) {
			delete(h, k)
		}
	}
	if len(h) == 0 {
		delete(n.hints, target)
	}
}

func (n *Node) wipe() {
	n.mu.Lock()
	n.data = map[string][]Version{}
	n.hints = map[string]map[string][]Version{}
	n.mu.Unlock()
}

func (n *Node) counts() (keys, hints int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, m := range n.hints {
		hints += len(m)
	}
	return len(n.data), hints
}

func (n *Node) localVersions(key string) []Version {
	n.mu.Lock()
	defer n.mu.Unlock()
	return cloneVersions(n.data[key])
}

func (n *Node) hintsFor(key string) map[string][]Version {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := map[string][]Version{}
	for target, m := range n.hints {
		if vs, ok := m[key]; ok {
			out[target] = cloneVersions(vs)
		}
	}
	return out
}

func (n *Node) merkle(rg Range, depth int) *MerkleTree {
	n.mu.Lock()
	defer n.mu.Unlock()
	return BuildMerkle(n.itemsInRangeLocked(rg), depth)
}

func (n *Node) itemsInLeaves(rg Range, depth int, leaves []int) map[string][]Version {
	n.mu.Lock()
	defer n.mu.Unlock()
	want := map[int]bool{}
	for _, l := range leaves {
		want[l] = true
	}
	out := map[string][]Version{}
	for k, vs := range n.itemsInRangeLocked(rg) {
		if want[leafIndex(k, depth)] {
			out[k] = cloneVersions(vs)
		}
	}
	return out
}

func (n *Node) keys() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, 0, len(n.data))
	for k := range n.data {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
