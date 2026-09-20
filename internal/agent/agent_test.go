package agent

import (
	"io"
	"log/slog"
	"testing"
	"time"

	pb "github.com/nishisan-dev/n-netman/api/inject/v1"
	"github.com/nishisan-dev/n-netman/internal/routepolicy"
)

// testSegment builds a segment without any of the netlink or socket wiring;
// onAdvertisement only touches state, so this exercises the real conversion.
func testSegment(policy routepolicy.Policy, acceptGW bool) *segment {
	return &segment{
		policy:   policy,
		acceptGW: acceptGW,
		metric:   100,
		state:    NewSegmentState(),
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		notify:   make(chan struct{}, 1),
	}
}

func advertisement(routes []*pb.InjectedRoute, gateway string) *pb.Advertisement {
	return &pb.Advertisement{
		ControllerId:   "host-a",
		Vni:            100,
		Segment:        "vxlan-prod",
		Tags:           []string{"it"},
		TimestampMs:    time.Now().UnixMilli(),
		Sequence:       1,
		LeaseSeconds:   30,
		Routes:         routes,
		DefaultGateway: gateway,
	}
}

func TestSegment_AppliesImportPolicy(t *testing.T) {
	s := testSegment(routepolicy.Policy{Allow: []string{"172.16.0.0/16"}}, false)

	s.onAdvertisement(advertisement([]*pb.InjectedRoute{
		{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100},
		{Prefix: "192.168.5.0/24", NextHop: "10.100.0.1", Metric: 100},
	}, ""))

	desired := s.state.Desired(time.Now())
	if len(desired) != 1 {
		t.Fatalf("expected only the allowed prefix, got %d routes", len(desired))
	}
	if desired[0].Prefix != "172.16.10.0/24" {
		t.Fatalf("the wrong route survived: %+v", desired[0])
	}
}

func TestSegment_DefaultGatewayIsOptIn(t *testing.T) {
	adv := advertisement([]*pb.InjectedRoute{
		{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100},
	}, "10.100.0.1")

	t.Run("declined by default", func(t *testing.T) {
		s := testSegment(routepolicy.Policy{AcceptAll: true}, false)
		s.onAdvertisement(adv)
		for _, r := range s.state.Desired(time.Now()) {
			if r.Prefix == defaultRoutePrefix {
				t.Fatal("an advertised default gateway must not be installed without opting in")
			}
		}
	})

	t.Run("installed when opted in", func(t *testing.T) {
		s := testSegment(routepolicy.Policy{AcceptAll: true}, true)
		s.onAdvertisement(adv)

		found := false
		for _, r := range s.state.Desired(time.Now()) {
			if r.Prefix == defaultRoutePrefix {
				found = true
				if r.NextHop != "10.100.0.1" {
					t.Fatalf("unexpected gateway next-hop: %s", r.NextHop)
				}
			}
		}
		if !found {
			t.Fatal("expected the default route to be installed")
		}
	})

	// The allow list governs specific prefixes; the flag alone governs the
	// default route. A policy listing only its own prefixes must not silently
	// drop the gateway and make the flag look broken.
	t.Run("not filtered through the allow list", func(t *testing.T) {
		s := testSegment(routepolicy.Policy{Allow: []string{"172.16.0.0/16"}}, true)
		s.onAdvertisement(adv)

		found := false
		for _, r := range s.state.Desired(time.Now()) {
			if r.Prefix == defaultRoutePrefix {
				found = true
			}
		}
		if !found {
			t.Fatal("accept_default_gateway must not require the allow list to also list 0.0.0.0/0")
		}
	})
}

func TestSegment_SkipsRoutesWithoutANextHop(t *testing.T) {
	s := testSegment(routepolicy.Policy{AcceptAll: true}, false)
	s.onAdvertisement(advertisement([]*pb.InjectedRoute{
		{Prefix: "172.16.10.0/24", NextHop: "", Metric: 100},
	}, ""))

	if got := len(s.state.Desired(time.Now())); got != 0 {
		t.Fatalf("expected a route without a next-hop to be skipped, got %d", got)
	}
}

func TestSegment_ZeroMetricFallsBackToTheConfiguredOne(t *testing.T) {
	s := testSegment(routepolicy.Policy{AcceptAll: true}, false)
	s.onAdvertisement(advertisement([]*pb.InjectedRoute{
		{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 0},
	}, ""))

	desired := s.state.Desired(time.Now())
	if len(desired) != 1 || desired[0].Metric != 100 {
		t.Fatalf("expected the configured metric to apply, got %+v", desired)
	}
}

func TestSegment_NotifyCollapsesBursts(t *testing.T) {
	s := testSegment(routepolicy.Policy{AcceptAll: true}, false)
	adv := advertisement([]*pb.InjectedRoute{
		{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100},
	}, "")

	// More advertisements than the channel can hold must not block.
	for i := 0; i < 10; i++ {
		s.onAdvertisement(adv)
	}
	if len(s.notify) != 1 {
		t.Fatalf("expected a single pending reconcile, got %d", len(s.notify))
	}
}
