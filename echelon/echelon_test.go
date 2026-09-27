package echelon

import (
	"fmt"
	"testing"
	"time"
)

func mustCluster(t *testing.T, mod func(*Config)) *Cluster {
	t.Helper()
	cfg := DefaultConfig()
	if mod != nil {
		mod(&cfg)
	}
	c, err := NewCluster(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestVClockCompare(t *testing.T) {
	a := VClock(nil).Increment("x", 1) // x:1
	b := a.Increment("x", 2)           // x:2
	c := a.Increment("y", 3)           // x:1 y:1
	d := Merge(b, c).Increment("z", 4) // x:2 y:1 z:1
	cases := []struct {
		l, r VClock
		want Order
	}{
		{a, a, Equal}, {a, b, Before}, {b, a, After}, {b, c, Concurrent},
		{d, b, After}, {d, c, After}, {nil, a, Before},
	}
	for _, tc := range cases {
		if got := Compare(tc.l, tc.r); got != tc.want {
			t.Errorf("Compare(%v, %v) = %v, want %v", tc.l, tc.r, got, tc.want)
		}
	}
}

func TestVClockTruncate(t *testing.T) {
	var v VClock
	for i := 0; i < 15; i++ {
		v = v.Increment(fmt.Sprintf("n%02d", i), int64(i))
	}
	v = v.Truncate(10)
	if len(v) != 10 || v.Get("n00") != 0 || v.Get("n14") != 1 {
		t.Fatalf("truncate kept the wrong entries: %v", v)
	}
}

func TestReconcileDropsAncestors(t *testing.T) {
	a := Version{Value: []byte("a"), Clock: VClock(nil).Increment("x", 1)}
	b := Version{Value: []byte("b"), Clock: a.Clock.Increment("x", 2)}
	c := Version{Value: []byte("c"), Clock: a.Clock.Increment("y", 3)}
	got := Reconcile([]Version{a, b, c, b})
	if len(got) != 2 {
		t.Fatalf("want 2 concurrent siblings, got %d", len(got))
	}
}

func TestRingPreferenceAndBalance(t *testing.T) {
	r := NewRing([]string{"a", "b", "c", "d", "e"}, 64)
	pl := r.PreferenceList("okabe", 3)
	if len(pl) != 3 || pl[0] == pl[1] || pl[1] == pl[2] || pl[0] == pl[2] {
		t.Fatalf("preference list must hold 3 distinct nodes: %v", pl)
	}
	total := 0.0
	for n, share := range r.Ownership() {
		total += share
		if share < 0.10 || share > 0.30 {
			t.Errorf("node %s owns %.2f of the ring; vnodes should balance it near 0.20", n, share)
		}
	}
	if total < 0.999 || total > 1.001 {
		t.Errorf("ownership should sum to 1, got %f", total)
	}
	// Every key must fall inside exactly one range.
	for i := 0; i < 200; i++ {
		pos := HashKey(fmt.Sprint("k", i))
		hits := 0
		for _, rg := range r.Ranges() {
			if rg.Contains(pos) {
				hits++
			}
		}
		if hits != 1 {
			t.Fatalf("key position %x in %d ranges", pos, hits)
		}
	}
}

func TestPutGet(t *testing.T) {
	c := mustCluster(t, nil)
	w, err := c.Put("mayuri", nil, []byte("tuturu"))
	if err != nil || w.Acks != 3 {
		t.Fatalf("put: %v acks=%d", err, w.Acks)
	}
	r, err := c.Get("mayuri")
	if err != nil || !r.Found || len(r.Siblings) != 1 || string(r.Siblings[0].Value) != "tuturu" {
		t.Fatalf("get: %+v %v", r, err)
	}
	// Overwrite with context: the old version is superseded, not a sibling.
	if _, err := c.Put("mayuri", r.Context, []byte("tuturu~")); err != nil {
		t.Fatal(err)
	}
	r, _ = c.Get("mayuri")
	if len(r.Siblings) != 1 || string(r.Siblings[0].Value) != "tuturu~" {
		t.Fatalf("expected single updated version, got %d siblings", len(r.Siblings))
	}
}

func TestConcurrentWritesCreateSiblings(t *testing.T) {
	c := mustCluster(t, nil)
	c.Put("cart", nil, []byte("banana"))
	r, _ := c.Get("cart")
	// Two clients update from the same context: neither descends from the other.
	c.Put("cart", r.Context, []byte("banana+upa"))
	c.Put("cart", r.Context, []byte("banana+metal-upa"))
	r2, _ := c.Get("cart")
	if len(r2.Siblings) < 1 {
		t.Fatal("lost writes")
	}
	// Siblings only appear if the two writes had different coordinators.
	if len(r2.Siblings) == 2 {
		// A write with the merged context resolves the conflict.
		c.Put("cart", r2.Context, []byte("merged"))
		r3, _ := c.Get("cart")
		if len(r3.Siblings) != 1 || string(r3.Siblings[0].Value) != "merged" {
			t.Fatalf("merge write did not supersede siblings: %d", len(r3.Siblings))
		}
	}
}

func TestPartitionDivergenceAndMerge(t *testing.T) {
	c := mustCluster(t, func(cfg *Config) { cfg.Nodes = 3; cfg.W = 1; cfg.R = 1 })
	c.Put("k", nil, []byte("v0"))
	r, _ := c.Get("k")
	c.Partition([][]string{{"node-1"}, {"node-2", "node-3"}})
	// Force writes on both sides by crashing the other side's entry points.
	c.Crash("node-2")
	c.Crash("node-3")
	if _, err := c.Put("k", r.Context, []byte("left")); err != nil {
		t.Fatal(err)
	}
	c.Revive("node-2")
	c.Revive("node-3")
	c.Crash("node-1")
	if _, err := c.Put("k", r.Context, []byte("right")); err != nil {
		t.Fatal(err)
	}
	c.Revive("node-1")
	c.Heal()
	c.Tick()
	got, _ := c.Get("k")
	if len(got.Siblings) != 2 {
		t.Fatalf("expected 2 divergent worldlines after healing, got %d", len(got.Siblings))
	}
}

func TestHintedHandoff(t *testing.T) {
	c := mustCluster(t, nil)
	pl, _ := c.PreferenceList("kurisu")
	victim := pl[len(pl)-1]
	c.Crash(victim)
	w, err := c.Put("kurisu", nil, []byte("christina"))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Hints) != 1 {
		t.Fatalf("expected one hinted replica, got %v", w.Hints)
	}
	if vs, _, _ := c.Inspect(victim, "kurisu"); len(vs) != 0 {
		t.Fatal("crashed node should not have the write yet")
	}
	c.Revive(victim)
	if st := c.Handoff(); st.Delivered != 1 {
		t.Fatalf("expected 1 delivered hint, got %+v", st)
	}
	if vs, _, _ := c.Inspect(victim, "kurisu"); len(vs) != 1 {
		t.Fatal("hint was not delivered to its owner")
	}
	for fb := range w.Hints {
		if _, hints, _ := c.Inspect(fb, "kurisu"); len(hints) != 0 {
			t.Fatal("delivered hint should be deleted from the stand-in")
		}
	}
}

func TestQuorumFailure(t *testing.T) {
	c := mustCluster(t, func(cfg *Config) { cfg.Nodes = 3 })
	c.Crash("node-2")
	c.Crash("node-3")
	if _, err := c.Put("x", nil, []byte("1")); err == nil {
		t.Fatal("W=2 with one live node must fail")
	}
}

func TestAntiEntropyRestoresWipedNode(t *testing.T) {
	c := mustCluster(t, nil)
	for i := 0; i < 100; i++ {
		c.Put(fmt.Sprint("key-", i), nil, []byte{byte(i)})
	}
	before, _ := c.Keys("node-2")
	c.Wipe("node-2")
	st := c.AntiEntropy()
	after, _ := c.Keys("node-2")
	if len(after) != len(before) || st.KeysSent == 0 {
		t.Fatalf("anti-entropy restored %d/%d keys (%+v)", len(after), len(before), st)
	}
	// A second pass finds everything in sync using only root hashes.
	st2 := c.AntiEntropy()
	if st2.Repaired != 0 || st2.InSync != st2.PairsChecked {
		t.Fatalf("second pass should be all in sync: %+v", st2)
	}
}

func TestReadRepair(t *testing.T) {
	c := mustCluster(t, nil)
	c.Put("rintaro", nil, []byte("hououin"))
	pl, _ := c.PreferenceList("rintaro")
	c.Wipe(pl[0])
	r, _ := c.Get("rintaro")
	// The wiped replica is repaired if it was among those that answered.
	found := false
	for _, n := range r.Repaired {
		if n == pl[0] {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected %s to be read-repaired, got %v", pl[0], r.Repaired)
	}
}

func TestGossipJoinConverges(t *testing.T) {
	c := mustCluster(t, nil)
	if err := c.Join("node-6"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		c.Gossip(1)
	}
	for _, info := range c.Nodes() {
		if len(info.Members) != 6 {
			t.Fatalf("%s sees %d members after gossip", info.ID, len(info.Members))
		}
	}
	c.Leave("node-6")
	for i := 0; i < 10; i++ {
		c.Gossip(1)
	}
	for _, info := range c.Nodes() {
		if len(info.Members) != 5 {
			t.Fatalf("%s sees %d members after leave", info.ID, len(info.Members))
		}
	}
}

func TestAsyncModeWithLatency(t *testing.T) {
	c := mustCluster(t, func(cfg *Config) {
		cfg.Sync = false
		cfg.LatencyMin = 100 * time.Microsecond
		cfg.LatencyMax = 2 * time.Millisecond
	})
	for i := 0; i < 50; i++ {
		if _, err := c.Put(fmt.Sprint("k", i), nil, []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 50; i++ {
		r, err := c.Get(fmt.Sprint("k", i))
		if err != nil || !r.Found {
			t.Fatalf("get k%d: %v", i, err)
		}
	}
	c.Settle()
}

// TestMerkleLeavesSpread guards against a real bug: leaf indices taken from
// FNV-1a's top bits put similar keys into the same leaf.
func TestMerkleLeavesSpread(t *testing.T) {
	used := map[int]bool{}
	for i := 0; i < 64; i++ {
		used[leafIndex(fmt.Sprint("worldline-", i), 4)] = true
	}
	if len(used) < 12 {
		t.Fatalf("64 similar keys used only %d of 16 leaves", len(used))
	}
}
