package echelon

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Config describes a simulated cluster.
type Config struct {
	Nodes  int // number of storage nodes
	N      int // replicas per key
	R      int // read quorum
	W      int // write quorum
	VNodes int // virtual nodes (tokens) per physical node
	Seed   int64

	ClockLimit  int // max vector-clock entries before truncation (paper: 10)
	MerkleDepth int // Merkle tree depth; 2^depth leaves per range

	// LatencyMin/Max add a random delay to every message.
	LatencyMin, LatencyMax time.Duration

	// Sync makes every get/put wait for all replica traffic (including read
	// repair) before returning. Results are then deterministic, which is
	// what you want for teaching and tests. With Sync off, operations
	// return as soon as R or W replicas answer, exactly like Dynamo, and
	// the stragglers finish in the background.
	Sync bool
}

// DefaultConfig mirrors the paper's common (N, R, W) = (3, 2, 2).
func DefaultConfig() Config {
	return Config{Nodes: 5, N: 3, R: 2, W: 2, VNodes: 8, Seed: 1, ClockLimit: 10, MerkleDepth: 4, Sync: true}
}

// Validate checks that the configuration is usable.
func (c Config) Validate() error {
	switch {
	case c.Nodes < 1:
		return errors.New("a cluster needs at least 1 node")
	case c.N < 1 || c.N > c.Nodes:
		return fmt.Errorf("n must be between 1 and nodes (%d), got %d", c.Nodes, c.N)
	case c.R < 1 || c.R > c.N:
		return fmt.Errorf("r must be between 1 and n (%d), got %d", c.N, c.R)
	case c.W < 1 || c.W > c.N:
		return fmt.Errorf("w must be between 1 and n (%d), got %d", c.N, c.W)
	case c.VNodes < 1 || c.VNodes > 1024:
		return fmt.Errorf("vnodes must be between 1 and 1024, got %d", c.VNodes)
	case c.MerkleDepth < 0 || c.MerkleDepth > 16:
		return fmt.Errorf("merkle depth must be between 0 and 16, got %d", c.MerkleDepth)
	case c.LatencyMax < c.LatencyMin:
		return errors.New("latency max must be >= latency min")
	}
	return nil
}

// Stats are cluster-wide counters.
type Stats struct {
	Puts, Gets       atomic.Int64
	QuorumFailures   atomic.Int64
	HintsStored      atomic.Int64
	HintsDelivered   atomic.Int64
	ReadRepairs      atomic.Int64
	AntiEntropyKeys  atomic.Int64
	GossipExchanges  atomic.Int64
	CoordinatorHops  atomic.Int64 // requests forwarded to a top-N node
	SloppyCoordinate atomic.Int64 // requests coordinated outside the top N
}

// QuorumError reports that too few replicas answered.
type QuorumError struct {
	Op        string
	Need, Got int
}

func (e *QuorumError) Error() string {
	return fmt.Sprintf("%s failed: needed %d replicas, only %d answered", e.Op, e.Need, e.Got)
}

// Cluster is a simulated Dynamo deployment.
type Cluster struct {
	cfg   Config
	mu    sync.Mutex // guards nodes, order and rng
	nodes map[string]*Node
	order []string
	rng   *rand.Rand
	net   *SimNetwork
	stamp atomic.Int64
	bg    sync.WaitGroup
	Stats Stats
}

// NewCluster creates nodes node-1 .. node-N that all know each other.
func NewCluster(cfg Config) (*Cluster, error) {
	if cfg.ClockLimit == 0 {
		cfg.ClockLimit = 10
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	c := &Cluster{
		cfg:   cfg,
		nodes: map[string]*Node{},
		rng:   rand.New(rand.NewSource(cfg.Seed)),
		net:   NewSimNetwork(cfg.Seed, cfg.LatencyMin, cfg.LatencyMax),
	}
	view := View{}
	for i := 1; i <= cfg.Nodes; i++ {
		id := fmt.Sprintf("node-%d", i)
		view[id] = Member{Node: id, Status: Joined, Version: 1}
		c.order = append(c.order, id)
	}
	for _, id := range c.order {
		n := newNode(id, cfg.VNodes, view.Clone())
		c.nodes[id] = n
		c.net.attach(n)
	}
	return c, nil
}

// Config returns the cluster configuration.
func (c *Cluster) Config() Config { return c.cfg }

// Network exposes the simulated network (for message statistics).
func (c *Cluster) Network() *SimNetwork { return c.net }

func (c *Cluster) node(id string) (*Node, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.nodes[id]
	if !ok {
		return nil, fmt.Errorf("no node named %q", id)
	}
	return n, nil
}

// NodeIDs lists nodes in creation order.
func (c *Cluster) NodeIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.order...)
}

func (c *Cluster) liveIDs() []string {
	var out []string
	for _, id := range c.NodeIDs() {
		if c.net.IsUp(id) {
			out = append(out, id)
		}
	}
	return out
}

func (c *Cluster) randIntn(n int) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rng.Intn(n)
}

// Settle waits for all background replica traffic to finish.
func (c *Cluster) Settle() { c.bg.Wait() }

// ReferenceRing is the ring as seen by the first live node. Once gossip
// has converged every node sees this same ring.
func (c *Cluster) ReferenceRing() (*Ring, error) {
	live := c.liveIDs()
	if len(live) == 0 {
		return nil, errors.New("every node is down")
	}
	n, err := c.node(live[0])
	if err != nil {
		return nil, err
	}
	return n.Ring(), nil
}

// PreferenceList returns the N replicas for key.
func (c *Cluster) PreferenceList(key string) ([]string, error) {
	r, err := c.ReferenceRing()
	if err != nil {
		return nil, err
	}
	return r.PreferenceList(key, c.cfg.N), nil
}

// ---- failure injection and membership ----

// Crash stops a node. Its disk (data and hints) survives.
func (c *Cluster) Crash(id string) error {
	if _, err := c.node(id); err != nil {
		return err
	}
	c.net.SetDown(id, true)
	return nil
}

// Revive restarts a crashed node.
func (c *Cluster) Revive(id string) error {
	if _, err := c.node(id); err != nil {
		return err
	}
	c.net.SetDown(id, false)
	return nil
}

// Wipe erases a node's disk, simulating a replaced machine.
func (c *Cluster) Wipe(id string) error {
	n, err := c.node(id)
	if err != nil {
		return err
	}
	n.wipe()
	return nil
}

// Partition splits the network into isolated groups.
func (c *Cluster) Partition(groups [][]string) error {
	for _, g := range groups {
		for _, id := range g {
			if _, err := c.node(id); err != nil {
				return err
			}
		}
	}
	c.net.Partition(groups)
	return nil
}

// Heal reconnects every partition.
func (c *Cluster) Heal() { c.net.Heal() }

// Join adds a new node (§4.9). It contacts one live seed; the rest of the
// cluster learns about it through gossip.
func (c *Cluster) Join(id string) error {
	c.mu.Lock()
	if _, ok := c.nodes[id]; ok {
		c.mu.Unlock()
		return fmt.Errorf("node %q already exists", id)
	}
	n := newNode(id, c.cfg.VNodes, View{id: {Node: id, Status: Joined, Version: 1}})
	c.nodes[id] = n
	c.order = append(c.order, id)
	c.mu.Unlock()
	c.net.attach(n)

	for _, seed := range c.liveIDs() {
		if seed == id {
			continue
		}
		resp, err := c.net.Call(id, seed, Request{Kind: MsgGossip, View: n.View()})
		if err == nil {
			n.Handle(Request{Kind: MsgGossip, View: resp.View})
			return nil
		}
	}
	return errors.New("joined, but no seed node was reachable")
}

// Leave removes a node permanently. It announces its departure to one
// peer, then disappears; gossip spreads the news.
func (c *Cluster) Leave(id string) error {
	n, err := c.node(id)
	if err != nil {
		return err
	}
	n.mu.Lock()
	cur := n.view[id]
	n.view[id] = Member{Node: id, Status: Left, Version: cur.Version + 1}
	n.mu.Unlock()
	for _, peer := range c.liveIDs() {
		if peer == id {
			continue
		}
		if _, err := c.net.Call(id, peer, Request{Kind: MsgGossip, View: n.View()}); err == nil {
			break
		}
	}
	c.net.detach(id)
	c.mu.Lock()
	delete(c.nodes, id)
	for i, o := range c.order {
		if o == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	return nil
}

// ---- inspection ----

// NodeInfo summarizes one node.
type NodeInfo struct {
	ID      string
	Up      bool
	Keys    int
	Hints   int
	Members []string // active members in this node's view
}

// Nodes describes every node.
func (c *Cluster) Nodes() []NodeInfo {
	var out []NodeInfo
	for _, id := range c.NodeIDs() {
		n, err := c.node(id)
		if err != nil {
			continue
		}
		k, h := n.counts()
		out = append(out, NodeInfo{ID: id, Up: c.net.IsUp(id), Keys: k, Hints: h, Members: n.View().Active()})
	}
	return out
}

// Inspect returns what a node stores for key: its own versions, and any
// hints it holds for other nodes.
func (c *Cluster) Inspect(id, key string) ([]Version, map[string][]Version, error) {
	n, err := c.node(id)
	if err != nil {
		return nil, nil, err
	}
	return n.localVersions(key), n.hintsFor(key), nil
}

// Keys lists the keys stored on a node.
func (c *Cluster) Keys(id string) ([]string, error) {
	n, err := c.node(id)
	if err != nil {
		return nil, err
	}
	return n.keys(), nil
}

// ViewOf returns a node's active membership list.
func (c *Cluster) ViewOf(id string) ([]string, error) {
	n, err := c.node(id)
	if err != nil {
		return nil, err
	}
	v := n.View().Active()
	sort.Strings(v)
	return v, nil
}
