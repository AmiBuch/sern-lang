package echelon

import (
	"fmt"
	"math/rand"
	"strings"
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
	// b and c both descend from a, and from nothing of each other's.
	a := Version{Value: []byte("a"), Dot: ClockEntry{Node: "x", Counter: 1}}
	b := Version{Value: []byte("b"), Dot: ClockEntry{Node: "x", Counter: 2}, Context: a.FullContext()}
	c := Version{Value: []byte("c"), Dot: ClockEntry{Node: "y", Counter: 1}, Context: a.FullContext()}
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
	w, err := c.Put("mayuri", Context{}, []byte("tuturu"))
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

// Two clients writing from the same context must both survive even when the
// *same* node coordinates both writes. Pinning via to a node in the key's
// preference list makes that node coordinate, so this is the bad case on
// purpose rather than by luck.
func TestConcurrentWritesCreateSiblings(t *testing.T) {
	c := mustCluster(t, nil)
	pl, err := c.PreferenceList("cart")
	if err != nil {
		t.Fatal(err)
	}
	c.Put("cart", Context{}, []byte("banana"))
	r, _ := c.Get("cart")

	wa, err := c.PutVia(pl[0], "cart", r.Context, []byte("banana+upa"))
	if err != nil {
		t.Fatal(err)
	}
	wb, err := c.PutVia(pl[0], "cart", r.Context, []byte("banana+metal-upa"))
	if err != nil {
		t.Fatal(err)
	}
	// Guard the premise: without one shared coordinator this proves nothing.
	if wa.Coordinator != pl[0] || wb.Coordinator != pl[0] {
		t.Fatalf("wanted both writes coordinated by %s, got %s and %s", pl[0], wa.Coordinator, wb.Coordinator)
	}

	r2, _ := c.Get("cart")
	if len(r2.Siblings) != 2 {
		t.Fatalf("want 2 siblings from one coordinator, got %d", len(r2.Siblings))
	}
	got := map[string]bool{}
	for _, v := range r2.Siblings {
		got[string(v.Value)] = true
	}
	if !got["banana+upa"] || !got["banana+metal-upa"] {
		t.Fatalf("wrong siblings survived: %v", got)
	}
	if got["banana"] {
		t.Fatal("the superseded original is still a sibling")
	}

	// A write with the merged context resolves the conflict.
	c.Put("cart", r2.Context, []byte("merged"))
	r3, _ := c.Get("cart")
	if len(r3.Siblings) != 1 || string(r3.Siblings[0].Value) != "merged" {
		t.Fatalf("merge write did not supersede siblings: %d", len(r3.Siblings))
	}
}

// The reviewer's repro: 300 independent keys, ordinary random routing. Before
// dotted version vectors this lost a write roughly 143 times.
func TestNoLostWritesUnderRandomRouting(t *testing.T) {
	c := mustCluster(t, nil)
	lost, sameCoord := 0, 0
	for i := 0; i < 300; i++ {
		key := fmt.Sprintf("cart-%d", i)
		if _, err := c.Put(key, Context{}, []byte("v0")); err != nil {
			t.Fatal(err)
		}
		r, err := c.Get(key)
		if err != nil {
			t.Fatal(err)
		}
		wa, err := c.Put(key, r.Context, []byte("a"))
		if err != nil {
			t.Fatal(err)
		}
		wb, err := c.Put(key, r.Context, []byte("b"))
		if err != nil {
			t.Fatal(err)
		}
		if wa.Coordinator == wb.Coordinator {
			sameCoord++
		}
		r2, err := c.Get(key)
		if err != nil {
			t.Fatal(err)
		}
		if len(r2.Siblings) != 2 {
			lost++
		}
	}
	if lost != 0 {
		t.Fatalf("%d/300 concurrent write pairs lost a version", lost)
	}
	// If routing ever stops colliding, this test silently stops testing
	// anything. Expect ~132/300 collisions at 5 nodes with N=3.
	if sameCoord < 50 {
		t.Fatalf("only %d/300 pairs shared a coordinator; the test no longer exercises the bug", sameCoord)
	}
}

// Truncating a context can only uncover a dot, costing an extra sibling. It
// must never drop a write or leave a version whose dot its own clock omits.
func TestTruncatedContextNeverLosesWrites(t *testing.T) {
	c := mustCluster(t, func(cfg *Config) { cfg.ClockLimit = 2 })
	pl, err := c.PreferenceList("k")
	if err != nil {
		t.Fatal(err)
	}
	c.Put("k", Context{}, []byte("v0"))
	for i := 0; i < 6; i++ {
		r, err := c.Get("k")
		if err != nil {
			t.Fatal(err)
		}
		via := pl[i%len(pl)]
		if _, err := c.PutVia(via, "k", r.Context, []byte(fmt.Sprintf("v%d", i+1))); err != nil {
			t.Fatal(err)
		}
	}
	r, err := c.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Siblings) == 0 {
		t.Fatal("truncation dropped every version")
	}
	for _, v := range r.Siblings {
		if v.Dot.Counter == 0 {
			t.Fatalf("version %q has no dot", v.Value)
		}
		if !v.FullContext().Covers(v.Dot) {
			t.Fatalf("version %q: dot %v missing from its own context %v", v.Value, v.Dot, v.FullContext())
		}
	}
}

func TestPartitionDivergenceAndMerge(t *testing.T) {
	c := mustCluster(t, func(cfg *Config) { cfg.Nodes = 3; cfg.W = 1; cfg.R = 1 })
	c.Put("k", Context{}, []byte("v0"))
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
	w, err := c.Put("kurisu", Context{}, []byte("christina"))
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
	if _, err := c.Put("x", Context{}, []byte("1")); err == nil {
		t.Fatal("W=2 with one live node must fail")
	}
}

func TestAntiEntropyRestoresWipedNode(t *testing.T) {
	c := mustCluster(t, nil)
	for i := 0; i < 100; i++ {
		c.Put(fmt.Sprint("key-", i), Context{}, []byte{byte(i)})
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
	c.Put("rintaro", Context{}, []byte("hououin"))
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
		if _, err := c.Put(fmt.Sprint("k", i), Context{}, []byte("v")); err != nil {
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

// ---- causal contexts: gaps, and what must never fill them ----

func dot(node string, ctr uint64) ClockEntry {
	return ClockEntry{Node: node, Counter: ctr, Stamp: int64(ctr)}
}

func values(vs []Version) string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = string(v.Value)
	}
	return "[" + strings.Join(out, " ") + "]"
}

// A context has to distinguish "everything up to 2" from "2, but not 1". A
// vector clock cannot, which is the whole reason Context exists.
func TestContextKeepsGapsAndFoldsThem(t *testing.T) {
	c := Context{}.Add(dot("n", 2))
	if !c.Gapped() {
		t.Fatalf("a lone dot n:2 sits past a gap: %v", c)
	}
	if c.Covers(dot("n", 1)) {
		t.Fatalf("n:2 must not claim n:1: %v", c)
	}
	if !c.Covers(dot("n", 2)) {
		t.Fatalf("context must cover its own dot: %v", c)
	}
	if got := c.String(); got != "+n:2" {
		t.Fatalf("String() = %q, want %q", got, "+n:2")
	}

	// Filling the gap folds the dot into the prefix: nothing stays loose, so a
	// healthy context is exactly as compact as the old vector clock.
	c = c.Add(dot("n", 1))
	if c.Gapped() || !c.Covers(dot("n", 1)) || !c.Covers(dot("n", 2)) {
		t.Fatalf("n:1 plus n:2 should compact to a plain clock: %v", c)
	}
	if got := c.String(); got != "n:2" {
		t.Fatalf("String() = %q, want %q", got, "n:2")
	}

	// Merging closes a gap from the other side, and is idempotent.
	a := Context{}.Add(dot("n", 3))
	b := Context{}.Add(dot("n", 1)).Add(dot("n", 2))
	if m := MergeContexts(a, b); m.Gapped() || m.String() != "n:3" {
		t.Fatalf("merge should close the gap: %v", m)
	}
	if x := MergeContexts(a, a); x.String() != a.String() {
		t.Fatalf("merge is not idempotent: %v vs %v", x, a)
	}
}

// Truncation may only ever *lower* coverage. The cost is a version that survives
// as a sibling when it could have been superseded, never a version dropped when
// it should have survived: truncation cannot lose a write.
func TestContextTruncateOnlyLowersCoverage(t *testing.T) {
	var full Context
	for i := 1; i <= 6; i++ {
		full = full.Add(ClockEntry{Node: fmt.Sprintf("n%d", i), Counter: 1, Stamp: int64(i)})
	}
	full = full.Add(ClockEntry{Node: "n1", Counter: 9, Stamp: 99}) // loose dot
	if !full.Gapped() {
		t.Fatalf("expected a loose dot: %v", full)
	}
	cut := full.Truncate(3)
	if n := len(cut.Clock) + len(cut.Dots); n > 3 {
		t.Fatalf("truncate kept %d entries, want at most 3: %v", n, cut)
	}
	if !full.Includes(cut) {
		t.Fatalf("truncated context %v is not contained in %v", cut, full)
	}
	for i := 1; i <= 6; i++ {
		for _, ctr := range []uint64{1, 9} {
			e := ClockEntry{Node: fmt.Sprintf("n%d", i), Counter: ctr}
			if cut.Covers(e) && !full.Covers(e) {
				t.Fatalf("truncation invented coverage of %v", e)
			}
		}
	}
}

// A client reads from a replica that missed one of two concurrent writes. Its
// context must record "I saw B, across a gap where A should be" — otherwise its
// next put supersedes A, a version nobody ever read.
//
// The paper's mechanism cannot express that: §4.4 makes the context a vector
// clock and orders versions with a pointwise <=, so {node:2} silently asserts
// node:1. Before gap-aware contexts this test's final read returned ["C"] and A
// was gone. Dots alone (B15) do not reach it, because the gap is destroyed when
// the read path flattens the siblings into one clock.
func TestGapContextKeepsUnseenWrite(t *testing.T) {
	c := mustCluster(t, func(cfg *Config) { cfg.Nodes = 3; cfg.N = 3; cfg.R = 1; cfg.W = 1 })
	pl, err := c.PreferenceList("k")
	if err != nil {
		t.Fatal(err)
	}
	coord, lagging, other := pl[0], pl[1], pl[2]

	c.Partition([][]string{{coord, other}, {lagging}})
	if _, err := c.PutVia(coord, "k", Context{}, []byte("A")); err != nil {
		t.Fatal(err)
	}
	c.Heal()
	if _, err := c.PutVia(coord, "k", Context{}, []byte("B")); err != nil {
		t.Fatal(err)
	}

	// The client can only reach the replica that never received A.
	c.Partition([][]string{{lagging}, {coord, other}})
	r, err := c.GetVia(lagging, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Siblings) != 1 || string(r.Siblings[0].Value) != "B" {
		t.Fatalf("the lagging replica should hold B alone, got %s", values(r.Siblings))
	}
	if !r.Context.Gapped() {
		t.Fatalf("a client that saw only B must carry the gap, got %v", r.Context)
	}
	if _, err := c.PutVia(lagging, "k", r.Context, []byte("C")); err != nil {
		t.Fatal(err)
	}

	c.Heal()
	c.AntiEntropy()
	got, err := c.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"A": true, "C": true}
	if len(got.Siblings) != len(want) {
		t.Fatalf("want A and C, got %s", values(got.Siblings))
	}
	for _, v := range got.Siblings {
		if !want[string(v.Value)] {
			t.Fatalf("unexpected sibling %q, want A and C: %s", v.Value, values(got.Siblings))
		}
	}
}

// The property the gap bug violated, stated end to end: a version is only ever
// dropped when some write that *actually saw it* superseded it.
//
// The model tracks concrete dots — (node, counter) pairs read straight off the
// stored versions — and never consults a Context. An earlier version of this
// test compared the returned context against one rebuilt with MergeContexts,
// which passed happily under the flattened contexts it was meant to catch: both
// sides made the same mistake. Ground truth has to come from outside the type
// under test.
func TestNoVersionDroppedUnobserved(t *testing.T) {
	c := mustCluster(t, func(cfg *Config) { cfg.Nodes = 5; cfg.N = 3; cfg.R = 1; cfg.W = 1 })
	rng := rand.New(rand.NewSource(9))
	var key string

	// live is every version anywhere in the cluster, by dot. It unions across
	// all replicas, so a dot missing from it has genuinely been reconciled away
	// rather than merely not replicated yet.
	live := func(key string) map[string]string {
		out := map[string]string{}
		for _, id := range c.NodeIDs() {
			vs, _, err := c.Inspect(id, key)
			if err != nil {
				continue
			}
			for _, v := range vs {
				out[dotID(key, v.Dot)] = string(v.Value)
			}
		}
		return out
	}

	// saw[d] is every dot the author of write d had actually observed.
	saw := map[string]map[string]bool{}
	keys := []string{"cart", "list", "notes", "plan"}
	for i := 0; i < 400; i++ {
		key = keys[rng.Intn(len(keys))]
		pl, err := c.PreferenceList(key)
		if err != nil {
			t.Fatal(err)
		}
		// Hide a single replica from this round's write about half the time. That
		// is what leaves one replica behind on a coordinator's dot sequence,
		// which is the precondition for a gap.
		if rng.Intn(2) == 0 {
			hidden := pl[1+rng.Intn(len(pl)-1)]
			var rest []string
			for _, id := range c.NodeIDs() {
				if id != hidden {
					rest = append(rest, id)
				}
			}
			c.Partition([][]string{rest, {hidden}})
		} else {
			c.Heal()
		}

		// Blind writes matter here: two of them through one coordinator are
		// concurrent, so a replica missing the earlier one has a real gap rather
		// than merely stale data.
		observed := map[string]bool{}
		ctx := Context{}
		if rng.Intn(2) == 0 {
			via := pl[rng.Intn(len(pl))]
			if r, err := c.GetVia(via, key); err == nil {
				ctx = r.Context
				for _, v := range r.Siblings {
					observed[dotID(key, v.Dot)] = true
					for d := range saw[dotID(key, v.Dot)] {
						observed[d] = true
					}
				}
			}
		}
		val := fmt.Sprintf("%s-v%d", key, i)
		// Record the dot even when the quorum fails: a failed put still stores
		// its version on whatever replica it reached, so that version exists and
		// has to be accounted for.
		w, _ := c.PutVia(pl[rng.Intn(len(pl))], key, ctx, []byte(val))
		if w.Dot.Counter > 0 {
			saw[dotID(key, w.Dot)] = observed
		}
	}

	c.Heal()
	c.Settle()
	for i := 0; i < 3; i++ {
		c.Tick()
	}
	final := map[string]string{}
	for _, k := range keys {
		for d, v := range live(k) {
			final[d] = v
		}
	}
	for d := range saw {
		if _, still := final[d]; still {
			continue
		}
		justified := false
		for l := range final {
			if saw[l][d] {
				justified = true
				break
			}
		}
		if !justified {
			t.Fatalf("version %s was dropped, but no surviving write had ever seen it (survivors: %v)", d, final)
		}
	}
	if len(saw) < 50 {
		t.Fatalf("only %d writes tracked; the test is no longer exercising much", len(saw))
	}
}

// dotID names a dot. A dot is only unique *within* a key — each node keeps a
// separate counter per key — so the key belongs in the identity.
func dotID(key string, d ClockEntry) string {
	return fmt.Sprintf("%s/%s:%d", key, d.Node, d.Counter)
}

// Every gap comes from *delivery*, never from numbering: a coordinator's dots
// for a key are contiguous, because nextCounter only ever advances by one within
// its own counter space. That is what keeps the loose-dot list short, and it is
// implicit in nextCounter, so pin it.
func TestCoordinatorDotsAreGapless(t *testing.T) {
	c := mustCluster(t, func(cfg *Config) { cfg.Nodes = 5; cfg.N = 3; cfg.R = 1; cfg.W = 1 })
	rng := rand.New(rand.NewSource(4))
	pl, err := c.PreferenceList("k")
	if err != nil {
		t.Fatal(err)
	}
	coordinated := map[string]uint64{}
	for i := 0; i < 80; i++ {
		via := pl[rng.Intn(len(pl))]
		ctx := Context{}
		if rng.Intn(3) > 0 { // mix blind writes in with read-modify-writes
			if r, err := c.GetVia(via, "k"); err == nil {
				ctx = r.Context
			}
		}
		if w, _ := c.PutVia(via, "k", ctx, []byte(fmt.Sprintf("v%d", i))); w.Coordinator != "" {
			coordinated[w.Coordinator]++
		}
		if rng.Intn(4) == 0 {
			c.Partition([][]string{{pl[0]}, {pl[1], pl[2]}})
		} else {
			c.Heal()
		}
	}
	c.Heal()
	c.Settle()
	if len(coordinated) < 2 {
		t.Fatalf("only %d coordinators took part; the test is not exercising much", len(coordinated))
	}
	for id, count := range coordinated {
		n, err := c.node(id)
		if err != nil {
			t.Fatal(err)
		}
		n.mu.Lock()
		issued := n.issued["k"]
		n.mu.Unlock()
		if issued != count {
			t.Fatalf("%s coordinated %d writes but its counter reached %d: dots are not contiguous", id, count, issued)
		}
	}
}
