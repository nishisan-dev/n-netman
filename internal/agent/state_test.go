package agent

import (
	"testing"
	"time"
)

var epoch = time.Unix(1_700_000_000, 0)

func update(id string, lease time.Duration, routes ...Route) Update {
	return Update{
		ControllerID: id,
		Segment:      "vxlan-prod",
		VNI:          100,
		Tags:         []string{"it"},
		Routes:       routes,
		Lease:        lease,
	}
}

func prefixes(routes []Route) map[string]Route {
	out := make(map[string]Route, len(routes))
	for _, r := range routes {
		out[r.Prefix] = r
	}
	return out
}

func TestSegmentState_UnionOfControllers(t *testing.T) {
	s := NewSegmentState()
	s.Apply(update("host-a", 30*time.Second, Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100}), epoch)
	s.Apply(update("host-b", 30*time.Second, Route{Prefix: "172.16.20.0/24", NextHop: "10.100.0.2", Metric: 100}), epoch)

	desired := s.Desired(epoch)
	if len(desired) != 2 {
		t.Fatalf("expected the union of both controllers, got %d routes", len(desired))
	}
	if len(s.Controllers(epoch)) != 2 {
		t.Fatalf("expected 2 live controllers, got %d", len(s.Controllers(epoch)))
	}
}

// A controller going away must expire only its own routes. This is the whole
// reason state is kept per controller.
func TestSegmentState_LeasesAreIndependent(t *testing.T) {
	s := NewSegmentState()
	s.Apply(update("host-a", 10*time.Second, Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100}), epoch)
	s.Apply(update("host-b", 60*time.Second, Route{Prefix: "172.16.20.0/24", NextHop: "10.100.0.2", Metric: 100}), epoch)

	later := epoch.Add(30 * time.Second)

	expired := s.Expire(later)
	if len(expired) != 1 || expired[0] != "host-a" {
		t.Fatalf("expected only host-a to expire, got %v", expired)
	}

	desired := s.Desired(later)
	if len(desired) != 1 {
		t.Fatalf("expected host-b's route to survive, got %d routes", len(desired))
	}
	if desired[0].Prefix != "172.16.20.0/24" {
		t.Fatalf("the surviving route is the wrong one: %+v", desired[0])
	}
}

func TestSegmentState_SamePrefixResolvesToLowestMetric(t *testing.T) {
	s := NewSegmentState()
	s.Apply(update("host-a", time.Minute, Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 200}), epoch)
	s.Apply(update("host-b", time.Minute, Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.2", Metric: 50}), epoch)

	desired := s.Desired(epoch)
	if len(desired) != 1 {
		t.Fatalf("the kernel holds one route per destination; expected 1, got %d", len(desired))
	}
	if desired[0].NextHop != "10.100.0.2" {
		t.Fatalf("expected the lower metric to win, got next-hop %s", desired[0].NextHop)
	}
}

// An exact tie must resolve the same way every cycle, or the agent would flap
// the route between controllers.
func TestSegmentState_MetricTieIsStable(t *testing.T) {
	s := NewSegmentState()
	s.Apply(update("host-a", time.Minute, Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.9", Metric: 100}), epoch)
	s.Apply(update("host-b", time.Minute, Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.2", Metric: 100}), epoch)

	first := s.Desired(epoch)
	for i := 0; i < 5; i++ {
		again := s.Desired(epoch)
		if again[0].NextHop != first[0].NextHop {
			t.Fatalf("tie-break is not stable: %s then %s", first[0].NextHop, again[0].NextHop)
		}
	}
	if first[0].NextHop != "10.100.0.2" {
		t.Fatalf("expected the deterministic tie-break to pick 10.100.0.2, got %s", first[0].NextHop)
	}
}

// A refresh replaces the previous route set, so a withdrawn prefix disappears
// rather than lingering until the lease runs out.
func TestSegmentState_RefreshReplacesTheRouteSet(t *testing.T) {
	s := NewSegmentState()
	s.Apply(update("host-a", time.Minute,
		Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100},
		Route{Prefix: "172.16.20.0/24", NextHop: "10.100.0.1", Metric: 100},
	), epoch)

	s.Apply(update("host-a", time.Minute,
		Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100},
	), epoch.Add(time.Second))

	desired := s.Desired(epoch.Add(time.Second))
	if len(desired) != 1 {
		t.Fatalf("expected the withdrawn prefix to be gone, got %d routes", len(desired))
	}
	if _, ok := prefixes(desired)["172.16.20.0/24"]; ok {
		t.Fatal("a prefix dropped from the advertisement must not survive")
	}
}

func TestSegmentState_ZeroLeaseFallsBackToTheDefault(t *testing.T) {
	s := NewSegmentState()
	s.Apply(update("host-a", 0, Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100}), epoch)

	if len(s.Desired(epoch.Add(DefaultLease-time.Second))) != 1 {
		t.Fatal("expected the route to be live inside the default lease")
	}
	if len(s.Desired(epoch.Add(DefaultLease+time.Second))) != 0 {
		t.Fatal("a controller must not pin routes forever by omitting the lease")
	}
}

func TestSegmentState_HasLiveController(t *testing.T) {
	s := NewSegmentState()
	if s.HasLiveController(epoch) {
		t.Fatal("an empty state has no live controller")
	}

	s.Apply(update("host-a", 10*time.Second, Route{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100}), epoch)
	if !s.HasLiveController(epoch) {
		t.Fatal("expected a live controller")
	}
	if s.HasLiveController(epoch.Add(time.Minute)) {
		t.Fatal("expected the lease to have run out")
	}
}

// Desired must not expose the caller to the stored slice.
func TestSegmentState_AppliedRoutesAreCopied(t *testing.T) {
	s := NewSegmentState()
	routes := []Route{{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100}}
	s.Apply(update("host-a", time.Minute, routes...), epoch)

	routes[0].NextHop = "10.100.0.99"

	if got := s.Desired(epoch)[0].NextHop; got != "10.100.0.1" {
		t.Fatalf("state must not alias the caller's slice, got next-hop %s", got)
	}
}
