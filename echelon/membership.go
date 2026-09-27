package echelon

import "sort"

// MemberStatus is a node's membership state.
type MemberStatus int

const (
	Joined MemberStatus = iota
	Left
)

func (s MemberStatus) String() string {
	if s == Left {
		return "left"
	}
	return "joined"
}

// Member is one node's entry in a membership view. Version increases
// every time the node's own status changes, so the newest entry wins.
type Member struct {
	Node    string
	Status  MemberStatus
	Version uint64
}

// View is a node's belief about cluster membership (§4.8). Views are
// reconciled by gossip: each round a node exchanges its view with a random
// peer, and each side keeps the higher-versioned entry per node. Changes
// spread epidemically, reaching all N nodes in O(log N) rounds.
type View map[string]Member

// Clone copies a view.
func (v View) Clone() View {
	out := make(View, len(v))
	for k, m := range v {
		out[k] = m
	}
	return out
}

// Merge folds o into v and reports whether anything changed.
func (v View) Merge(o View) bool {
	changed := false
	for k, m := range o {
		if cur, ok := v[k]; !ok || m.Version > cur.Version {
			v[k] = m
			changed = true
		}
	}
	return changed
}

// Active lists nodes currently considered part of the ring.
// Note that a crashed node is still a member: Dynamo treats outages as
// transient and only an explicit leave removes a node from the ring.
func (v View) Active() []string {
	var out []string
	for k, m := range v {
		if m.Status == Joined {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// fingerprint identifies the active set, for caching the ring.
func (v View) fingerprint() string {
	s := ""
	for _, n := range v.Active() {
		s += n + ","
	}
	return s
}
