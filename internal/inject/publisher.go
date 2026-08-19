package inject

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/ipv4"

	pb "github.com/nishisan-dev/n-netman/api/v1"
	"github.com/nishisan-dev/n-netman/internal/config"
	"github.com/nishisan-dev/n-netman/internal/observability"
)

// defaultAdvertisedMetric matches the project's default export metric.
const defaultAdvertisedMetric = 100

// RIBRoute is a route learned from peers, reduced to what the publisher needs.
//
// The controller adapts controlplane.Route into this shape so the inject
// package never depends on the gRPC control plane — which keeps the same
// package usable inside a VM, where gRPC has no business being linked in.
type RIBRoute struct {
	Prefix string
	Metric uint32
	Tags   []string
	VNI    uint32
}

// RouteSource supplies the routes currently known to the controller.
type RouteSource interface {
	InjectableRoutes() []RIBRoute
}

// Transport delivers a sealed datagram to the segment.
type Transport interface {
	Send(datagram []byte) error
	Close() error
}

// PublisherConfig describes one bridge's advertisement.
type PublisherConfig struct {
	ControllerID string
	VNI          uint32
	Segment      string   // overlay name, for diagnostics
	BridgeName   string   // interface the advertisement is sent on
	BridgeIPv4   string   // CIDR; its address is the default next-hop
	Tags         []string // bridge tags, matched against the inject rules
	Inject       config.InjectConfig
	PSK          []byte
	KeyID        string
}

// Publisher advertises routes on one bridge segment.
//
// A Publisher owns its transport and sequence counter and is safe for
// concurrent use: Run and Advertise may be called from different goroutines.
type Publisher struct {
	cfg       PublisherConfig
	rules     []config.InjectRule
	routes    RouteSource
	transport Transport
	logger    *slog.Logger
	metrics   *observability.Metrics
	vniLabel  string

	mu  sync.Mutex
	seq uint64
}

// Option configures a Publisher.
type Option func(*Publisher)

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option {
	return func(p *Publisher) { p.logger = l }
}

// WithMetrics sets the metrics sink.
func WithMetrics(m *observability.Metrics) Option {
	return func(p *Publisher) { p.metrics = m }
}

// WithTransport overrides the transport, used by tests to avoid a real socket.
func WithTransport(t Transport) Option {
	return func(p *Publisher) { p.transport = t }
}

// WithRouteSource sets where RIB-derived routes come from.
func WithRouteSource(s RouteSource) Option {
	return func(p *Publisher) { p.routes = s }
}

// NewPublisher creates a publisher for one bridge.
//
// It returns nil (with no error) when no inject rule matches the bridge's tags:
// that bridge simply is not part of the injection channel.
func NewPublisher(cfg PublisherConfig, opts ...Option) (*Publisher, error) {
	rules := cfg.Inject.MatchingRules(cfg.Tags)
	if len(rules) == 0 {
		return nil, nil
	}

	p := &Publisher{
		cfg:      cfg,
		rules:    rules,
		logger:   slog.Default(),
		vniLabel: strconv.FormatUint(uint64(cfg.VNI), 10),
	}
	for _, opt := range opts {
		opt(p)
	}

	if len(cfg.PSK) == 0 {
		return nil, fmt.Errorf("inject publisher for %s: no psk", cfg.BridgeName)
	}

	// Fail here rather than at the first tick, so a misconfigured channel is
	// reported while the daemon is still starting up.
	if _, err := p.defaultNextHop(); err != nil {
		return nil, err
	}
	if _, err := p.resolveDefaultGateway(); err != nil {
		return nil, err
	}

	if p.transport == nil {
		base := net.ParseIP(cfg.Inject.GetGroupBase())
		if base == nil {
			return nil, fmt.Errorf("inject publisher for %s: invalid group_base %q", cfg.BridgeName, cfg.Inject.GetGroupBase())
		}
		group, err := GroupForVNI(base, cfg.VNI)
		if err != nil {
			return nil, fmt.Errorf("inject publisher for %s: %w", cfg.BridgeName, err)
		}
		transport, err := NewMulticastTransport(cfg.BridgeName, group, cfg.Inject.GetPort())
		if err != nil {
			return nil, fmt.Errorf("inject publisher for %s: %w", cfg.BridgeName, err)
		}
		p.transport = transport
	}

	return p, nil
}

// Run publishes an advertisement immediately and then on every interval until
// the context is cancelled. A failed cycle is logged and retried on the next
// tick rather than tearing the publisher down.
func (p *Publisher) Run(ctx context.Context) error {
	interval := p.cfg.Inject.GetInterval()

	if err := p.Advertise(); err != nil {
		p.logger.Error("inject advertisement failed",
			"bridge", p.cfg.BridgeName, "vni", p.cfg.VNI, "error", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := p.Advertise(); err != nil {
				p.logger.Error("inject advertisement failed",
					"bridge", p.cfg.BridgeName, "vni", p.cfg.VNI, "error", err)
			}
		}
	}
}

// Advertise builds, seals and sends a single advertisement.
func (p *Publisher) Advertise() error {
	adv, err := p.buildAdvertisement(time.Now())
	if err != nil {
		p.countError("build")
		return err
	}

	datagram, err := Seal(adv, p.cfg.PSK, p.cfg.KeyID)
	if err != nil {
		// An oversized advertisement is reported and nothing is sent, so agents
		// keep their previous route set until it expires rather than converging
		// on a silently truncated one.
		p.countError("seal")
		return err
	}

	if err := p.transport.Send(datagram); err != nil {
		p.countError("send")
		return fmt.Errorf("failed to send advertisement on %s: %w", p.cfg.BridgeName, err)
	}

	if p.metrics != nil {
		p.metrics.InjectAdvertisementsSent.WithLabelValues(p.vniLabel).Inc()
		p.metrics.InjectRoutesAdvertised.WithLabelValues(p.vniLabel).Set(float64(len(adv.GetRoutes())))
		p.metrics.InjectLastAdvertisement.WithLabelValues(p.vniLabel).Set(float64(time.Now().Unix()))
	}

	p.logger.Debug("published inject advertisement",
		"bridge", p.cfg.BridgeName, "vni", p.cfg.VNI,
		"routes", len(adv.GetRoutes()), "sequence", adv.GetSequence())

	return nil
}

// Close releases the transport.
func (p *Publisher) Close() error {
	if p.transport == nil {
		return nil
	}
	return p.transport.Close()
}

// Rules exposes the rules that matched this bridge, for status reporting.
func (p *Publisher) Rules() []config.InjectRule { return p.rules }

// buildAdvertisement assembles the route set for this segment.
//
// The advertised set is the union of every matching rule. Routes are keyed by
// (prefix, next-hop) and the lowest metric wins, then sorted so a stable
// configuration produces a byte-stable datagram.
func (p *Publisher) buildAdvertisement(now time.Time) (*pb.Advertisement, error) {
	nextHop, err := p.defaultNextHop()
	if err != nil {
		return nil, err
	}

	type routeKey struct{ prefix, nextHop string }
	collected := make(map[routeKey]uint32)

	add := func(prefix, hop string, metric uint32) {
		if prefix == "" || hop == "" {
			return
		}
		if metric == 0 {
			metric = defaultAdvertisedMetric
		}
		k := routeKey{prefix: prefix, nextHop: hop}
		if existing, ok := collected[k]; ok && existing <= metric {
			return
		}
		collected[k] = metric
	}

	for _, rule := range p.rules {
		hop := nextHop
		if rule.NextHop != "" {
			hop = rule.NextHop
		}

		for _, prefix := range rule.Networks {
			add(prefix, hop, uint32(rule.Metric))
		}

		if rule.FromRIB && p.routes != nil {
			for _, r := range p.routes.InjectableRoutes() {
				if r.VNI != p.cfg.VNI {
					continue
				}
				if !tagsIntersect(r.Tags, rule.RouteTags) {
					continue
				}
				// The VM's next-hop is this host on the segment, not the peer
				// that originated the prefix: the host is what carries the
				// packet across the overlay.
				metric := uint32(rule.Metric)
				if metric == 0 {
					metric = r.Metric
				}
				add(r.Prefix, hop, metric)
			}
		}
	}

	routes := make([]*pb.InjectedRoute, 0, len(collected))
	for k, metric := range collected {
		routes = append(routes, &pb.InjectedRoute{
			Prefix:  k.prefix,
			NextHop: k.nextHop,
			Metric:  metric,
		})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Prefix != routes[j].Prefix {
			return routes[i].Prefix < routes[j].Prefix
		}
		return routes[i].NextHop < routes[j].NextHop
	})

	gateway, err := p.resolveDefaultGateway()
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	p.seq++
	seq := p.seq
	p.mu.Unlock()

	return &pb.Advertisement{
		ControllerId:   p.cfg.ControllerID,
		Vni:            p.cfg.VNI,
		Segment:        p.cfg.Segment,
		Tags:           p.cfg.Tags,
		TimestampMs:    now.UnixMilli(),
		Sequence:       seq,
		Generation:     seq,
		LeaseSeconds:   uint32(p.cfg.Inject.GetLeaseSeconds()),
		Routes:         routes,
		DefaultGateway: gateway,
	}, nil
}

// defaultNextHop is this host's address on the segment.
//
// It is only required when a matching rule omits next_hop; config validation
// already rejects that combination, and this repeats the check so the publisher
// is safe to construct directly.
func (p *Publisher) defaultNextHop() (string, error) {
	if p.cfg.BridgeIPv4 != "" {
		ip, _, err := net.ParseCIDR(p.cfg.BridgeIPv4)
		if err != nil {
			return "", fmt.Errorf("bridge %s: invalid ipv4 %q: %w", p.cfg.BridgeName, p.cfg.BridgeIPv4, err)
		}
		return ip.String(), nil
	}

	for _, rule := range p.rules {
		if rule.NextHop == "" {
			return "", fmt.Errorf("bridge %s: no bridge.ipv4 and a matching rule has no next_hop", p.cfg.BridgeName)
		}
	}
	return "", nil
}

// resolveDefaultGateway returns the gateway advertised for this segment.
//
// Two matching rules offering different gateways is a contradiction the
// operator has to resolve, so it is an error rather than a silent last-one-wins.
func (p *Publisher) resolveDefaultGateway() (string, error) {
	gateway := ""
	for _, rule := range p.rules {
		if rule.DefaultGateway == "" {
			continue
		}
		if gateway != "" && gateway != rule.DefaultGateway {
			return "", fmt.Errorf("bridge %s: matching inject rules advertise conflicting default gateways (%s and %s)",
				p.cfg.BridgeName, gateway, rule.DefaultGateway)
		}
		gateway = rule.DefaultGateway
	}
	return gateway, nil
}

func (p *Publisher) countError(reason string) {
	if p.metrics != nil {
		p.metrics.InjectPublishErrors.WithLabelValues(p.vniLabel, reason).Inc()
	}
}

// tagsIntersect reports whether a route carries any of the wanted communities.
// An empty want list means the rule does not filter on communities.
func tagsIntersect(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		for _, h := range have {
			if h == w {
				return true
			}
		}
	}
	return false
}

// multicastTransport sends datagrams out one bridge.
type multicastTransport struct {
	conn *ipv4.PacketConn
	raw  net.PacketConn
	dst  *net.UDPAddr
}

// NewMulticastTransport binds a sender to a specific interface.
//
// TTL is pinned to 1 so an advertisement never leaves the L2 segment, and
// loopback is disabled because the publisher is not its own audience.
func NewMulticastTransport(ifname string, group net.IP, port int) (Transport, error) {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", ifname, err)
	}

	raw, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("failed to open multicast socket: %w", err)
	}

	conn := ipv4.NewPacketConn(raw)
	if err := conn.SetMulticastInterface(iface); err != nil {
		raw.Close()
		return nil, fmt.Errorf("failed to bind multicast sender to %s: %w", ifname, err)
	}
	if err := conn.SetMulticastTTL(1); err != nil {
		raw.Close()
		return nil, fmt.Errorf("failed to set multicast TTL: %w", err)
	}
	if err := conn.SetMulticastLoopback(false); err != nil {
		raw.Close()
		return nil, fmt.Errorf("failed to disable multicast loopback: %w", err)
	}

	return &multicastTransport{
		conn: conn,
		raw:  raw,
		dst:  &net.UDPAddr{IP: group, Port: port},
	}, nil
}

func (t *multicastTransport) Send(datagram []byte) error {
	if _, err := t.raw.WriteTo(datagram, t.dst); err != nil {
		return fmt.Errorf("failed to write to %s: %w", t.dst, err)
	}
	return nil
}

func (t *multicastTransport) Close() error { return t.raw.Close() }
