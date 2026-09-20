package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	pb "github.com/nishisan-dev/n-netman/api/inject/v1"
	"github.com/nishisan-dev/n-netman/internal/agentconfig"
	"github.com/nishisan-dev/n-netman/internal/inject"
	nlink "github.com/nishisan-dev/n-netman/internal/netlink"
	"github.com/nishisan-dev/n-netman/internal/observability"
	"github.com/nishisan-dev/n-netman/internal/routepolicy"
)

// defaultRoutePrefix is the destination an advertised default gateway installs.
const defaultRoutePrefix = "0.0.0.0/0"

// expiryInterval is how often leases are checked. It is well below the shortest
// sensible lease, so a controller going away is noticed promptly.
const expiryInterval = time.Second

// Agent runs one segment listener per configured interface.
type Agent struct {
	cfg      *agentconfig.Config
	logger   *slog.Logger
	metrics  *observability.AgentMetrics
	segments []*segment
}

// New builds an agent from its configuration.
func New(cfg *agentconfig.Config, logger *slog.Logger, metrics *observability.AgentMetrics) (*Agent, error) {
	a := &Agent{cfg: cfg, logger: logger, metrics: metrics}

	for _, ifaceCfg := range cfg.Interfaces {
		seg, err := newSegment(cfg, ifaceCfg, logger, metrics)
		if err != nil {
			return nil, err
		}
		a.segments = append(a.segments, seg)
	}

	return a, nil
}

// Run drives every segment until the context is cancelled, then withdraws the
// routes it installed.
func (a *Agent) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, seg := range a.segments {
		wg.Add(1)
		go func(s *segment) {
			defer wg.Done()
			if err := s.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.logger.Error("segment stopped", "interface", s.cfg.Name, "error", err)
			}
		}(seg)
	}

	wg.Wait()
	return nil
}

// Withdraw removes every route the agent installed. Routes are scoped by
// protocol, so nothing installed by netplan or by hand is touched.
func (a *Agent) Withdraw() error {
	var errs []error
	for _, seg := range a.segments {
		if err := seg.withdraw(); err != nil {
			errs = append(errs, fmt.Errorf("interface %s: %w", seg.cfg.Name, err))
		}
	}
	return errors.Join(errs...)
}

// Status reports what each interface currently holds.
func (a *Agent) Status() []InterfaceStatus {
	now := time.Now()
	out := make([]InterfaceStatus, 0, len(a.segments))
	for _, seg := range a.segments {
		out = append(out, InterfaceStatus{
			Interface:   seg.cfg.Name,
			Address:     seg.cfg.Address,
			Group:       seg.group.String(),
			Port:        seg.port,
			ExpectTags:  seg.cfg.ExpectTags,
			Table:       seg.table,
			Controllers: seg.state.Controllers(now),
			Routes:      seg.state.Desired(now),
		})
	}
	return out
}

// Healthy reports whether every interface still hears from a controller.
func (a *Agent) Healthy() bool {
	now := time.Now()
	for _, seg := range a.segments {
		if !seg.state.HasLiveController(now) {
			return false
		}
	}
	return len(a.segments) > 0
}

// InterfaceStatus is the /status payload for one interface.
type InterfaceStatus struct {
	Interface   string             `json:"interface"`
	Address     string             `json:"address"`
	Group       string             `json:"group"`
	Port        int                `json:"port"`
	ExpectTags  []string           `json:"expect_tags"`
	Table       int                `json:"table"`
	Controllers []ControllerStatus `json:"controllers"`
	Routes      []Route            `json:"routes"`
}

// segment couples one interface to the channel it listens on.
type segment struct {
	cfg      agentconfig.InterfaceConfig
	group    net.IP
	port     int
	table    int
	metric   uint32
	policy   routepolicy.Policy
	acceptGW bool

	state    *SegmentState
	guard    *inject.ReplayGuard
	listener *inject.Listener
	routes   *nlink.RouteManager
	addrs    *nlink.AddrManager

	logger  *slog.Logger
	metrics *observability.AgentMetrics

	// notify carries a single pending reconcile; a full channel already means
	// one is queued, so bursts collapse instead of piling up.
	notify chan struct{}
}

func newSegment(
	cfg *agentconfig.Config,
	ifaceCfg agentconfig.InterfaceConfig,
	logger *slog.Logger,
	metrics *observability.AgentMetrics,
) (*segment, error) {
	group, err := cfg.GroupFor(ifaceCfg)
	if err != nil {
		return nil, err
	}

	psk, err := inject.LoadPSK(ifaceCfg.PSKRef)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", ifaceCfg.Name, err)
	}
	keys, err := inject.NewKeyRing(psk, ifaceCfg.KeyIDs)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", ifaceCfg.Name, err)
	}

	// A table of 0 in configuration means the main table; resolve it here so
	// reads and writes agree on which table that is.
	table := ifaceCfg.Install.Table
	if table == 0 {
		table = nlink.RouteTableMain
	}

	s := &segment{
		cfg:      ifaceCfg,
		group:    group,
		port:     cfg.PortFor(ifaceCfg),
		table:    table,
		metric:   uint32(ifaceCfg.Install.GetMetric()),
		policy:   ifaceCfg.Import.Policy(),
		acceptGW: ifaceCfg.Install.AcceptDefaultGateway,
		state:    NewSegmentState(),
		guard:    inject.NewReplayGuard(inject.ReplayWindow),
		routes:   nlink.NewRouteManager(),
		addrs:    nlink.NewAddrManager(),
		logger:   logger.With("interface", ifaceCfg.Name),
		metrics:  metrics,
		notify:   make(chan struct{}, 1),
	}

	s.listener, err = inject.NewListener(inject.ListenerConfig{
		Interface:  ifaceCfg.Name,
		Group:      group,
		Port:       s.port,
		Keys:       keys,
		ExpectTags: ifaceCfg.ExpectTags,
	},
		// The undecorated logger: the listener names the interface itself, and
		// passing s.logger would print the attribute twice.
		inject.WithListenerLogger(logger),
		inject.WithRejectHandler(s.onReject),
	)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %w", ifaceCfg.Name, err)
	}

	return s, nil
}

func (s *segment) run(ctx context.Context) error {
	// Phase 1 is static addressing: the address is applied here, after netplan
	// has had its turn, and is what makes the segment usable at all.
	if err := s.addrs.SetUp(s.cfg.Name); err != nil {
		s.logger.Warn("could not bring the interface up", "error", err)
	}
	if err := s.addrs.Ensure(s.cfg.Name, s.cfg.Address); err != nil {
		return fmt.Errorf("failed to configure %s on %s: %w", s.cfg.Address, s.cfg.Name, err)
	}
	s.logger.Info("interface configured", "address", s.cfg.Address, "group", s.group, "port", s.port)

	go func() {
		if err := s.listener.Run(ctx, s.guard, s.onAdvertisement); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Error("listener stopped", "error", err)
		}
	}()

	ticker := time.NewTicker(expiryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-s.notify:
			s.reconcile()

		case <-ticker.C:
			// An expired controller changes the desired set, so only reconcile
			// when something actually went away.
			if expired := s.state.Expire(time.Now()); len(expired) > 0 {
				for _, id := range expired {
					s.logger.Info("controller lease expired, withdrawing its routes", "controller", id)
					s.guard.Forget(id)
				}
				s.reconcile()
			}
		}
	}
}

// onAdvertisement converts an authenticated advertisement into an update and
// records it. Authentication and tag matching already happened in the listener.
func (s *segment) onAdvertisement(adv *pb.Advertisement) {
	now := time.Now()

	routes := make([]Route, 0, len(adv.GetRoutes()))
	for _, r := range adv.GetRoutes() {
		if r.GetNextHop() == "" {
			continue
		}
		if !s.policy.Admits(r.GetPrefix()) {
			s.countReject("policy")
			continue
		}
		metric := r.GetMetric()
		if metric == 0 {
			metric = s.metric
		}
		routes = append(routes, Route{Prefix: r.GetPrefix(), NextHop: r.GetNextHop(), Metric: metric})
	}

	// accept_default_gateway is the opt-in for the default route, and is not
	// filtered through the allow list: a policy listing only the specific
	// prefixes it wants would otherwise drop the gateway silently, making the
	// flag look broken.
	if s.acceptGW && adv.GetDefaultGateway() != "" {
		routes = append(routes, Route{
			Prefix:  defaultRoutePrefix,
			NextHop: adv.GetDefaultGateway(),
			Metric:  s.metric,
		})
	}

	s.state.Apply(Update{
		ControllerID: adv.GetControllerId(),
		Segment:      adv.GetSegment(),
		VNI:          adv.GetVni(),
		Tags:         adv.GetTags(),
		Routes:       routes,
		Lease:        time.Duration(adv.GetLeaseSeconds()) * time.Second,
	}, now)

	if s.metrics != nil {
		s.metrics.AdvertisementsReceived.WithLabelValues(s.cfg.Name).Inc()
		s.metrics.LastAdvertisement.WithLabelValues(s.cfg.Name).Set(float64(now.Unix()))
	}

	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// reconcile programs the union of every live controller's routes.
func (s *segment) reconcile() {
	now := time.Now()
	desired := s.state.Desired(now)

	configs := make([]nlink.RouteConfig, 0, len(desired))
	for _, r := range desired {
		_, dst, err := net.ParseCIDR(r.Prefix)
		if err != nil {
			s.logger.Warn("skipping an advertised route with an invalid prefix", "prefix", r.Prefix, "error", err)
			continue
		}
		gw := net.ParseIP(r.NextHop)
		if gw == nil {
			s.logger.Warn("skipping an advertised route with an invalid next-hop", "prefix", r.Prefix, "next_hop", r.NextHop)
			continue
		}
		configs = append(configs, nlink.RouteConfig{
			Destination: dst,
			Gateway:     gw,
			Table:       s.table,
			Metric:      int(r.Metric),
			Protocol:    nlink.RouteProtocolNNetAgent,
		})
	}

	if err := s.routes.Sync(s.table, nlink.RouteProtocolNNetAgent, configs); err != nil {
		s.logger.Error("failed to program routes", "error", err)
		if s.metrics != nil {
			s.metrics.RouteSyncErrors.WithLabelValues(s.cfg.Name).Inc()
		}
		return
	}

	if s.metrics != nil {
		s.metrics.RoutesInstalled.WithLabelValues(s.cfg.Name).Set(float64(len(configs)))
		s.metrics.ControllersActive.WithLabelValues(s.cfg.Name).Set(float64(len(s.state.Controllers(now))))
	}

	s.logger.Debug("routes reconciled", "routes", len(configs), "table", s.table)
}

func (s *segment) withdraw() error {
	if err := s.routes.FlushByProtocol(s.table, nlink.RouteProtocolNNetAgent); err != nil {
		return err
	}
	s.logger.Info("withdrew injected routes", "table", s.table)
	return nil
}

func (s *segment) onReject(reason string, err error) {
	if s.metrics != nil {
		s.metrics.AdvertisementsRejected.WithLabelValues(s.cfg.Name, reason).Inc()
	}
}

func (s *segment) countReject(reason string) {
	if s.metrics != nil {
		s.metrics.AdvertisementsRejected.WithLabelValues(s.cfg.Name, reason).Inc()
	}
}
