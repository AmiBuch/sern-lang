package stdlib

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"sern/echelon"
	"sern/object"
)

// ---- the echelon module ----

type echelonModule struct{}

func (*echelonModule) TypeName() string { return "module" }
func (*echelonModule) String() string   { return "<module echelon>" }

func (m *echelonModule) GetField(name string) (object.Value, bool) {
	switch name {
	case "cluster":
		return object.BuiltinVal(fn("echelon.cluster", -1, newCluster)), true
	case "defaults":
		return object.BuiltinVal(fn("echelon.defaults", 0, func(_ object.Caller, _ []object.Value) (object.Value, error) {
			return configMap(echelon.DefaultConfig()), nil
		})), true
	}
	return object.Nil, false
}

func newCluster(_ object.Caller, a []object.Value) (object.Value, error) {
	cfg := echelon.DefaultConfig()
	if len(a) > 1 {
		return object.Nil, errors.New("echelon.cluster takes an optional config map")
	}
	if len(a) == 1 {
		m, err := wantMap("echelon.cluster", a, 0)
		if err != nil {
			return object.Nil, err
		}
		for i, k := range m.Keys() {
			v := m.Values()[i]
			name := k.String()
			intField := func(dst *int) error {
				if v.K != object.KInt {
					return fmt.Errorf("config %s must be an int", name)
				}
				*dst = int(v.I)
				return nil
			}
			var err error
			switch name {
			case "nodes":
				err = intField(&cfg.Nodes)
			case "n":
				err = intField(&cfg.N)
			case "r":
				err = intField(&cfg.R)
			case "w":
				err = intField(&cfg.W)
			case "vnodes":
				err = intField(&cfg.VNodes)
			case "clock_limit":
				err = intField(&cfg.ClockLimit)
			case "merkle_depth":
				err = intField(&cfg.MerkleDepth)
			case "seed":
				var s int
				err = intField(&s)
				cfg.Seed = int64(s)
			case "sync":
				cfg.Sync = v.Truthy()
			case "latency":
				// latency: [min_ms, max_ms]
				if v.K != object.KList || len(v.AsList().Items) != 2 {
					return object.Nil, errors.New("config latency must be [min_ms, max_ms]")
				}
				lo, hi := v.AsList().Items[0], v.AsList().Items[1]
				if !lo.IsNumber() || !hi.IsNumber() {
					return object.Nil, errors.New("config latency must be numbers")
				}
				cfg.LatencyMin = time.Duration(lo.Num() * float64(time.Millisecond))
				cfg.LatencyMax = time.Duration(hi.Num() * float64(time.Millisecond))
			default:
				err = fmt.Errorf("unknown config key %q (known: nodes, n, r, w, vnodes, seed, sync, latency, clock_limit, merkle_depth)", name)
			}
			if err != nil {
				return object.Nil, err
			}
		}
	}
	c, err := echelon.NewCluster(cfg)
	if err != nil {
		return object.Nil, err
	}
	h := &clusterHandle{c: c}
	h.methods = h.buildMethods()
	return object.NativeVal(h), nil
}

func configMap(cfg echelon.Config) object.Value {
	m := object.NewMap()
	m.SetStr("nodes", object.Int(int64(cfg.Nodes)))
	m.SetStr("n", object.Int(int64(cfg.N)))
	m.SetStr("r", object.Int(int64(cfg.R)))
	m.SetStr("w", object.Int(int64(cfg.W)))
	m.SetStr("vnodes", object.Int(int64(cfg.VNodes)))
	m.SetStr("seed", object.Int(cfg.Seed))
	m.SetStr("sync", object.Bool(cfg.Sync))
	m.SetStr("clock_limit", object.Int(int64(cfg.ClockLimit)))
	m.SetStr("merkle_depth", object.Int(int64(cfg.MerkleDepth)))
	return object.MapVal(m)
}

// ---- context values ----

// contextValue wraps a vector clock. Sern code treats it as opaque: you get
// one from get() and hand it back to put() so the new version descends
// from everything you read.
type contextValue struct{ clock echelon.VClock }

func (*contextValue) TypeName() string { return "context" }
func (c *contextValue) String() string {
	if len(c.clock) == 0 {
		return "<context {}>"
	}
	return "<context {" + c.clock.String() + "}>"
}
func (c *contextValue) GetField(name string) (object.Value, bool) {
	if name == "clock" {
		m := object.NewMap()
		for _, e := range c.clock {
			m.SetStr(e.Node, object.Int(int64(e.Counter)))
		}
		return object.MapVal(m), true
	}
	return object.Nil, false
}

func contextVal(v echelon.VClock) object.Value {
	return object.NativeVal(&contextValue{clock: v})
}

func wantContext(name string, a []object.Value, i int) (echelon.VClock, error) {
	if i >= len(a) || a[i].K == object.KNil {
		return nil, nil
	}
	if a[i].K == object.KNative {
		if c, ok := a[i].O.(*contextValue); ok {
			return c.clock, nil
		}
	}
	return nil, argErr(name, i, "a context (from get) or nil", a[i])
}

// ---- cluster handles ----

type clusterHandle struct {
	c       *echelon.Cluster
	methods map[string]*object.Builtin
}

func (*clusterHandle) TypeName() string { return "echelon.cluster" }
func (h *clusterHandle) String() string {
	cfg := h.c.Config()
	return fmt.Sprintf("<echelon cluster nodes=%d N=%d R=%d W=%d>", len(h.c.NodeIDs()), cfg.N, cfg.R, cfg.W)
}
func (h *clusterHandle) GetField(name string) (object.Value, bool) {
	if b, ok := h.methods[name]; ok {
		return object.BuiltinVal(b), true
	}
	return object.Nil, false
}

func strList(ss []string) object.Value {
	out := make([]object.Value, len(ss))
	for i, s := range ss {
		out[i] = object.Str(s)
	}
	return object.ListOf(out)
}

func wantKey(name string, a []object.Value, i int) (string, error) {
	if a[i].K != object.KString {
		return "", fmt.Errorf("ECHELON keys must be strings; argument %d of %s is %s", i+1, name, a[i].TypeName())
	}
	return a[i].AsString(), nil
}

func (h *clusterHandle) settleIfSync() {
	if h.c.Config().Sync {
		h.c.Settle()
	}
}

func (h *clusterHandle) writeResult(w echelon.WriteResult, err error) object.Value {
	h.settleIfSync()
	m := object.NewMap()
	m.SetStr("ok", object.Bool(err == nil))
	m.SetStr("coordinator", object.Str(w.Coordinator))
	m.SetStr("context", contextVal(w.Clock))
	m.SetStr("acks", object.Int(int64(w.Acks)))
	m.SetStr("replicas", strList(w.Replicas))
	hints := object.NewMap()
	for _, fb := range sortedStrKeys(w.Hints) {
		hints.SetStr(fb, object.Str(w.Hints[fb]))
	}
	m.SetStr("hinted", object.MapVal(hints))
	m.SetStr("preference", strList(w.Preference))
	if err != nil {
		m.SetStr("error", object.Str(err.Error()))
	}
	return object.MapVal(m)
}

func sortedStrKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func versionsList(vs []echelon.Version) (object.Value, error) {
	out := make([]object.Value, 0, len(vs))
	for _, v := range vs {
		m := object.NewMap()
		if v.Deleted {
			m.SetStr("value", object.Nil)
		} else {
			val, err := object.Decode(v.Value)
			if err != nil {
				return object.Nil, err
			}
			m.SetStr("value", val)
		}
		m.SetStr("context", contextVal(v.FullClock()))
		m.SetStr("deleted", object.Bool(v.Deleted))
		out = append(out, object.MapVal(m))
	}
	return object.ListOf(out), nil
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

func (h *clusterHandle) buildMethods() map[string]*object.Builtin {
	c := h.c
	methods := map[string]*object.Builtin{}
	def := func(name string, arity int, f func(a []object.Value) (object.Value, error)) {
		methods[name] = fn(name, arity, func(_ object.Caller, a []object.Value) (object.Value, error) { return f(a) })
	}
	nodeArg := func(name string, a []object.Value) (string, error) {
		if len(a) != 1 {
			return "", fmt.Errorf("%s(node) takes 1 argument", name)
		}
		return wantStr(name, a, 0)
	}

	def("put", -1, func(a []object.Value) (object.Value, error) { return h.doPut("", a) })
	def("delete", -1, func(a []object.Value) (object.Value, error) { return h.doDelete("", a) })
	def("get", 1, func(a []object.Value) (object.Value, error) { return h.doGet("", a) })
	def("via", 1, func(a []object.Value) (object.Value, error) {
		id, err := nodeArg("via", a)
		if err != nil {
			return object.Nil, err
		}
		if _, err := c.ViewOf(id); err != nil {
			return object.Nil, err
		}
		return object.NativeVal(&viaHandle{h: h, node: id}), nil
	})

	def("crash", 1, func(a []object.Value) (object.Value, error) {
		id, err := nodeArg("crash", a)
		if err != nil {
			return object.Nil, err
		}
		return object.Nil, c.Crash(id)
	})
	def("revive", 1, func(a []object.Value) (object.Value, error) {
		id, err := nodeArg("revive", a)
		if err != nil {
			return object.Nil, err
		}
		return object.Nil, c.Revive(id)
	})
	def("wipe", 1, func(a []object.Value) (object.Value, error) {
		id, err := nodeArg("wipe", a)
		if err != nil {
			return object.Nil, err
		}
		return object.Nil, c.Wipe(id)
	})
	def("join", 1, func(a []object.Value) (object.Value, error) {
		id, err := nodeArg("join", a)
		if err != nil {
			return object.Nil, err
		}
		return object.Nil, c.Join(id)
	})
	def("leave", 1, func(a []object.Value) (object.Value, error) {
		id, err := nodeArg("leave", a)
		if err != nil {
			return object.Nil, err
		}
		return object.Nil, c.Leave(id)
	})

	def("partition", -1, func(a []object.Value) (object.Value, error) {
		if len(a) < 2 {
			return object.Nil, errors.New("partition(group, group, ...) needs at least 2 groups")
		}
		var groups [][]string
		for i := range a {
			l, err := wantList("partition", a, i)
			if err != nil {
				return object.Nil, err
			}
			var g []string
			for _, it := range l.Items {
				if it.K != object.KString {
					return object.Nil, errors.New("partition groups must be lists of node names")
				}
				g = append(g, it.AsString())
			}
			groups = append(groups, g)
		}
		return object.Nil, c.Partition(groups)
	})
	def("heal", 0, func(a []object.Value) (object.Value, error) {
		c.Heal()
		return object.Nil, nil
	})
	def("settle", 0, func(a []object.Value) (object.Value, error) {
		c.Settle()
		return object.Nil, nil
	})

	def("gossip", -1, func(a []object.Value) (object.Value, error) {
		rounds := int64(1)
		if len(a) == 1 {
			var err error
			if rounds, err = wantInt("gossip", a, 0); err != nil {
				return object.Nil, err
			}
		}
		st := c.Gossip(int(rounds))
		m := object.NewMap()
		m.SetStr("exchanges", object.Int(int64(st.Exchanges)))
		m.SetStr("failed", object.Int(int64(st.Failed)))
		return object.MapVal(m), nil
	})
	def("handoff", 0, func(a []object.Value) (object.Value, error) {
		c.Settle()
		st := c.Handoff()
		m := object.NewMap()
		m.SetStr("delivered", object.Int(int64(st.Delivered)))
		m.SetStr("pending", object.Int(int64(st.Pending)))
		return object.MapVal(m), nil
	})
	def("antientropy", 0, func(a []object.Value) (object.Value, error) {
		c.Settle()
		return aeMap(c.AntiEntropy()), nil
	})
	def("tick", 0, func(a []object.Value) (object.Value, error) {
		st := c.Tick()
		m := object.NewMap()
		m.SetStr("gossip_exchanges", object.Int(int64(st.Gossip.Exchanges)))
		m.SetStr("hints_delivered", object.Int(int64(st.Handoff.Delivered)))
		m.SetStr("hints_pending", object.Int(int64(st.Handoff.Pending)))
		ae := aeMap(st.AntiEntropy).AsMap()
		for i, k := range ae.Keys() {
			m.SetStr("ae_"+k.AsString(), ae.Values()[i])
		}
		return object.MapVal(m), nil
	})

	def("preference", 1, func(a []object.Value) (object.Value, error) {
		key, err := wantKey("preference", a, 0)
		if err != nil {
			return object.Nil, err
		}
		pl, err := c.PreferenceList(key)
		return strList(pl), err
	})
	def("ring", 0, func(a []object.Value) (object.Value, error) {
		r, err := c.ReferenceRing()
		if err != nil {
			return object.Nil, err
		}
		out := make([]object.Value, 0, len(r.Tokens))
		for _, t := range r.Tokens {
			m := object.NewMap()
			m.SetStr("token", object.Str(fmt.Sprintf("%016x", t.Pos)))
			m.SetStr("node", object.Str(t.Node))
			m.SetStr("vnode", object.Int(int64(t.VNode)))
			out = append(out, object.MapVal(m))
		}
		return object.ListOf(out), nil
	})
	def("ownership", 0, func(a []object.Value) (object.Value, error) {
		r, err := c.ReferenceRing()
		if err != nil {
			return object.Nil, err
		}
		own := r.Ownership()
		m := object.NewMap()
		for _, n := range sortedStrKeys(own) {
			m.SetStr(n, object.Float(round3(own[n])))
		}
		return object.MapVal(m), nil
	})
	def("nodes", 0, func(a []object.Value) (object.Value, error) {
		var out []object.Value
		for _, info := range c.Nodes() {
			m := object.NewMap()
			m.SetStr("id", object.Str(info.ID))
			m.SetStr("up", object.Bool(info.Up))
			m.SetStr("keys", object.Int(int64(info.Keys)))
			m.SetStr("hints", object.Int(int64(info.Hints)))
			m.SetStr("members", object.Int(int64(len(info.Members))))
			out = append(out, object.MapVal(m))
		}
		return object.ListOf(out), nil
	})
	def("inspect", 2, func(a []object.Value) (object.Value, error) {
		id, err := wantStr("inspect", a, 0)
		if err != nil {
			return object.Nil, err
		}
		key, err := wantKey("inspect", a, 1)
		if err != nil {
			return object.Nil, err
		}
		vs, hints, err := c.Inspect(id, key)
		if err != nil {
			return object.Nil, err
		}
		m := object.NewMap()
		lv, err := versionsList(vs)
		if err != nil {
			return object.Nil, err
		}
		m.SetStr("versions", lv)
		hm := object.NewMap()
		for _, target := range sortedStrKeys(hints) {
			hv, err := versionsList(hints[target])
			if err != nil {
				return object.Nil, err
			}
			hm.SetStr(target, hv)
		}
		m.SetStr("hints", object.MapVal(hm))
		return object.MapVal(m), nil
	})
	def("keys", 1, func(a []object.Value) (object.Value, error) {
		id, err := nodeArg("keys", a)
		if err != nil {
			return object.Nil, err
		}
		ks, err := c.Keys(id)
		return strList(ks), err
	})
	def("view", 1, func(a []object.Value) (object.Value, error) {
		id, err := nodeArg("view", a)
		if err != nil {
			return object.Nil, err
		}
		v, err := c.ViewOf(id)
		return strList(v), err
	})
	def("stats", 0, func(a []object.Value) (object.Value, error) {
		s := &c.Stats
		m := object.NewMap()
		for _, kv := range []struct {
			k string
			v int64
		}{
			{"puts", s.Puts.Load()},
			{"gets", s.Gets.Load()},
			{"quorum_failures", s.QuorumFailures.Load()},
			{"hints_stored", s.HintsStored.Load()},
			{"hints_delivered", s.HintsDelivered.Load()},
			{"read_repairs", s.ReadRepairs.Load()},
			{"antientropy_keys", s.AntiEntropyKeys.Load()},
			{"gossip_exchanges", s.GossipExchanges.Load()},
			{"coordinator_hops", s.CoordinatorHops.Load()},
			{"sloppy_coordinations", s.SloppyCoordinate.Load()},
			{"messages", c.Network().Calls.Load()},
			{"unreachable", c.Network().Failures.Load()},
		} {
			m.SetStr(kv.k, object.Int(kv.v))
		}
		return object.MapVal(m), nil
	})
	def("config", 0, func(a []object.Value) (object.Value, error) {
		return configMap(c.Config()), nil
	})
	return methods
}

func aeMap(st echelon.AntiEntropyStats) object.Value {
	m := object.NewMap()
	m.SetStr("pairs_checked", object.Int(int64(st.PairsChecked)))
	m.SetStr("in_sync", object.Int(int64(st.InSync)))
	m.SetStr("repaired", object.Int(int64(st.Repaired)))
	m.SetStr("unreachable", object.Int(int64(st.Unreachable)))
	m.SetStr("leaves_differed", object.Int(int64(st.LeavesDiffered)))
	m.SetStr("hashes_sent", object.Int(int64(st.HashesSent)))
	m.SetStr("keys_sent", object.Int(int64(st.KeysSent)))
	return object.MapVal(m)
}

// ---- data operations (shared by cluster handles and via handles) ----

func (h *clusterHandle) doPut(via string, a []object.Value) (object.Value, error) {
	if len(a) < 2 || len(a) > 3 {
		return object.Nil, errors.New("put(key, value[, context]) takes 2 or 3 arguments")
	}
	key, err := wantKey("put", a, 0)
	if err != nil {
		return object.Nil, err
	}
	ctx, err := wantContext("put", a, 2)
	if err != nil {
		return object.Nil, err
	}
	blob, err := object.Encode(a[1])
	if err != nil {
		return object.Nil, err
	}
	if via == "" {
		return h.writeResult(h.c.Put(key, ctx, blob)), nil
	}
	return h.writeResult(h.c.PutVia(via, key, ctx, blob)), nil
}

func (h *clusterHandle) doDelete(via string, a []object.Value) (object.Value, error) {
	if len(a) < 1 || len(a) > 2 {
		return object.Nil, errors.New("delete(key[, context]) takes 1 or 2 arguments")
	}
	key, err := wantKey("delete", a, 0)
	if err != nil {
		return object.Nil, err
	}
	ctx, err := wantContext("delete", a, 1)
	if err != nil {
		return object.Nil, err
	}
	if via == "" {
		return h.writeResult(h.c.Delete(key, ctx)), nil
	}
	return h.writeResult(h.c.DeleteVia(via, key, ctx)), nil
}

func (h *clusterHandle) doGet(via string, a []object.Value) (object.Value, error) {
	key, err := wantKey("get", a, 0)
	if err != nil {
		return object.Nil, err
	}
	var r echelon.ReadResult
	var gerr error
	if via == "" {
		r, gerr = h.c.Get(key)
	} else {
		r, gerr = h.c.GetVia(via, key)
	}
	h.settleIfSync()
	m := object.NewMap()
	m.SetStr("ok", object.Bool(gerr == nil))
	m.SetStr("found", object.Bool(r.Found))
	var worlds []object.Value
	for _, v := range r.Siblings {
		val, err := object.Decode(v.Value)
		if err != nil {
			return object.Nil, err
		}
		worlds = append(worlds, val)
	}
	m.SetStr("worldlines", object.ListOf(worlds))
	m.SetStr("conflict", object.Bool(len(worlds) > 1))
	m.SetStr("context", contextVal(r.Context))
	m.SetStr("coordinator", object.Str(r.Coordinator))
	m.SetStr("responded", strList(r.Responded))
	m.SetStr("repaired", strList(r.Repaired))
	m.SetStr("preference", strList(r.Preference))
	if gerr != nil {
		m.SetStr("error", object.Str(gerr.Error()))
	}
	return object.MapVal(m), nil
}

// viaHandle sends every request to one chosen node, like a client that
// talks to a specific server (useful to write on each side of a partition).
type viaHandle struct {
	h    *clusterHandle
	node string
}

func (*viaHandle) TypeName() string { return "echelon.client" }
func (v *viaHandle) String() string { return "<echelon client via " + v.node + ">" }
func (v *viaHandle) GetField(name string) (object.Value, bool) {
	var f func(string, []object.Value) (object.Value, error)
	arity := -1
	switch name {
	case "put":
		f = v.h.doPut
	case "get":
		f, arity = v.h.doGet, 1
	case "delete":
		f = v.h.doDelete
	default:
		return object.Nil, false
	}
	return object.BuiltinVal(fn(name, arity, func(_ object.Caller, a []object.Value) (object.Value, error) {
		return f(v.node, a)
	})), true
}
