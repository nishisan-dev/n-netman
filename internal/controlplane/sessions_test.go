package controlplane

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/nishisan-dev/n-netman/api/v1"
	"github.com/nishisan-dev/n-netman/internal/config"
	nl "github.com/nishisan-dev/n-netman/internal/netlink"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

type sessionFIB struct{ routes map[string]nl.RouteConfig }

func (f *sessionFIB) List(table int) ([]nl.RouteInfo, error) {
	var out []nl.RouteInfo
	for _, r := range f.routes {
		if r.Table == table {
			out = append(out, nl.RouteInfo{Destination: r.Destination, Gateway: r.Gateway, Device: r.Device, Metric: r.Metric, Table: r.Table, Protocol: r.Protocol})
		}
	}
	return out, nil
}
func (f *sessionFIB) Add(r nl.RouteConfig) error    { f.routes[fibKey(r)] = r; return nil }
func (f *sessionFIB) Delete(r nl.RouteConfig) error { delete(f.routes, fibKey(r)); return nil }
func fibKey(r nl.RouteConfig) string                { return r.Destination.String() + "/" + r.Device }
func testSessions(t *testing.T) (*SessionManager, *sessionFIB) {
	t.Helper()
	c := &config.Config{Version: 3, Node: config.NodeConfig{ID: "sp"}}
	p := config.PeerConfig{ID: "rj", Routing: config.OverlayRouting{Export: config.ExportConfig{Networks: []string{"192.168.101.0/24"}}, Import: config.ImportConfig{Allow: []string{"192.168.103.0/24"}}}}
	for _, id := range []string{"pw", "vx"} {
		ip := "192.168.185."
		metric := 100
		if id == "vx" {
			ip = "192.168.200."
			metric = 200
		}
		s := config.PeerSession{ID: id, Transport: id, LocalIP: ip + "1", ListenIP: ip + "1", RemoteIP: ip + "3", BindDevice: "br" + id, Port: 9898, Health: config.SessionHealth{IntervalMS: 1000, AckTimeoutMS: 700, DownAfterMisses: 3, UpAfterSuccesses: 10}}
		p.Sessions = append(p.Sessions, s)
		if len(c.RoutePolicies) == 0 {
			c.RoutePolicies = []config.RoutePolicy{{ID: "remote", Peer: "rj", Prefix: "192.168.103.0/24", Table: 254}}
		}
		c.RoutePolicies[0].Candidates = append(c.RoutePolicies[0].Candidates, config.RouteCandidate{Session: id, Via: s.RemoteIP, Dev: s.BindDevice, Metric: metric})
	}
	c.Peers = []config.PeerConfig{p}
	f := &sessionFIB{routes: map[string]nl.RouteConfig{}}
	fib, e := nl.NewCandidateManager(f, filepath.Join(t.TempDir(), "routes.json"))
	if e != nil {
		t.Fatal(e)
	}
	return NewSessionManager(c, fib, slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry()), f
}
func TestSessionHysteresisAndApplicationACKIdentity(t *testing.T) {
	h := SessionHealth{State: SessionInit}
	cfg := config.SessionHealth{DownAfterMisses: 3, UpAfterSuccesses: 10}
	for i := 0; i < 9; i++ {
		h.Observe(true, cfg)
		if h.Eligible {
			t.Fatal("premature UP")
		}
	}
	h.Observe(true, cfg)
	if !h.Eligible || h.State != SessionUp {
		t.Fatal(h)
	}
	for i := 0; i < 2; i++ {
		h.Observe(false, cfg)
		if !h.Eligible || h.State != SessionSuspect {
			t.Fatal(h)
		}
	}
	h.Observe(false, cfg)
	if h.Eligible || h.State != SessionDown {
		t.Fatal(h)
	}
	h.Observe(true, cfg)
	if h.State != SessionRecovering || h.Eligible {
		t.Fatal("first recovery ACK restored route")
	}
	id := &pb.SessionIdentity{SessionId: "pw", TransportId: "pw-rj", Epoch: newEpoch(), Generation: 1}
	ack := &pb.KeepaliveResponse{NodeId: "rj", Sequence: 4, Session: proto.Clone(id).(*pb.SessionIdentity)}
	if !validACK(ack, "rj", id, 4) {
		t.Fatal("valid ACK rejected")
	}
	for _, mutate := range []func(*pb.KeepaliveResponse){func(a *pb.KeepaliveResponse) { a.Sequence-- }, func(a *pb.KeepaliveResponse) { a.NodeId = "other" }, func(a *pb.KeepaliveResponse) { a.Session.SessionId = "vx" }, func(a *pb.KeepaliveResponse) { a.Session.Epoch = newEpoch() }, func(a *pb.KeepaliveResponse) { a.Session.TransportId = "vx" }} {
		bad := proto.Clone(ack).(*pb.KeepaliveResponse)
		mutate(bad)
		if validACK(bad, "rj", id, 4) {
			t.Fatal("invalid ACK accepted")
		}
	}
}
func TestSessionRoutesFailoverRecoveryWithdrawalAndLeases(t *testing.T) {
	m, f := testSessions(t)
	pw, vx := m.paths["rj/pw"], m.paths["rj/vx"]
	routes := []*pb.Route{{Prefix: "192.168.103.0/24", NextHop: "203.0.113.99", Metric: 1, LeaseSeconds: 30}, {Prefix: "10.99.0.0/16", LeaseSeconds: 30}}
	for _, p := range []*sessionPath{pw, vx} {
		m.snapshotLocked(p, routes, true)
		for i := 0; i < 10; i++ {
			m.observe(p, true, time.Millisecond, nil)
		}
	}
	if len(f.routes) != 2 {
		t.Fatalf("both UP: %v", f.routes)
	}
	for _, r := range f.routes {
		if r.Gateway.Equal(net.ParseIP("203.0.113.99")) || r.Metric == 1 {
			t.Fatal("remote supplied FIB attributes trusted")
		}
	}
	for i := 0; i < 3; i++ {
		m.observe(pw, false, 0, context.DeadlineExceeded)
	}
	if len(f.routes) != 1 || f.routes["192.168.103.0/24/brvx"].Metric != 200 {
		t.Fatal("backup not retained")
	}
	m.snapshotLocked(pw, routes, true)
	for i := 0; i < 9; i++ {
		m.observe(pw, true, time.Millisecond, nil)
		if len(f.routes) != 1 {
			t.Fatal("early restore")
		}
	}
	m.observe(pw, true, time.Millisecond, nil)
	if len(f.routes) != 2 {
		t.Fatal("primary not restored")
	}
	m.snapshotLocked(pw, nil, true)
	m.reconcileLocked("withdraw")
	if len(f.routes) != 1 {
		t.Fatal("PW snapshot withdrawal deleted VXLAN")
	}
	vx.ads["192.168.103.0/24"] = time.Now().Add(-time.Second)
	m.reconcileLocked("lease")
	if len(f.routes) != 0 {
		t.Fatal("expired lease installed")
	}
}
func TestSnapshotsGenerationsEpochsAndScopedWithdraw(t *testing.T) {
	m, f := testSessions(t)
	p := m.paths["rj/pw"]
	vx := m.paths["rj/vx"]
	p.health.Eligible = true
	vx.health.Eligible = true
	routes := []*pb.Route{{Prefix: "192.168.103.0/24", LeaseSeconds: 30}}
	id := &pb.SessionIdentity{SessionId: "pw", TransportId: "remote-pw", Epoch: newEpoch(), Generation: 1}
	if _, e := m.receiveSnapshotLocked(p, id, routes); e != nil {
		t.Fatal(e)
	}
	m.snapshotLocked(vx, routes, true)
	m.reconcileLocked("initial")
	ctx := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(p.cfg.RemoteIP), Port: 12345}})
	s := &pathServer{manager: m, listen: net.JoinHostPort(p.cfg.ListenIP, "9898")}
	withdrawn := proto.Clone(id).(*pb.SessionIdentity)
	withdrawn.Generation = 2
	if _, e := s.WithdrawRoutes(ctx, &pb.RouteWithdrawal{NodeId: "rj", Session: withdrawn, Prefixes: []string{"192.168.103.0/24"}}); e != nil {
		t.Fatal(e)
	}
	if len(f.routes) != 1 {
		t.Fatal("withdrawal crossed sessions")
	}
	if _, e := m.receiveSnapshotLocked(p, id, routes); e != nil {
		t.Fatal(e)
	}
	if len(p.ads) != 0 {
		t.Fatal("old snapshot resurrected withdrawal")
	}
	newer := proto.Clone(id).(*pb.SessionIdentity)
	newer.Epoch = newEpoch()
	if _, e := m.receiveSnapshotLocked(p, newer, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := m.receiveSnapshotLocked(p, id, routes); e == nil {
		t.Fatal("retired epoch accepted")
	}
	if _, e := s.AnnounceRoutes(ctx, &pb.RouteAnnouncement{NodeId: "rj", Session: withdrawn, Routes: routes}); e == nil {
		t.Fatal("previous epoch delta accepted")
	}
	wrong := peer.NewContext(context.Background(), &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(vx.cfg.RemoteIP), Port: 12345}})
	if _, e := s.identify(wrong, "rj", id); e == nil {
		t.Fatal("cross transport identity accepted")
	}
}
func TestSnapshotBoundsAndExportAllowlist(t *testing.T) {
	for _, r := range [][]*pb.Route{{nil}, {{Prefix: "bad", LeaseSeconds: 30}}, {{Prefix: "192.168.103.1/24", LeaseSeconds: 30}}, {{Prefix: "192.168.103.0/24", LeaseSeconds: 0}}, {{Prefix: "192.168.103.0/24", LeaseSeconds: 30}, {Prefix: "192.168.103.0/24", LeaseSeconds: 30}}} {
		if validateSnapshot(r) == nil {
			t.Fatal("bad snapshot accepted")
		}
	}
	m, _ := testSessions(t)
	if e := m.SetExports(context.Background(), "rj", []string{"0.0.0.0/0"}); e == nil {
		t.Fatal("unauthorized export accepted")
	}
	if e := m.SetExports(context.Background(), "rj", nil); e != nil {
		t.Fatal(e)
	}
	for _, p := range m.paths {
		if len(m.exports(p)) != 0 {
			t.Fatal("empty snapshot omitted withdrawal")
		}
	}
}

// A healthy TCP/gRPC connection with a stalled application must time out.
type stalledACKServer struct {
	pb.UnimplementedNNetManServer
	seen chan struct{}
}

func (s *stalledACKServer) Keepalive(stream pb.NNetMan_KeepaliveServer) error {
	if _, e := stream.Recv(); e != nil {
		return e
	}
	s.seen <- struct{}{}
	<-stream.Context().Done()
	return stream.Context().Err()
}
func TestApplicationACKTimeoutWhileConnectionRemainsOpen(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	stalled := &stalledACKServer{seen: make(chan struct{}, 3)}
	pb.RegisterNNetManServer(server, stalled)
	go func() { _ = server.Serve(lis) }()
	defer server.Stop()
	conn, e := grpc.NewClient("passthrough:///lab", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	id := &pb.SessionIdentity{SessionId: "pw", TransportId: "pw", Epoch: newEpoch()}
	h := SessionHealth{State: SessionUp, Eligible: true}
	for seq := uint64(1); seq <= 3; seq++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
		e := probeApplicationACK(ctx, pb.NewNNetManClient(conn), "sp", "rj", id, seq)
		cancel()
		if status.Code(e) != codes.DeadlineExceeded {
			t.Fatalf("expected application timeout, got %v", e)
		}
		h.Observe(false, config.SessionHealth{DownAfterMisses: 3, UpAfterSuccesses: 10})
	}
	if len(stalled.seen) != 3 || h.State != SessionDown || h.Eligible {
		t.Fatalf("missed HELLO/ACK timeout transition: %+v", h)
	}
}

func TestSessionCertificateMustAuthenticateClaimedNode(t *testing.T) {
	m, _ := testSessions(t)
	m.cfg.Security.ControlPlane.TLS.Enabled = true
	p := m.paths["rj/pw"]
	s := &pathServer{manager: m, listen: net.JoinHostPort(p.cfg.ListenIP, "9898")}
	id := &pb.SessionIdentity{SessionId: "pw", TransportId: "remote-pw", Epoch: newEpoch()}
	cert := &x509.Certificate{DNSNames: []string{"wrong-node"}}
	auth := credentials.TLSInfo{State: tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{cert}}, PeerCertificates: []*x509.Certificate{cert}}}
	remote := &peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(p.cfg.RemoteIP), Port: 12345}, AuthInfo: auth}
	if _, e := s.identify(peer.NewContext(context.Background(), remote), "rj", id); e == nil {
		t.Fatal("certificate allowed false node_id")
	}
	cert.DNSNames = []string{"rj"}
	if _, e := s.identify(peer.NewContext(context.Background(), remote), "rj", id); e != nil {
		t.Fatal(e)
	}
}

func TestStoppingPreventsLateRouteReinstallation(t *testing.T) {
	m, f := testSessions(t)
	p := m.paths["rj/pw"]
	p.health.Eligible = true
	m.snapshotLocked(p, []*pb.Route{{Prefix: "192.168.103.0/24", LeaseSeconds: 30}}, true)
	m.reconcileLocked("initial")
	if len(f.routes) != 1 {
		t.Fatal("initial candidate missing")
	}
	m.stopping = true
	m.reconcileLocked("shutdown")
	// Model a handler already in flight when grpc.Stop cancels its connection.
	m.snapshotLocked(p, []*pb.Route{{Prefix: "192.168.103.0/24", LeaseSeconds: 30}}, true)
	m.reconcileLocked("late snapshot")
	if len(f.routes) != 0 {
		t.Fatal("late handler reinstalled route after cleanup")
	}
}
