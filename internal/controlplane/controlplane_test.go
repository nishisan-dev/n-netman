package controlplane

import (
	"testing"
	"time"
)

func TestRouteTable_CompositeKeyAvoidsCollisions(t *testing.T) {
	rt := NewRouteTable()

	// Same prefix, different VNIs -> distinct entries.
	rt.Add(Route{Prefix: "10.0.0.0/24", VNI: 100, PeerID: "a", NextHop: "1.1.1.1"})
	rt.Add(Route{Prefix: "10.0.0.0/24", VNI: 200, PeerID: "a", NextHop: "2.2.2.2"})
	// Same prefix and VNI, different peers -> distinct entries.
	rt.Add(Route{Prefix: "10.0.0.0/24", VNI: 100, PeerID: "b", NextHop: "3.3.3.3"})

	if got := len(rt.All()); got != 3 {
		t.Fatalf("expected 3 distinct routes, got %d", got)
	}
}

func TestRouteTable_RemoveByPrefixPeer(t *testing.T) {
	rt := NewRouteTable()
	rt.Add(Route{Prefix: "10.0.0.0/24", VNI: 100, PeerID: "a"})
	rt.Add(Route{Prefix: "10.0.0.0/24", VNI: 200, PeerID: "a"})
	rt.Add(Route{Prefix: "10.0.0.0/24", VNI: 100, PeerID: "b"})

	removed := rt.RemoveByPrefixPeer("10.0.0.0/24", "a")
	if len(removed) != 2 {
		t.Fatalf("expected to remove 2 routes for peer a, got %d", len(removed))
	}
	if got := len(rt.All()); got != 1 {
		t.Fatalf("expected 1 route remaining (peer b), got %d", got)
	}
	if rt.All()[0].PeerID != "b" {
		t.Fatalf("expected remaining route to belong to peer b, got %q", rt.All()[0].PeerID)
	}
}

func TestRouteTable_RemoveByPeerReturnsRoutes(t *testing.T) {
	rt := NewRouteTable()
	rt.Add(Route{Prefix: "10.0.0.0/24", VNI: 100, PeerID: "a"})
	rt.Add(Route{Prefix: "10.1.0.0/24", VNI: 100, PeerID: "a"})
	rt.Add(Route{Prefix: "10.2.0.0/24", VNI: 100, PeerID: "b"})

	removed := rt.RemoveByPeer("a")
	if len(removed) != 2 {
		t.Fatalf("expected 2 routes removed for peer a, got %d", len(removed))
	}
	if got := len(rt.GetByPeer("a")); got != 0 {
		t.Fatalf("expected no routes left for peer a, got %d", got)
	}
}

func TestRouteTable_ExpireStale(t *testing.T) {
	rt := NewRouteTable()
	// Leased route (1s) and an unleased local route (never expires).
	rt.Add(Route{Prefix: "10.0.0.0/24", VNI: 100, PeerID: "a", LeaseSeconds: 1})
	rt.Add(Route{Prefix: "10.9.0.0/24", VNI: 100, PeerID: ""}) // local, no lease

	// Force the leased route to be stale.
	rt.mu.Lock()
	for k, r := range rt.routes {
		if r.PeerID == "a" {
			r.ExpiresAt = time.Now().Add(-time.Second)
			rt.routes[k] = r
		}
	}
	rt.mu.Unlock()

	expired := rt.ExpireStale()
	if len(expired) != 1 || expired[0].PeerID != "a" {
		t.Fatalf("expected exactly the leased route to expire, got %+v", expired)
	}
	if got := len(rt.All()); got != 1 {
		t.Fatalf("expected 1 route remaining (local), got %d", got)
	}
}
