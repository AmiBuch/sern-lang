package echelon

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// WriteResult describes a completed put or delete.
type WriteResult struct {
	Key         string
	Coordinator string
	Clock       VClock // the new version's clock (the next context)
	Preference  []string
	Replicas    []string          // preference-list nodes that stored it
	Hints       map[string]string // fallback node -> node it stands in for
	Acks        int
	OK          bool
}

// ReadResult describes a completed get.
type ReadResult struct {
	Key         string
	Coordinator string
	Preference  []string
	Responded   []string  // replicas whose answers formed the result
	Siblings    []Version // concurrent live versions ("worldlines")
	Context     VClock    // merged clock to pass back on the next put
	Found       bool
	Repaired    []string // replicas fixed by read repair (Sync mode)
	OK          bool
}

// route picks a coordinator for key (§4.5). A client request lands on a
// random node (as if through a load balancer), or on the node named by
// via (a partition-aware client). If that node is not one of
// the key's top N replicas it forwards the request to the first reachable
// one; if none is reachable, the first reachable node further round the
// ring coordinates instead (sloppy quorum).
func (c *Cluster) route(key, via string) (*Node, []string, error) {
	if via == "" {
		live := c.liveIDs()
		if len(live) == 0 {
			return nil, nil, errors.New("every node is down")
		}
		via = live[c.randIntn(len(live))]
	} else if !c.net.IsUp(via) {
		return nil, nil, fmt.Errorf("%s is down; the client cannot reach it", via)
	}
	entry, err := c.node(via)
	if err != nil {
		return nil, nil, err
	}
	walk := entry.Ring().Walk(key)
	if len(walk) == 0 {
		return nil, nil, errors.New("the ring is empty")
	}
	n := min(c.cfg.N, len(walk))
	coordID := ""
	// A node in the key's top N coordinates the request itself.
	for _, id := range walk[:n] {
		if id == entry.ID {
			coordID = id
		}
	}
	// Otherwise forward to the first reachable node, preferring the top N
	// and falling back to nodes further round the ring (sloppy quorum).
	for i := 0; coordID == "" && i < len(walk); i++ {
		id := walk[i]
		reachable := id == entry.ID
		if !reachable {
			_, err := c.net.Call(entry.ID, id, Request{Kind: MsgPing})
			reachable = err == nil
		}
		if reachable {
			coordID = id
			if id != entry.ID {
				c.Stats.CoordinatorHops.Add(1)
			}
			if i >= n {
				c.Stats.SloppyCoordinate.Add(1)
			}
		}
	}
	coord, err := c.node(coordID)
	if err != nil {
		return nil, nil, err
	}
	// The coordinator uses its own view of the ring from here on.
	return coord, coord.Ring().Walk(key), nil
}

// Put writes value under key. ctx is the context returned by an earlier
// Get (nil for a blind write); the new version's clock descends from it.
func (c *Cluster) Put(key string, ctx VClock, value []byte) (WriteResult, error) {
	return c.write(key, ctx, value, false, "")
}

// Delete writes a tombstone for key.
func (c *Cluster) Delete(key string, ctx VClock) (WriteResult, error) {
	return c.write(key, ctx, nil, true, "")
}

// PutVia is Put with the request sent to a specific node.
func (c *Cluster) PutVia(via, key string, ctx VClock, value []byte) (WriteResult, error) {
	return c.write(key, ctx, value, false, via)
}

// DeleteVia is Delete with the request sent to a specific node.
func (c *Cluster) DeleteVia(via, key string, ctx VClock) (WriteResult, error) {
	return c.write(key, ctx, nil, true, via)
}

type ack struct {
	node    string
	hintFor string
}

func (c *Cluster) write(key string, ctx VClock, value []byte, deleted bool, via string) (WriteResult, error) {
	c.Stats.Puts.Add(1)
	coord, walk, err := c.route(key, via)
	if err != nil {
		return WriteResult{Key: key}, err
	}
	// The coordinator stamps the new version by advancing its own entry in
	// the client's context clock, past any counter it has already issued
	// for this key so that two versions never share a clock. If the same
	// coordinator handles two writes made from the same context, the later
	// one descends from the earlier and silently wins: a known weakness of
	// per-node vector clocks (dotted version vectors fix it).
	clock := ctx.WithCounter(coord.ID, coord.nextCounter(key, ctx.Get(coord.ID)), c.stamp.Add(1)).Truncate(c.cfg.ClockLimit)
	ver := Version{Value: value, Clock: clock, Deleted: deleted}
	n := min(c.cfg.N, len(walk))
	top, fallbacks := walk[:n], walk[n:]

	acks := make(chan ack, len(walk))
	c.bg.Add(1)
	go func() {
		defer c.bg.Done()
		defer close(acks)
		// 1. Send to all N preference-list nodes in parallel.
		ok := make([]bool, n)
		var wg sync.WaitGroup
		for i, id := range top {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := c.net.Call(coord.ID, id, Request{Kind: MsgPut, Key: key, Versions: []Version{ver}}); err == nil {
					ok[i] = true
					acks <- ack{node: id}
				}
			}()
		}
		wg.Wait()
		// 2. Sloppy quorum: for each unreachable replica, walk further round
		// the ring and hand the write to a healthy stand-in, tagged with a
		// hint naming the intended owner (§4.6).
		next := 0
		for i, intended := range top {
			if ok[i] {
				continue
			}
			for next < len(fallbacks) {
				fb := fallbacks[next]
				next++
				req := Request{Kind: MsgPut, Key: key, Versions: []Version{ver}, HintFor: intended}
				if _, err := c.net.Call(coord.ID, fb, req); err == nil {
					c.Stats.HintsStored.Add(1)
					acks <- ack{node: fb, hintFor: intended}
					break
				}
			}
		}
	}()

	res := WriteResult{Key: key, Coordinator: coord.ID, Clock: clock, Preference: append([]string(nil), top...), Hints: map[string]string{}}
	for a := range acks {
		res.Acks++
		if a.hintFor != "" {
			res.Hints[a.node] = a.hintFor
		} else {
			res.Replicas = append(res.Replicas, a.node)
		}
		if !c.cfg.Sync && res.Acks >= c.cfg.W {
			break // return to the client; stragglers finish in the background
		}
	}
	sort.Strings(res.Replicas)
	res.OK = res.Acks >= c.cfg.W
	if !res.OK {
		c.Stats.QuorumFailures.Add(1)
		return res, &QuorumError{Op: "put", Need: c.cfg.W, Got: res.Acks}
	}
	return res, nil
}

type reply struct {
	node     string
	rank     int // position in the ring walk; < N means preference list
	versions []Version
}

// Get reads key from the first N healthy replicas, waits for R answers,
// and returns every concurrent version plus a merged context (§4.5).
func (c *Cluster) Get(key string) (ReadResult, error) { return c.GetVia("", key) }

// GetVia is Get with the request sent to a specific node.
func (c *Cluster) GetVia(via, key string) (ReadResult, error) {
	c.Stats.Gets.Add(1)
	coord, walk, err := c.route(key, via)
	if err != nil {
		return ReadResult{Key: key}, err
	}
	n := min(c.cfg.N, len(walk))
	top, fallbacks := walk[:n], walk[n:]

	replies := make(chan reply, len(walk))
	c.bg.Add(1)
	go func() {
		defer c.bg.Done()
		defer close(replies)
		ok := make([]bool, n)
		var wg sync.WaitGroup
		for i, id := range top {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if resp, err := c.net.Call(coord.ID, id, Request{Kind: MsgGet, Key: key}); err == nil {
					ok[i] = true
					replies <- reply{node: id, rank: i, versions: resp.Versions}
				}
			}()
		}
		wg.Wait()
		next := 0
		for i := range top {
			if ok[i] {
				continue
			}
			for next < len(fallbacks) {
				fb := fallbacks[next]
				next++
				if resp, err := c.net.Call(coord.ID, fb, Request{Kind: MsgGet, Key: key}); err == nil {
					replies <- reply{node: fb, rank: n + next - 1, versions: resp.Versions}
					break
				}
			}
		}
	}()

	res := ReadResult{Key: key, Coordinator: coord.ID, Preference: append([]string(nil), top...)}
	var got []reply
	if c.cfg.Sync {
		for r := range replies {
			got = append(got, r)
		}
		// With equal latency the first R to answer are the highest ranked.
		sort.Slice(got, func(i, j int) bool { return got[i].rank < got[j].rank })
	} else {
		for r := range replies {
			got = append(got, r)
			if len(got) == c.cfg.R {
				break
			}
		}
	}
	quorum := got
	if len(quorum) > c.cfg.R {
		quorum = quorum[:c.cfg.R]
	}
	if len(quorum) < c.cfg.R {
		c.Stats.QuorumFailures.Add(1)
		return res, &QuorumError{Op: "get", Need: c.cfg.R, Got: len(quorum)}
	}

	var all []Version
	for _, r := range quorum {
		res.Responded = append(res.Responded, r.node)
		all = append(all, r.versions...)
	}
	versions := Reconcile(all)
	for _, v := range versions {
		res.Context = Merge(res.Context, v.Clock)
		if !v.Deleted {
			res.Siblings = append(res.Siblings, v)
		}
	}
	res.Found = len(res.Siblings) > 0
	res.OK = true

	// Read repair (§5): once every replica has answered, push the
	// reconciled versions to any preference-list replica that was stale.
	if c.cfg.Sync {
		res.Repaired = c.readRepair(coord.ID, key, got, n)
	} else {
		early := append([]reply(nil), got...)
		c.bg.Add(1)
		go func() {
			defer c.bg.Done()
			for r := range replies {
				early = append(early, r)
			}
			c.readRepair(coord.ID, key, early, n)
		}()
	}
	return res, nil
}

func (c *Cluster) readRepair(coord, key string, got []reply, n int) []string {
	var all []Version
	for _, r := range got {
		all = append(all, r.versions...)
	}
	final := Reconcile(all)
	if len(final) == 0 {
		return nil
	}
	want := digest(final)
	var repaired []string
	for _, r := range got {
		if r.rank >= n { // only repair real replicas, not stand-ins
			continue
		}
		if digest(Reconcile(r.versions)) == want {
			continue
		}
		if _, err := c.net.Call(coord, r.node, Request{Kind: MsgPut, Key: key, Versions: final}); err == nil {
			c.Stats.ReadRepairs.Add(1)
			repaired = append(repaired, r.node)
		}
	}
	sort.Strings(repaired)
	return repaired
}
