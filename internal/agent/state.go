// Package agent implements nnet-agent, the daemon that runs inside a VM and
// programs routes from advertisements received on the inject channel.
package agent

import (
	"sort"
	"sync"
	"time"
)

// Route is one entry the agent wants in the kernel.
type Route struct {
	Prefix  string
	NextHop string
	Metric  uint32
}

// Update is an accepted advertisement, already filtered by import policy.
type Update struct {
	ControllerID string
	Segment      string
	VNI          uint32
	Tags         []string
	Routes       []Route
	Lease        time.Duration
}

// ControllerStatus reports what one controller currently contributes.
type ControllerStatus struct {
	ID        string    `json:"id"`
	Segment   string    `json:"segment"`
	VNI       uint32    `json:"vni"`
	Tags      []string  `json:"tags"`
	Routes    int       `json:"routes"`
	LastSeen  time.Time `json:"last_seen"`
	ExpiresAt time.Time `json:"expires_at"`
}

type controllerEntry struct {
	segment   string
	vni       uint32
	tags      []string
	routes    []Route
	lastSeen  time.Time
	expiresAt time.Time
}

// SegmentState holds what every controller on one segment has advertised.
//
// State is kept per controller because a segment spans every host in the
// overlay: an agent legitimately hears from several controllers, and each holds
// its own lease. A controller going away must expire only its own routes.
//
// SegmentState is safe for concurrent use.
type SegmentState struct {
	mu          sync.RWMutex
	controllers map[string]*controllerEntry
}

// NewSegmentState creates an empty state.
func NewSegmentState() *SegmentState {
	return &SegmentState{controllers: make(map[string]*controllerEntry)}
}

// Apply records an update, replacing whatever that controller said before.
//
// A lease of zero is treated as the default, so a controller cannot pin routes
// forever by omitting it.
func (s *SegmentState) Apply(u Update, now time.Time) {
	lease := u.Lease
	if lease <= 0 {
		lease = DefaultLease
	}

	routes := make([]Route, len(u.Routes))
	copy(routes, u.Routes)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.controllers[u.ControllerID] = &controllerEntry{
		segment:   u.Segment,
		vni:       u.VNI,
		tags:      u.Tags,
		routes:    routes,
		lastSeen:  now,
		expiresAt: now.Add(lease),
	}
}

// DefaultLease is used when an advertisement does not state one.
const DefaultLease = 30 * time.Second

// Expire drops controllers whose lease has run out and returns their ids.
func (s *SegmentState) Expire(now time.Time) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var expired []string
	for id, entry := range s.controllers {
		if now.After(entry.expiresAt) {
			expired = append(expired, id)
			delete(s.controllers, id)
		}
	}
	sort.Strings(expired)
	return expired
}

// Desired returns the route set the kernel should hold.
//
// The kernel keeps one route per destination per table, so competing
// advertisements for the same prefix have to be resolved here: the lowest
// metric wins, and an exact tie is broken on next-hop so the choice is stable
// rather than flapping between controllers on every cycle.
//
// Load sharing across controllers would need multipath routes and is not
// attempted; one live controller carrying a prefix is enough for the VM to
// reach it, and the other takes over when the first one's lease expires.
func (s *SegmentState) Desired(now time.Time) []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()

	best := make(map[string]Route)
	for _, entry := range s.controllers {
		if now.After(entry.expiresAt) {
			continue
		}
		for _, r := range entry.routes {
			current, seen := best[r.Prefix]
			if !seen || r.Metric < current.Metric ||
				(r.Metric == current.Metric && r.NextHop < current.NextHop) {
				best[r.Prefix] = r
			}
		}
	}

	out := make([]Route, 0, len(best))
	for _, r := range best {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Prefix < out[j].Prefix })
	return out
}

// Controllers reports the controllers currently holding a live lease.
func (s *SegmentState) Controllers(now time.Time) []ControllerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]ControllerStatus, 0, len(s.controllers))
	for id, entry := range s.controllers {
		if now.After(entry.expiresAt) {
			continue
		}
		out = append(out, ControllerStatus{
			ID:        id,
			Segment:   entry.segment,
			VNI:       entry.vni,
			Tags:      entry.tags,
			Routes:    len(entry.routes),
			LastSeen:  entry.lastSeen,
			ExpiresAt: entry.expiresAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// HasLiveController reports whether any controller still holds a lease. It is
// what the agent's health endpoint reflects.
func (s *SegmentState) HasLiveController(now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, entry := range s.controllers {
		if !now.After(entry.expiresAt) {
			return true
		}
	}
	return false
}
