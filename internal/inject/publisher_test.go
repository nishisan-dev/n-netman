package inject

import (
	"net"
	"strings"
	"testing"
	"time"

	pb "github.com/nishisan-dev/n-netman/api/inject/v1"
	"github.com/nishisan-dev/n-netman/internal/config"
)

// fakeTransport captures datagrams instead of putting them on the wire.
type fakeTransport struct {
	sent [][]byte
	err  error
}

func (f *fakeTransport) Send(d []byte) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, append([]byte(nil), d...))
	return nil
}
func (f *fakeTransport) Close() error { return nil }

// staticRoutes is a RouteSource backed by a fixed slice.
type staticRoutes []RIBRoute

func (s staticRoutes) InjectableRoutes() []RIBRoute { return s }

func basePublisherConfig(inject config.InjectConfig, tags []string) PublisherConfig {
	return PublisherConfig{
		ControllerID: "host-a",
		VNI:          100,
		Segment:      "vxlan-prod",
		BridgeName:   "br-prod",
		BridgeIPv4:   "10.100.0.1/24",
		Tags:         tags,
		Inject:       inject,
		PSK:          testPSK,
		KeyID:        "k1",
	}
}

func newTestPublisher(t *testing.T, inject config.InjectConfig, tags []string, opts ...Option) (*Publisher, *fakeTransport) {
	t.Helper()
	transport := &fakeTransport{}
	opts = append([]Option{WithTransport(transport)}, opts...)
	p, err := NewPublisher(basePublisherConfig(inject, tags), opts...)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	if p == nil {
		t.Fatal("expected a publisher, got nil")
	}
	return p, transport
}

func routeSet(adv *pb.Advertisement) map[string]*pb.InjectedRoute {
	out := make(map[string]*pb.InjectedRoute, len(adv.GetRoutes()))
	for _, r := range adv.GetRoutes() {
		out[r.GetPrefix()] = r
	}
	return out
}

// A bridge whose tags match no rule is not part of the channel at all.
func TestNewPublisher_NoMatchingRulesReturnsNil(t *testing.T) {
	inject := config.InjectConfig{
		Enabled: true,
		Rules:   []config.InjectRule{{MatchTags: []string{"storage"}, FromRIB: true}},
	}
	p, err := NewPublisher(basePublisherConfig(inject, []string{"it"}), WithTransport(&fakeTransport{}))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if p != nil {
		t.Fatal("expected nil publisher for an unmatched bridge")
	}
}

func TestPublisher_AdvertisesExplicitNetworks(t *testing.T) {
	inject := config.InjectConfig{
		Enabled: true,
		Rules: []config.InjectRule{{
			MatchTags: []string{"it"},
			Networks:  []string{"172.16.10.0/24", "172.16.20.0/24"},
			Metric:    50,
		}},
	}
	p, _ := newTestPublisher(t, inject, []string{"it", "external"})

	adv, err := p.buildAdvertisement(time.Now())
	if err != nil {
		t.Fatalf("buildAdvertisement: %v", err)
	}

	routes := routeSet(adv)
	if len(routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(routes))
	}
	r := routes["172.16.10.0/24"]
	if r == nil {
		t.Fatal("expected 172.16.10.0/24 to be advertised")
	}
	// The next-hop defaults to this host's address on the segment.
	if r.GetNextHop() != "10.100.0.1" {
		t.Fatalf("expected next-hop 10.100.0.1, got %s", r.GetNextHop())
	}
	if r.GetMetric() != 50 {
		t.Fatalf("expected metric 50, got %d", r.GetMetric())
	}
	if adv.GetControllerId() != "host-a" || adv.GetVni() != 100 {
		t.Fatalf("unexpected identity: %+v", adv)
	}
	if len(adv.GetTags()) != 2 {
		t.Fatalf("expected the bridge tags to travel with the advertisement, got %v", adv.GetTags())
	}
}

func TestPublisher_AdvertisesUnionOfMatchingRules(t *testing.T) {
	inject := config.InjectConfig{
		Enabled: true,
		Rules: []config.InjectRule{
			{MatchTags: []string{"it"}, Networks: []string{"172.16.10.0/24"}},
			{MatchTags: []string{"it", "external"}, Networks: []string{"192.168.0.0/24"}, DefaultGateway: "10.100.0.1"},
			{MatchTags: []string{"storage"}, Networks: []string{"10.99.0.0/24"}},
		},
	}
	p, _ := newTestPublisher(t, inject, []string{"it", "external"})

	adv, err := p.buildAdvertisement(time.Now())
	if err != nil {
		t.Fatalf("buildAdvertisement: %v", err)
	}

	routes := routeSet(adv)
	if len(routes) != 2 {
		t.Fatalf("expected the union of the two matching rules, got %d routes", len(routes))
	}
	if _, ok := routes["10.99.0.0/24"]; ok {
		t.Fatal("a rule that did not match must not contribute routes")
	}
	if adv.GetDefaultGateway() != "10.100.0.1" {
		t.Fatalf("expected the default gateway to be advertised, got %q", adv.GetDefaultGateway())
	}
}

func TestPublisher_FromRIBFiltersByVNIAndRouteTags(t *testing.T) {
	rib := staticRoutes{
		{Prefix: "172.16.10.0/24", Metric: 100, Tags: []string{"it"}, VNI: 100},
		{Prefix: "172.16.20.0/24", Metric: 100, Tags: []string{"storage"}, VNI: 100},
		{Prefix: "172.16.30.0/24", Metric: 100, Tags: []string{"it"}, VNI: 200},
		{Prefix: "172.16.40.0/24", Metric: 100, Tags: nil, VNI: 100},
	}
	inject := config.InjectConfig{
		Enabled: true,
		Rules: []config.InjectRule{{
			MatchTags: []string{"it"},
			FromRIB:   true,
			RouteTags: []string{"it"},
		}},
	}
	p, _ := newTestPublisher(t, inject, []string{"it"}, WithRouteSource(rib))

	adv, err := p.buildAdvertisement(time.Now())
	if err != nil {
		t.Fatalf("buildAdvertisement: %v", err)
	}

	routes := routeSet(adv)
	if _, ok := routes["172.16.10.0/24"]; !ok {
		t.Fatal("expected the matching RIB route to be advertised")
	}
	if _, ok := routes["172.16.20.0/24"]; ok {
		t.Fatal("a route whose communities do not match route_tags must be excluded")
	}
	if _, ok := routes["172.16.30.0/24"]; ok {
		t.Fatal("a route from another VNI must be excluded")
	}
	if _, ok := routes["172.16.40.0/24"]; ok {
		t.Fatal("an untagged route must not match an explicit route_tags filter")
	}

	// The VM must send to this host, not to the peer that originated the prefix.
	if got := routes["172.16.10.0/24"].GetNextHop(); got != "10.100.0.1" {
		t.Fatalf("expected the segment next-hop 10.100.0.1, got %s", got)
	}
}

func TestPublisher_EmptyRouteTagsAcceptsEveryRIBRoute(t *testing.T) {
	rib := staticRoutes{
		{Prefix: "172.16.10.0/24", Metric: 100, Tags: []string{"it"}, VNI: 100},
		{Prefix: "172.16.40.0/24", Metric: 100, Tags: nil, VNI: 100},
	}
	inject := config.InjectConfig{
		Enabled: true,
		Rules:   []config.InjectRule{{MatchTags: []string{"it"}, FromRIB: true}},
	}
	p, _ := newTestPublisher(t, inject, []string{"it"}, WithRouteSource(rib))

	adv, err := p.buildAdvertisement(time.Now())
	if err != nil {
		t.Fatalf("buildAdvertisement: %v", err)
	}
	if len(adv.GetRoutes()) != 2 {
		t.Fatalf("expected both RIB routes, got %d", len(adv.GetRoutes()))
	}
}

func TestPublisher_RuleNextHopOverridesBridgeAddress(t *testing.T) {
	inject := config.InjectConfig{
		Enabled: true,
		Rules: []config.InjectRule{{
			MatchTags: []string{"it"},
			Networks:  []string{"172.16.10.0/24"},
			NextHop:   "10.100.0.254",
		}},
	}
	p, _ := newTestPublisher(t, inject, []string{"it"})

	adv, err := p.buildAdvertisement(time.Now())
	if err != nil {
		t.Fatalf("buildAdvertisement: %v", err)
	}
	if got := routeSet(adv)["172.16.10.0/24"].GetNextHop(); got != "10.100.0.254" {
		t.Fatalf("expected the rule next_hop to win, got %s", got)
	}
}

// The same prefix reachable through the same next-hop must collapse to one
// route, keeping the better metric.
func TestPublisher_DeduplicatesKeepingLowestMetric(t *testing.T) {
	inject := config.InjectConfig{
		Enabled: true,
		Rules: []config.InjectRule{
			{MatchTags: []string{"it"}, Networks: []string{"172.16.10.0/24"}, Metric: 200},
			{MatchTags: []string{"external"}, Networks: []string{"172.16.10.0/24"}, Metric: 50},
		},
	}
	p, _ := newTestPublisher(t, inject, []string{"it", "external"})

	adv, err := p.buildAdvertisement(time.Now())
	if err != nil {
		t.Fatalf("buildAdvertisement: %v", err)
	}
	if len(adv.GetRoutes()) != 1 {
		t.Fatalf("expected a single deduplicated route, got %d", len(adv.GetRoutes()))
	}
	if got := adv.GetRoutes()[0].GetMetric(); got != 50 {
		t.Fatalf("expected the lowest metric to win, got %d", got)
	}
}

func TestNewPublisher_RejectsConflictingDefaultGateways(t *testing.T) {
	inject := config.InjectConfig{
		Enabled: true,
		Rules: []config.InjectRule{
			{MatchTags: []string{"it"}, DefaultGateway: "10.100.0.1"},
			{MatchTags: []string{"external"}, DefaultGateway: "10.100.0.2"},
		},
	}
	_, err := NewPublisher(basePublisherConfig(inject, []string{"it", "external"}), WithTransport(&fakeTransport{}))
	if err == nil || !strings.Contains(err.Error(), "conflicting default gateways") {
		t.Fatalf("expected a conflicting gateway error, got: %v", err)
	}
}

func TestNewPublisher_RejectsMissingNextHopSource(t *testing.T) {
	inject := config.InjectConfig{
		Enabled: true,
		Rules:   []config.InjectRule{{MatchTags: []string{"it"}, FromRIB: true}},
	}
	cfg := basePublisherConfig(inject, []string{"it"})
	cfg.BridgeIPv4 = ""

	if _, err := NewPublisher(cfg, WithTransport(&fakeTransport{})); err == nil {
		t.Fatal("expected an error when neither bridge.ipv4 nor next_hop is available")
	}
}

// A published advertisement must round-trip through the codec, and its sequence
// must advance so the receiver's replay guard admits it.
func TestPublisher_AdvertiseProducesVerifiableDatagrams(t *testing.T) {
	inject := config.InjectConfig{
		Enabled: true,
		Rules:   []config.InjectRule{{MatchTags: []string{"it"}, Networks: []string{"172.16.10.0/24"}}},
	}
	p, transport := newTestPublisher(t, inject, []string{"it"})

	for i := 0; i < 2; i++ {
		if err := p.Advertise(); err != nil {
			t.Fatalf("Advertise: %v", err)
		}
	}
	if len(transport.sent) != 2 {
		t.Fatalf("expected 2 datagrams, got %d", len(transport.sent))
	}

	ring := mustKeyRing(t)
	guard := NewReplayGuard(ReplayWindow)
	var lastSeq uint64
	for i, datagram := range transport.sent {
		adv, err := Open(datagram, ring, time.Now(), guard)
		if err != nil {
			t.Fatalf("datagram %d failed to verify: %v", i, err)
		}
		if adv.GetSequence() <= lastSeq {
			t.Fatalf("expected an advancing sequence, got %d after %d", adv.GetSequence(), lastSeq)
		}
		lastSeq = adv.GetSequence()
		if adv.GetLeaseSeconds() != uint32(config.DefaultInjectLease) {
			t.Fatalf("expected the default lease, got %d", adv.GetLeaseSeconds())
		}
	}
}

func TestTagsIntersect(t *testing.T) {
	cases := []struct {
		name string
		have []string
		want []string
		out  bool
	}{
		{name: "empty want accepts anything", have: []string{"it"}, want: nil, out: true},
		{name: "empty want accepts an untagged route", have: nil, want: nil, out: true},
		{name: "common tag", have: []string{"it", "prod"}, want: []string{"prod"}, out: true},
		{name: "no common tag", have: []string{"it"}, want: []string{"storage"}},
		{name: "untagged route against an explicit filter", have: nil, want: []string{"it"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tagsIntersect(tc.have, tc.want); got != tc.out {
				t.Fatalf("tagsIntersect(%v, %v) = %v, want %v", tc.have, tc.want, got, tc.out)
			}
		})
	}
}

func TestNewMulticastTransport_Validation(t *testing.T) {
	if _, err := NewMulticastTransport("", net.ParseIP("239.8.0.100"), 4790); err == nil {
		t.Fatal("expected an error for an empty interface name")
	}
	if _, err := NewMulticastTransport("br-prod", net.ParseIP("10.0.0.1"), 4790); err == nil {
		t.Fatal("expected an error for a non-multicast group")
	}
}

// The socket must not be opened at construction: the bridge is created by the
// reconciler and may not exist yet when the publisher is built.
func TestNewMulticastTransport_DoesNotTouchTheInterfaceUntilSend(t *testing.T) {
	transport, err := NewMulticastTransport("nnet-absent-iface", net.ParseIP("239.8.0.100"), 4790)
	if err != nil {
		t.Fatalf("expected construction to succeed for an absent interface, got: %v", err)
	}
	defer transport.Close()

	if err := transport.Send([]byte("x")); err == nil {
		t.Fatal("expected Send to report the missing interface")
	}
}
