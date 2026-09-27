package echelon

import (
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// MsgKind identifies an RPC between nodes.
type MsgKind int

const (
	MsgPing     MsgKind = iota // liveness check
	MsgPut                     // store versions (optionally as a hint for another node)
	MsgGet                     // read versions of a key
	MsgPutBatch                // store many keys (handoff, anti-entropy, repair)
	MsgGossip                  // exchange membership views (push-pull)
	MsgMerkle                  // return Merkle hashes for a range at one level
	MsgFetch                   // return the items in some Merkle leaves of a range
)

// Request is the single message type nodes exchange. Only the fields
// relevant to Kind are set. Keeping it a plain struct with exported fields
// means phase 3 can put it on a real network with encoding/gob unchanged.
type Request struct {
	Kind     MsgKind
	From     string
	Key      string
	Versions []Version
	HintFor  string // MsgPut: the intended owner, when sent to a fallback
	Items    map[string][]Version
	View     View
	Range    Range
	Depth    int
	Level    int
	Indices  []int
}

// Response carries a node's reply.
type Response struct {
	Versions []Version
	Items    map[string][]Version
	View     View
	Hashes   [][32]byte
}

// Transport delivers requests between nodes. The simulator implements it
// in-process; a TCP implementation can replace it without touching the
// coordinator or node logic.
type Transport interface {
	Call(from, to string, req Request) (Response, error)
}

// ErrUnreachable means the destination is down or partitioned away.
// Dynamo detects this with timeouts; the simulator knows it instantly.
var ErrUnreachable = errors.New("node unreachable")

// SimNetwork is an in-process network with failure injection.
type SimNetwork struct {
	mu     sync.RWMutex
	nodes  map[string]*Node
	down   map[string]bool
	group  map[string]int // partition group per node; empty = fully connected
	latMin time.Duration
	latMax time.Duration
	rngMu  sync.Mutex
	rng    *rand.Rand

	Calls    atomic.Int64
	Failures atomic.Int64
}

// NewSimNetwork creates an empty network.
func NewSimNetwork(seed int64, latMin, latMax time.Duration) *SimNetwork {
	return &SimNetwork{
		nodes:  map[string]*Node{},
		down:   map[string]bool{},
		group:  map[string]int{},
		latMin: latMin,
		latMax: latMax,
		rng:    rand.New(rand.NewSource(seed ^ 0x5e125e12)),
	}
}

func (s *SimNetwork) attach(n *Node) {
	s.mu.Lock()
	s.nodes[n.ID] = n
	s.mu.Unlock()
}

func (s *SimNetwork) detach(id string) {
	s.mu.Lock()
	delete(s.nodes, id)
	delete(s.down, id)
	delete(s.group, id)
	s.mu.Unlock()
}

// SetDown crashes or revives a node. A crashed node keeps its disk.
func (s *SimNetwork) SetDown(id string, down bool) {
	s.mu.Lock()
	s.down[id] = down
	s.mu.Unlock()
}

// IsUp reports whether a node exists and is running.
func (s *SimNetwork) IsUp(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.nodes[id]
	return ok && !s.down[id]
}

// Partition splits the network into groups that cannot talk to each other.
// Nodes not listed form one extra group together.
func (s *SimNetwork) Partition(groups [][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.group = map[string]int{}
	for i, g := range groups {
		for _, n := range g {
			s.group[n] = i + 1
		}
	}
}

// Heal removes all partitions.
func (s *SimNetwork) Heal() {
	s.mu.Lock()
	s.group = map[string]int{}
	s.mu.Unlock()
}

func (s *SimNetwork) reachable(from, to string) bool {
	if s.down[from] || s.down[to] {
		return false
	}
	if _, ok := s.nodes[to]; !ok {
		return false
	}
	if len(s.group) == 0 || from == to {
		return true
	}
	return s.group[from] == s.group[to]
}

func (s *SimNetwork) delay() {
	if s.latMax <= 0 {
		return
	}
	d := s.latMin
	if s.latMax > s.latMin {
		s.rngMu.Lock()
		d += time.Duration(s.rng.Int63n(int64(s.latMax - s.latMin)))
		s.rngMu.Unlock()
	}
	time.Sleep(d)
}

// Call implements Transport.
func (s *SimNetwork) Call(from, to string, req Request) (Response, error) {
	s.Calls.Add(1)
	s.mu.RLock()
	ok := s.reachable(from, to)
	dst := s.nodes[to]
	s.mu.RUnlock()
	s.delay()
	if !ok {
		s.Failures.Add(1)
		return Response{}, ErrUnreachable
	}
	req.From = from
	return dst.Handle(req), nil
}
