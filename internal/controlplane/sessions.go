package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	pb "github.com/nishisan-dev/n-netman/api/v1"
	"github.com/nishisan-dev/n-netman/internal/config"
	nl "github.com/nishisan-dev/n-netman/internal/netlink"
	"github.com/nishisan-dev/n-netman/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type SessionState string

const (
	SessionInit       SessionState = "INIT"
	SessionProbing    SessionState = "PROBING"
	SessionRecovering SessionState = "RECOVERING"
	SessionUp         SessionState = "UP"
	SessionSuspect    SessionState = "SUSPECT"
	SessionDown       SessionState = "DOWN"
)

// SessionHealth is deterministic: only a validated application ACK counts.
type SessionHealth struct {
	State             SessionState
	Successes, Misses int
	Eligible          bool
}

func (h *SessionHealth) Observe(ok bool, cfg config.SessionHealth) {
	if ok {
		h.Misses = 0
		h.Successes++
		if h.Eligible || h.Successes >= cfg.UpAfterSuccesses {
			h.State = SessionUp
			h.Eligible = true
		} else {
			h.State = SessionRecovering
		}
		return
	}
	h.Successes = 0
	h.Misses++
	if h.Misses >= cfg.DownAfterMisses {
		h.State = SessionDown
		h.Eligible = false
	} else if h.Eligible {
		h.State = SessionSuspect
	} else {
		h.State = SessionProbing
	}
}

type sessionPath struct {
	peer              config.PeerConfig
	cfg               config.PeerSession
	health            SessionHealth
	ads               map[string]time.Time
	rxEpoch           string
	rxTransport       string
	rxGeneration      uint64
	rxSequence        uint64
	txIdentity        *pb.SessionIdentity
	client            pb.NNetManClient
	exportNetworks    []string
	retired           map[string]time.Time
	rtt               time.Duration
	overlayReady      bool
	underlayReachable bool
	lastSeen          time.Time
	lastError         string
}

func pathKey(peerID, session string) string { return peerID + "/" + session }
func newEpoch() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}

type pathMetrics struct {
	health     *prometheus.GaugeVec
	state      *prometheus.GaugeVec
	rtt        *prometheus.HistogramVec
	timeouts   *prometheus.CounterVec
	failovers  *prometheus.CounterVec
	candidates *prometheus.GaugeVec
	errors     prometheus.Counter
}

func newPathMetrics(reg prometheus.Registerer) *pathMetrics {
	m := &pathMetrics{
		health:     prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nnetman_transport_health", Help: "Application health of each transport."}, []string{"transport", "peer"}),
		state:      prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nnetman_peer_session_state", Help: "One-hot session state."}, []string{"peer", "session", "transport", "state"}),
		rtt:        prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "nnetman_peer_heartbeat_rtt_seconds", Help: "Validated application ACK RTT."}, []string{"peer", "session"}),
		timeouts:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nnetman_peer_heartbeat_timeouts_total", Help: "Application heartbeat timeouts."}, []string{"peer", "session"}),
		failovers:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "nnetman_failovers_total", Help: "Changes to selected route path."}, []string{"peer", "from", "to", "reason"}),
		candidates: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "nnetman_route_candidates", Help: "Configured candidates grouped by state (no prefix labels)."}, []string{"state"}),
		errors:     prometheus.NewCounter(prometheus.CounterOpts{Name: "nnetman_route_reconcile_errors_total", Help: "Failed candidate reconciliation."}),
	}
	m.health = registerPathMetric(reg, m.health)
	m.state = registerPathMetric(reg, m.state)
	m.rtt = registerPathMetric(reg, m.rtt)
	m.timeouts = registerPathMetric(reg, m.timeouts)
	m.failovers = registerPathMetric(reg, m.failovers)
	m.candidates = registerPathMetric(reg, m.candidates)
	m.errors = registerPathMetric(reg, m.errors)
	return m
}
func registerPathMetric[T prometheus.Collector](reg prometheus.Registerer, c T) T {
	if e := reg.Register(c); e != nil {
		var existing prometheus.AlreadyRegisteredError
		if errors.As(e, &existing) {
			return existing.ExistingCollector.(T)
		}
		panic(e)
	}
	return c
}

// SessionManager implements v3 on the same gRPC service as the legacy client.
// Session state and received prefixes are never keyed only by peer or VNI.
type SessionManager struct {
	mu        sync.Mutex
	cfg       *config.Config
	paths     map[string]*sessionPath
	listeners map[string]*grpc.Server
	fib       *nl.CandidateManager
	log       *slog.Logger
	metrics   *pathMetrics
	selected  map[string]string
	installed map[string]nl.PathRoute
	wg        sync.WaitGroup
}

func NewSessionManager(cfg *config.Config, fib *nl.CandidateManager, logger *slog.Logger, reg prometheus.Registerer) *SessionManager {
	m := &SessionManager{cfg: cfg, paths: map[string]*sessionPath{}, listeners: map[string]*grpc.Server{}, fib: fib, log: logger, selected: map[string]string{}, installed: map[string]nl.PathRoute{}}
	if reg != nil {
		m.metrics = newPathMetrics(reg)
	}
	for _, p := range cfg.Peers {
		for _, s := range p.Sessions {
			m.paths[pathKey(p.ID, s.ID)] = &sessionPath{peer: p, cfg: s, health: SessionHealth{State: SessionInit}, ads: map[string]time.Time{}, exportNetworks: append([]string(nil), p.Routing.Export.Networks...), txIdentity: &pb.SessionIdentity{SessionId: s.ID, TransportId: s.Transport, Epoch: newEpoch(), Generation: 1}, retired: map[string]time.Time{}}
		}
	}
	return m
}

// Run retries listeners independently (managed overlays may not exist at boot).
// Cleanup removes only journaled exact candidates, including after a restart.
func (m *SessionManager) Run(ctx context.Context) error {
	if e := m.fib.SyncCandidates(nil); e != nil {
		return fmt.Errorf("startup candidate cleanup: %w", e)
	}
	m.ensureListeners(ctx)
	for _, p := range m.paths {
		m.wg.Add(1)
		go func(p *sessionPath) { defer m.wg.Done(); m.runPath(ctx, p) }(p)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer func() {
		m.mu.Lock()
		for _, s := range m.listeners {
			s.Stop()
		}
		m.mu.Unlock()
		m.wg.Wait()
	}()
	for {
		m.ensureListeners(ctx)
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, server := range m.listeners {
				server.Stop()
			}
			m.mu.Unlock()
			m.wg.Wait()
			return m.fib.SyncCandidates(nil)
		case <-ticker.C:
			m.mu.Lock()
			m.reconcileLocked("lease/reconcile")
			m.mu.Unlock()
		}
	}
}
func (m *SessionManager) ensureListeners(ctx context.Context) {
	for _, p := range m.paths {
		address := net.JoinHostPort(p.cfg.ListenIP, strconv.Itoa(p.cfg.Port))
		m.mu.Lock()
		_, exists := m.listeners[address]
		m.mu.Unlock()
		if exists {
			continue
		}
		if e := nl.ValidatePath(p.cfg.BindDevice, p.cfg.LocalIP, p.cfg.RemoteIP, true); e != nil {
			continue
		}
		lc := net.ListenConfig{Control: nl.BindDevice(p.cfg.BindDevice)}
		lis, e := lc.Listen(ctx, "tcp4", address)
		if e != nil {
			m.log.Warn("session listener unavailable", "address", address, "error", e)
			continue
		}
		opts := []grpc.ServerOption{grpc.MaxRecvMsgSize(1 << 20)}
		if m.cfg.Security.ControlPlane.TLS.Enabled {
			creds, e := LoadServerTLSConfig(&m.cfg.Security.ControlPlane.TLS)
			if e != nil {
				lis.Close()
				m.log.Error("listener TLS failed", "error", e)
				continue
			}
			opts = append(opts, grpc.Creds(creds))
		}
		server := grpc.NewServer(opts...)
		pb.RegisterNNetManServer(server, &pathServer{manager: m, listen: address})
		m.mu.Lock()
		m.listeners[address] = server
		m.mu.Unlock()
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if e := server.Serve(lis); e != nil {
				m.log.Warn("session listener stopped", "address", address, "error", e)
			}
			m.mu.Lock()
			delete(m.listeners, address)
			m.mu.Unlock()
		}()
	}
}
func (m *SessionManager) connect(ctx context.Context, p *sessionPath) (*grpc.ClientConn, error) {
	var creds credentials.TransportCredentials = insecure.NewCredentials()
	if m.cfg.Security.ControlPlane.TLS.Enabled {
		var e error
		creds, e = LoadClientTLSConfig(&m.cfg.Security.ControlPlane.TLS, p.peer.ID)
		if e != nil {
			return nil, e
		}
	}
	address := net.JoinHostPort(p.cfg.RemoteIP, strconv.Itoa(p.cfg.Port))
	return grpc.NewClient("passthrough:///"+address, grpc.WithTransportCredentials(creds), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return nl.DialPath(ctx, p.cfg, address) }), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(1<<20)))
}
func (m *SessionManager) exports(p *sessionPath) []*pb.Route {
	var routes []*pb.Route
	for _, prefix := range p.exportNetworks {
		routes = append(routes, &pb.Route{Prefix: prefix, LeaseSeconds: 30})
	}
	return routes
}
func validateSnapshot(routes []*pb.Route) error {
	if len(routes) > 4096 {
		return fmt.Errorf("too many routes")
	}
	seen := map[string]bool{}
	for _, r := range routes {
		if r == nil {
			return fmt.Errorf("nil route")
		}
		_, network, e := net.ParseCIDR(r.Prefix)
		if e != nil || network.IP.To4() == nil || network.String() != r.Prefix || r.LeaseSeconds < 1 || r.LeaseSeconds > 3600 || seen[r.Prefix] {
			return fmt.Errorf("invalid/duplicate prefix or lease")
		}
		seen[r.Prefix] = true
	}
	return nil
}
func (m *SessionManager) snapshotLocked(p *sessionPath, routes []*pb.Route, replace bool) {
	if replace {
		p.ads = map[string]time.Time{}
	}
	for _, r := range routes {
		if config.AllowsPrefix(p.peer.Routing.Import, r.Prefix) {
			p.ads[r.Prefix] = time.Now().Add(time.Duration(r.LeaseSeconds) * time.Second)
		}
	}
}

// receiveSnapshotLocked merges both RPC directions using the exporting node's
// epoch and generation. Older snapshots cannot resurrect a later withdrawal.
func (m *SessionManager) receiveSnapshotLocked(p *sessionPath, id *pb.SessionIdentity, routes []*pb.Route) (bool, error) {
	if id == nil || len(id.Epoch) != 32 || id.SessionId != p.cfg.RemoteSession() || id.TransportId == "" || (p.cfg.RemoteTransportID != "" && id.TransportId != p.cfg.RemoteTransportID) {
		return false, fmt.Errorf("advertisement identity mismatch")
	}
	if p.retired == nil {
		p.retired = map[string]time.Time{}
	}
	if id.Epoch != p.rxEpoch {
		if _, old := p.retired[id.Epoch]; old {
			return false, fmt.Errorf("retired advertisement epoch")
		}
		for epoch, at := range p.retired {
			if time.Since(at) > time.Hour {
				delete(p.retired, epoch)
			}
		}
		if len(p.retired) >= 4096 {
			return false, fmt.Errorf("session epoch churn limit exceeded")
		}
		if p.rxEpoch != "" {
			p.retired[p.rxEpoch] = time.Now()
		}
		p.rxEpoch, p.rxTransport, p.rxGeneration, p.rxSequence = id.Epoch, id.TransportId, 0, 0
	} else if p.rxTransport != id.TransportId {
		return false, fmt.Errorf("transport changed within epoch")
	}
	if id.Generation < p.rxGeneration {
		return false, nil
	}
	p.rxGeneration = id.Generation
	m.snapshotLocked(p, routes, true)
	return true, nil
}

func (m *SessionManager) observe(p *sessionPath, ok bool, rtt time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := p.health.State
	p.health.Observe(ok, p.cfg.Health)
	if ok {
		p.rtt = rtt
	}
	reason := "valid_ack"
	if err != nil {
		p.lastError = err.Error()
		reason = p.lastError
	} else {
		p.lastError = ""
		p.lastSeen = time.Now()
	}
	if p.health.State == SessionDown {
		p.ads = map[string]time.Time{}
	}
	if prev != p.health.State {
		m.log.Info("peer session transition", "peer_id", p.peer.ID, "session_id", p.cfg.ID, "transport_id", p.cfg.Transport, "previous_state", prev, "new_state", p.health.State, "reason", reason)
	}
	if m.metrics != nil {
		healthy := 0.
		if p.health.Eligible {
			healthy = 1
		}
		m.metrics.health.WithLabelValues(p.cfg.Transport, p.peer.ID).Set(healthy)
		for _, state := range []SessionState{SessionInit, SessionProbing, SessionRecovering, SessionUp, SessionSuspect, SessionDown} {
			value := 0.
			if state == p.health.State {
				value = 1
			}
			m.metrics.state.WithLabelValues(p.peer.ID, p.cfg.ID, p.cfg.Transport, string(state)).Set(value)
		}
		if ok {
			m.metrics.rtt.WithLabelValues(p.peer.ID, p.cfg.ID).Observe(rtt.Seconds())
		} else if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded {
			m.metrics.timeouts.WithLabelValues(p.peer.ID, p.cfg.ID).Inc()
		}
	}
	m.reconcileLocked(reason)
}
func validACK(a *pb.KeepaliveResponse, node string, id *pb.SessionIdentity, seq uint64) bool {
	return a != nil && a.NodeId == node && a.Sequence == seq && proto.Equal(a.Session, id)
}

// A stream is scoped to one HELLO/ACK deadline. The TCP/gRPC connection stays
// open between probes; a nonresponsive application cannot keep it healthy.
func probeApplicationACK(ctx context.Context, client pb.NNetManClient, local, remote string, id *pb.SessionIdentity, seq uint64) error {
	stream, e := client.Keepalive(ctx)
	if e != nil {
		return e
	}
	defer stream.CloseSend()
	if e = stream.Send(&pb.KeepaliveRequest{NodeId: local, Sequence: seq, Session: id, TimestampMs: time.Now().UnixMilli()}); e != nil {
		return e
	}
	ack, e := stream.Recv()
	if e != nil {
		return e
	}
	if !validACK(ack, remote, id, seq) {
		return fmt.Errorf("stale or mismatched application ACK")
	}
	return nil
}

func (m *SessionManager) runPath(ctx context.Context, p *sessionPath) {
	interval := time.Duration(p.cfg.Health.IntervalMS) * time.Millisecond
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var conn *grpc.ClientConn
	var client pb.NNetManClient
	var identity *pb.SessionIdentity
	var seq uint64
	var refreshed time.Time
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()
	// Reconnect cadence is bounded by the configured interval, with per-attempt
	// jitter up to 10%. No gRPC background retry is counted as application health.
	for {
		started := time.Now()
		err := m.validatePath(p)
		address := net.JoinHostPort(p.cfg.ListenIP, strconv.Itoa(p.cfg.Port))
		m.mu.Lock()
		_, listening := m.listeners[address]
		m.mu.Unlock()
		if !listening {
			err = errors.Join(err, fmt.Errorf("local listener unavailable on %s", address))
		}
		probeCtx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.Health.AckTimeoutMS)*time.Millisecond)
		if err == nil && conn == nil {
			conn, err = m.connect(probeCtx, p)
			if err == nil {
				client = pb.NewNNetManClient(conn)
				identity = &pb.SessionIdentity{SessionId: p.cfg.ID, TransportId: p.cfg.Transport, Epoch: newEpoch(), Generation: 1}
				m.mu.Lock()
				p.txIdentity = proto.Clone(identity).(*pb.SessionIdentity)
				p.client = client
				m.mu.Unlock()
				refreshed = time.Time{}
			}
		}
		if err == nil && time.Since(refreshed) >= 10*time.Second {
			m.mu.Lock()
			p.txIdentity.Generation++
			identity = proto.Clone(p.txIdentity).(*pb.SessionIdentity)
			exports := m.exports(p)
			m.mu.Unlock()
			var response *pb.StateResponse
			response, err = client.ExchangeState(probeCtx, &pb.StateRequest{NodeId: m.cfg.Node.ID, Routes: exports, Session: identity, TimestampMs: time.Now().UnixMilli()})
			if err == nil {
				if response.NodeId != p.peer.ID || !response.Accepted || !proto.Equal(response.Session, identity) {
					err = fmt.Errorf("state response identity mismatch")
				} else {
					err = validateSnapshot(response.Routes)
				}
			}
			if err == nil {
				m.mu.Lock()
				_, err = m.receiveSnapshotLocked(p, response.Advertisement, response.Routes)
				m.mu.Unlock()
				if err == nil {
					refreshed = time.Now()
				}
			}
		}
		if err == nil {
			m.mu.Lock()
			identity = proto.Clone(p.txIdentity).(*pb.SessionIdentity)
			m.mu.Unlock()
			seq++
			started = time.Now()
			err = probeApplicationACK(probeCtx, client, m.cfg.Node.ID, p.peer.ID, identity, seq)
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		m.observe(p, err == nil, time.Since(started), err)
		if err != nil && conn != nil {
			conn.Close()
			m.mu.Lock()
			p.client = nil
			m.mu.Unlock()
			conn = nil
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err != nil {
			var b [1]byte
			_, _ = rand.Read(b[:])
			timer := time.NewTimer(time.Duration(b[0]) * interval / 2560)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}
func (m *SessionManager) validatePath(p *sessionPath) error {
	overlayErr := nl.ValidatePath(p.cfg.BindDevice, p.cfg.LocalIP, p.cfg.RemoteIP, true)
	var underlayErr error
	for _, t := range m.cfg.Transports {
		if t.ID == p.cfg.Transport && t.Type == "vxlan" && t.Managed {
			underlayErr = nl.ValidatePath(t.Underlay.Interface, t.Underlay.LocalVTEPIP, t.Underlay.RemoteVTEPIP, false)
		}
	}
	m.mu.Lock()
	p.overlayReady = overlayErr == nil
	p.underlayReachable = underlayErr == nil
	m.mu.Unlock()
	return errors.Join(overlayErr, underlayErr)
}

func (m *SessionManager) desiredLocked() []nl.PathRoute {
	var desired []nl.PathRoute
	now := time.Now()
	for _, policy := range m.cfg.RoutePolicies {
		for _, candidate := range policy.Candidates {
			p := m.paths[pathKey(policy.Peer, candidate.Session)]
			if p == nil || !p.health.Eligible || !p.ads[policy.Prefix].After(now) {
				continue
			}
			_, prefix, _ := net.ParseCIDR(policy.Prefix)
			desired = append(desired, nl.PathRoute{Peer: policy.Peer, Session: candidate.Session, Transport: p.cfg.Transport, Route: nl.RouteConfig{Destination: prefix, Gateway: net.ParseIP(candidate.Via), Device: candidate.Dev, Metric: candidate.Metric, Table: int(policy.Table), Protocol: nl.RouteProtocolNNetMan}})
		}
	}
	return desired
}
func (m *SessionManager) reconcileLocked(reason string) {
	for _, p := range m.paths {
		for prefix, expiry := range p.ads {
			if !expiry.After(time.Now()) {
				delete(p.ads, prefix)
			}
		}
	}
	desired := m.desiredLocked()
	if e := m.fib.SyncCandidates(desired); e != nil {
		m.log.Error("candidate reconciliation failed", "error", e)
		if m.metrics != nil {
			m.metrics.errors.Inc()
		}
	}
	actual, e := m.fib.Current()
	if e != nil {
		m.log.Error("reading candidate FIB failed", "error", e)
		return
	}
	installed := map[string]nl.PathRoute{}
	for _, p := range actual {
		installed[p.Key()] = p
	}
	for key, p := range installed {
		if _, had := m.installed[key]; !had {
			m.logRouteChange(p, "INELIGIBLE", "INSTALLED", reason)
		}
	}
	for key, p := range m.installed {
		if _, has := installed[key]; !has {
			m.logRouteChange(p, "INSTALLED", "REMOVED", reason)
		}
	}
	m.installed = installed
	selected := map[string]nl.PathRoute{}
	for _, r := range actual {
		key := fmt.Sprintf("%d/%s", r.Route.Table, r.Route.Destination)
		prev, ok := selected[key]
		if !ok || r.Route.Metric < prev.Route.Metric {
			selected[key] = r
		}
	}
	visited := map[string]bool{}
	for _, policy := range m.cfg.RoutePolicies {
		key := fmt.Sprintf("%d/%s", policy.Table, policy.Prefix)
		if visited[key] {
			continue
		}
		visited[key] = true
		r, ok := selected[key]
		next := "none"
		if ok {
			next = r.Peer + "/" + r.Session
		}
		prev := m.selected[key]
		if prev == "" {
			prev = "none"
		}
		if prev != next {
			m.log.Info("selected route changed", "peer_id", policy.Peer, "session_id", next, "transport_id", r.Transport, "previous_state", prev, "new_state", next, "reason", reason, "route_prefix", policy.Prefix, "table", policy.Table, "via", r.Route.Gateway, "metric", r.Route.Metric)
			if m.metrics != nil {
				m.metrics.failovers.WithLabelValues(policy.Peer, prev, next, transitionReason(prev, next)).Inc()
			}
		}
		m.selected[key] = next
	}
	if m.metrics != nil {
		total := 0
		for _, p := range m.cfg.RoutePolicies {
			total += len(p.Candidates)
		}
		m.metrics.candidates.WithLabelValues("eligible").Set(float64(len(desired)))
		m.metrics.candidates.WithLabelValues("ineligible").Set(float64(total - len(desired)))
	}
}
func (m *SessionManager) logRouteChange(p nl.PathRoute, previous, next, reason string) {
	m.log.Info("candidate route changed", "peer_id", p.Peer, "session_id", p.Session, "transport_id", p.Transport, "previous_state", previous, "new_state", next, "reason", reason, "route_prefix", p.Route.Destination.String(), "table", p.Route.Table, "via", p.Route.Gateway.String(), "metric", p.Route.Metric)
}
func transitionReason(from, to string) string {
	if to == "none" {
		return "unavailable"
	}
	if from == "none" {
		return "available"
	}
	return "preference_change"
}
func (m *SessionManager) GetPeerStatuses() map[string]observability.PeerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]observability.PeerStatus{}
	for k, p := range m.paths {
		last := ""
		if !p.lastSeen.IsZero() {
			last = p.lastSeen.Format(time.RFC3339)
		}
		out[k] = observability.PeerStatus{ID: p.peer.ID, Session: p.cfg.ID, Transport: p.cfg.Transport, Source: p.cfg.LocalIP, Device: p.cfg.BindDevice, Endpoint: p.cfg.RemoteIP, Status: string(p.health.State), OverlayReady: p.overlayReady, UnderlayReachable: p.underlayReachable, RTTSeconds: p.rtt.Seconds(), LastSeen: last, Routes: len(p.ads), Error: p.lastError}
	}
	return out
}
func (m *SessionManager) GetRouteStats() observability.RouteStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	exported := 0
	for _, p := range m.cfg.Peers {
		exported += len(p.Routing.Export.Networks)
	}
	current, _ := m.fib.Current()
	return observability.RouteStats{Exported: exported, Installed: len(current)}
}

// Each listener accepts only configured node/session identities from the
// corresponding transit source. TLS additionally binds node_id to a DNS SAN.
type pathServer struct {
	pb.UnimplementedNNetManServer
	manager *SessionManager
	listen  string
}

func (s *pathServer) identify(ctx context.Context, node string, id *pb.SessionIdentity) (*sessionPath, error) {
	if id == nil || id.SessionId == "" || id.TransportId == "" || len(id.Epoch) != 32 {
		return nil, status.Error(codes.InvalidArgument, "session identity required")
	}
	remote, ok := peer.FromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing peer")
	}
	host, _, e := net.SplitHostPort(remote.Addr.String())
	if e != nil {
		return nil, e
	}
	if s.manager.cfg.Security.ControlPlane.TLS.Enabled {
		auth, ok := remote.AuthInfo.(credentials.TLSInfo)
		if !ok || len(auth.State.VerifiedChains) == 0 || len(auth.State.PeerCertificates) == 0 || auth.State.PeerCertificates[0].VerifyHostname(node) != nil {
			return nil, status.Error(codes.Unauthenticated, "node_id must match verified certificate SAN")
		}
	}
	for _, p := range s.manager.paths {
		if p.peer.ID == node && p.cfg.RemoteSession() == id.SessionId && p.cfg.RemoteIP == host && net.JoinHostPort(p.cfg.ListenIP, strconv.Itoa(p.cfg.Port)) == s.listen && (p.cfg.RemoteTransportID == "" || p.cfg.RemoteTransportID == id.TransportId) {
			return p, nil
		}
	}
	return nil, status.Error(codes.PermissionDenied, "unconfigured peer/session/transport endpoint")
}
func (s *pathServer) ExchangeState(ctx context.Context, r *pb.StateRequest) (*pb.StateResponse, error) {
	p, e := s.identify(ctx, r.NodeId, r.Session)
	if e != nil {
		return nil, e
	}
	if e = validateSnapshot(r.Routes); e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if _, e := s.manager.receiveSnapshotLocked(p, r.Session, r.Routes); e != nil {
		return nil, status.Error(codes.FailedPrecondition, e.Error())
	}
	s.manager.reconcileLocked("snapshot")
	return &pb.StateResponse{NodeId: s.manager.cfg.Node.ID, Routes: s.manager.exports(p), Accepted: true, Session: r.Session, Advertisement: proto.Clone(p.txIdentity).(*pb.SessionIdentity), TimestampMs: time.Now().UnixMilli()}, nil
}
func (s *pathServer) mutate(ctx context.Context, node string, id *pb.SessionIdentity, routes []*pb.Route, prefixes []string) (*pb.RouteAck, error) {
	p, e := s.identify(ctx, node, id)
	if e != nil {
		return nil, e
	}
	if e = validateSnapshot(routes); e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	if len(prefixes) > 4096 {
		return nil, status.Error(codes.InvalidArgument, "too many prefixes")
	}
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if p.rxEpoch != id.Epoch || p.rxTransport != id.TransportId || id.Generation <= p.rxGeneration {
		return nil, status.Error(codes.FailedPrecondition, "stale session generation")
	}
	for _, prefix := range prefixes {
		if _, _, e := net.ParseCIDR(prefix); e != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid prefix")
		}
	}
	p.rxGeneration = id.Generation
	s.manager.snapshotLocked(p, routes, false)
	for _, prefix := range prefixes {
		delete(p.ads, prefix)
	}
	s.manager.reconcileLocked("prefix_update")
	return &pb.RouteAck{Accepted: true, RoutesProcessed: uint32(len(routes) + len(prefixes))}, nil
}
func (s *pathServer) AnnounceRoutes(ctx context.Context, r *pb.RouteAnnouncement) (*pb.RouteAck, error) {
	return s.mutate(ctx, r.NodeId, r.Session, r.Routes, nil)
}
func (s *pathServer) WithdrawRoutes(ctx context.Context, r *pb.RouteWithdrawal) (*pb.RouteAck, error) {
	return s.mutate(ctx, r.NodeId, r.Session, nil, r.Prefixes)
}
func (s *pathServer) Keepalive(stream pb.NNetMan_KeepaliveServer) error {
	var sequence uint64
	for {
		r, e := stream.Recv()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		p, e := s.identify(stream.Context(), r.NodeId, r.Session)
		if e != nil {
			return e
		}
		if r.Sequence <= sequence {
			return status.Error(codes.InvalidArgument, "duplicate heartbeat sequence")
		}
		sequence = r.Sequence
		s.manager.mu.Lock()
		valid := p.rxEpoch == r.Session.Epoch && p.rxTransport == r.Session.TransportId && r.Sequence > p.rxSequence
		if valid {
			p.rxSequence = r.Sequence
		}
		s.manager.mu.Unlock()
		if !valid {
			return status.Error(codes.FailedPrecondition, "snapshot required for epoch")
		}
		if e := s.manager.validatePath(p); e != nil {
			return status.Error(codes.Unavailable, e.Error())
		}
		if e := stream.Send(&pb.KeepaliveResponse{NodeId: s.manager.cfg.Node.ID, Sequence: r.Sequence, Session: r.Session, TimestampMs: time.Now().UnixMilli()}); e != nil {
			return e
		}
	}
}

// SetExports changes the live subset of explicitly authorized networks. It sends
// withdrawals/announcements on every connected transport; reconnects always send
// the full current snapshot, including an empty export set.
func (m *SessionManager) SetExports(ctx context.Context, peerID string, networks []string) error {
	m.mu.Lock()
	var paths []*sessionPath
	for _, p := range m.paths {
		if p.peer.ID == peerID {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		m.mu.Unlock()
		return fmt.Errorf("unknown peer")
	}
	allowed := map[string]bool{}
	for _, n := range paths[0].peer.Routing.Export.Networks {
		allowed[n] = true
	}
	wanted := map[string]bool{}
	for _, n := range networks {
		if !allowed[n] || wanted[n] {
			m.mu.Unlock()
			return fmt.Errorf("export outside configured allowlist or duplicate: %s", n)
		}
		wanted[n] = true
	}
	type update struct {
		client       pb.NNetManClient
		withdrawal   *pb.RouteWithdrawal
		announcement *pb.RouteAnnouncement
	}
	var updates []update
	for _, p := range paths {
		old := map[string]bool{}
		for _, n := range p.exportNetworks {
			old[n] = true
		}
		var withdrawn []string
		var added []*pb.Route
		for n := range old {
			if !wanted[n] {
				withdrawn = append(withdrawn, n)
			}
		}
		for n := range wanted {
			if !old[n] {
				added = append(added, &pb.Route{Prefix: n, LeaseSeconds: 30})
			}
		}
		p.exportNetworks = append([]string(nil), networks...)
		u := update{client: p.client}
		if len(withdrawn) > 0 {
			p.txIdentity.Generation++
			u.withdrawal = &pb.RouteWithdrawal{NodeId: m.cfg.Node.ID, Prefixes: withdrawn, Session: proto.Clone(p.txIdentity).(*pb.SessionIdentity)}
		}
		if len(added) > 0 {
			p.txIdentity.Generation++
			u.announcement = &pb.RouteAnnouncement{NodeId: m.cfg.Node.ID, Routes: added, Session: proto.Clone(p.txIdentity).(*pb.SessionIdentity)}
		}
		updates = append(updates, u)
	}
	m.mu.Unlock()
	var errs []error
	for _, u := range updates {
		if u.client == nil {
			continue
		}
		if u.withdrawal != nil {
			ack, e := u.client.WithdrawRoutes(ctx, u.withdrawal)
			if e != nil {
				errs = append(errs, e)
			} else if !ack.Accepted {
				errs = append(errs, fmt.Errorf("withdrawal rejected: %s", ack.Error))
			}
		}
		if u.announcement != nil {
			ack, e := u.client.AnnounceRoutes(ctx, u.announcement)
			if e != nil {
				errs = append(errs, e)
			} else if !ack.Accepted {
				errs = append(errs, fmt.Errorf("announcement rejected: %s", ack.Error))
			}
		}
	}
	return errors.Join(errs...)
}
