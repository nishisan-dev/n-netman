//go:build integration

// Integration tests for the agent's kernel-facing behaviour.
//
// They need CAP_NET_ADMIN in the current network namespace. Running as root
// works; so does an unprivileged user namespace:
//
//	unshare --net --user --map-root-user make test-integration
package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/net/ipv4"

	injectv1 "github.com/nishisan-dev/n-netman/api/inject/v1"
	"github.com/nishisan-dev/n-netman/internal/agentconfig"
	"github.com/nishisan-dev/n-netman/internal/inject"
	nlink "github.com/nishisan-dev/n-netman/internal/netlink"
)

const (
	testBridge  = "nnet-itest-br"
	testPSKText = "0123456789abcdef0123456789abcdef"
	testGroup   = "239.8.0.100"
	testPort    = 24790
)

func requireNetAdmin(t *testing.T) {
	t.Helper()
	// Interface names are capped at 15 characters by the kernel.
	link := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "nnet-probe"}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Skipf("no CAP_NET_ADMIN in this namespace: %v", err)
	}
	_ = netlink.LinkDel(link)
}

// setupBridge creates the bridge the tests use as their segment and removes it
// afterwards.
func setupBridge(t *testing.T) {
	t.Helper()
	requireNetAdmin(t)

	link := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: testBridge}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatalf("failed to create %s: %v", testBridge, err)
	}
	t.Cleanup(func() { _ = netlink.LinkDel(link) })

	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("failed to bring %s up: %v", testBridge, err)
	}
}

func writePSK(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inject.key")
	if err := os.WriteFile(path, []byte(testPSKText), 0o600); err != nil {
		t.Fatalf("failed to write the test key: %v", err)
	}
	return "file:" + path
}

func agentConfig(t *testing.T, acceptGateway bool, allow []string) *agentconfig.Config {
	t.Helper()

	cfg := agentconfig.Defaults()
	cfg.Version = 1
	cfg.Agent.ID = "itest-vm"
	cfg.Inject.Port = testPort
	cfg.Observability.Metrics.Enabled = false
	cfg.Observability.Healthcheck.Enabled = false
	cfg.Interfaces = []agentconfig.InterfaceConfig{{
		Name:       testBridge,
		Address:    "10.100.0.50/24",
		VNI:        100,
		ExpectTags: []string{"it"},
		PSKRef:     writePSK(t),
		Install: agentconfig.InstallConfig{
			Metric:               100,
			AcceptDefaultGateway: acceptGateway,
		},
		Import: agentconfig.ImportPolicy{Allow: allow},
	}}

	if err := agentconfig.NewLoader().Validate(cfg); err != nil {
		t.Fatalf("test config is invalid: %v", err)
	}
	return cfg
}

// sender publishes sealed advertisements onto the segment with multicast
// loopback enabled, so a listener on the same host receives them. The real
// publisher disables loopback, because in production the listener is in a VM.
type sender struct {
	conn net.PacketConn
	dst  *net.UDPAddr

	mu  sync.Mutex
	seq uint64
}

func newSender(t *testing.T) *sender {
	t.Helper()

	iface, err := net.InterfaceByName(testBridge)
	if err != nil {
		t.Fatalf("interface lookup: %v", err)
	}
	raw, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("failed to open the test sender: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })

	pc := ipv4.NewPacketConn(raw)
	if err := pc.SetMulticastInterface(iface); err != nil {
		t.Fatalf("failed to bind the test sender: %v", err)
	}
	if err := pc.SetMulticastTTL(1); err != nil {
		t.Fatalf("failed to set TTL: %v", err)
	}
	if err := pc.SetMulticastLoopback(true); err != nil {
		t.Fatalf("failed to enable loopback: %v", err)
	}

	return &sender{
		conn: raw,
		dst:  &net.UDPAddr{IP: net.ParseIP(testGroup), Port: testPort},
	}
}

func (s *sender) publish(t *testing.T, controllerID string, tags []string, gateway string, routes ...*injectv1.InjectedRoute) {
	t.Helper()

	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()

	adv := &injectv1.Advertisement{
		ControllerId:   controllerID,
		Vni:            100,
		Segment:        "vxlan-prod",
		Tags:           tags,
		TimestampMs:    time.Now().UnixMilli(),
		Sequence:       seq,
		LeaseSeconds:   3,
		Routes:         routes,
		DefaultGateway: gateway,
	}

	datagram, err := inject.Seal(adv, []byte(testPSKText), "k1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := s.conn.WriteTo(datagram, s.dst); err != nil {
		t.Fatalf("failed to publish: %v", err)
	}
}

func route(prefix, nextHop string, metric uint32) *injectv1.InjectedRoute {
	return &injectv1.InjectedRoute{Prefix: prefix, NextHop: nextHop, Metric: metric}
}

// kernelRoutes returns the destinations currently installed by the agent.
func kernelRoutes(t *testing.T) map[string]net.IP {
	t.Helper()

	installed, err := nlink.NewRouteManager().ListByProtocol(nlink.RouteTableMain, nlink.RouteProtocolNNetAgent)
	if err != nil {
		t.Fatalf("failed to list routes: %v", err)
	}

	out := make(map[string]net.IP, len(installed))
	for _, r := range installed {
		dst := "0.0.0.0/0"
		if r.Destination != nil {
			dst = r.Destination.String()
		}
		out[dst] = r.Gateway
	}
	return out
}

// waitFor polls until cond holds, so tests do not depend on a fixed sleep.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func startAgent(t *testing.T, cfg *agentconfig.Config) *Agent {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(cfg, logger, nil)
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = a.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
		_ = a.Withdraw()
	})

	return a
}

// The whole point of the feature: an advertisement becomes a kernel route.
func TestIntegration_AdvertisementBecomesAKernelRoute(t *testing.T) {
	setupBridge(t)

	cfg := agentConfig(t, false, []string{"172.16.0.0/16"})
	a := startAgent(t, cfg)
	s := newSender(t)

	// The agent applies the configured static address itself.
	waitFor(t, "the configured address to be applied", func() bool {
		link, err := netlink.LinkByName(testBridge)
		if err != nil {
			return false
		}
		addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			return false
		}
		for _, addr := range addrs {
			if addr.IPNet.String() == "10.100.0.50/24" {
				return true
			}
		}
		return false
	})

	publish := func() {
		s.publish(t, "host-a", []string{"it", "external"}, "",
			route("172.16.10.0/24", "10.100.0.1", 100))
	}
	publish()

	waitFor(t, "the advertised route to be installed", func() bool {
		publish()
		_, ok := kernelRoutes(t)["172.16.10.0/24"]
		return ok
	})

	if gw := kernelRoutes(t)["172.16.10.0/24"]; gw.String() != "10.100.0.1" {
		t.Fatalf("expected next-hop 10.100.0.1, got %s", gw)
	}
	if !a.Healthy() {
		t.Fatal("expected the agent to report healthy while a lease is live")
	}
}

// A prefix outside the allow list must never reach the kernel.
func TestIntegration_ImportPolicyIsEnforcedAgainstTheKernel(t *testing.T) {
	setupBridge(t)

	cfg := agentConfig(t, false, []string{"172.16.0.0/16"})
	startAgent(t, cfg)
	s := newSender(t)

	publish := func() {
		s.publish(t, "host-a", []string{"it"}, "",
			route("172.16.10.0/24", "10.100.0.1", 100),
			route("192.168.5.0/24", "10.100.0.1", 100))
	}

	waitFor(t, "the allowed route to be installed", func() bool {
		publish()
		_, ok := kernelRoutes(t)["172.16.10.0/24"]
		return ok
	})

	if _, denied := kernelRoutes(t)["192.168.5.0/24"]; denied {
		t.Fatal("a prefix outside the allow list reached the kernel")
	}
}

// An advertisement for a segment that does not carry the expected tags must be
// ignored, even though it authenticates correctly.
func TestIntegration_TagMismatchInstallsNothing(t *testing.T) {
	setupBridge(t)

	cfg := agentConfig(t, false, []string{"0.0.0.0/0"})
	startAgent(t, cfg)
	s := newSender(t)

	for i := 0; i < 5; i++ {
		s.publish(t, "host-a", []string{"storage"}, "",
			route("172.16.10.0/24", "10.100.0.1", 100))
		time.Sleep(100 * time.Millisecond)
	}

	if len(kernelRoutes(t)) != 0 {
		t.Fatalf("expected nothing to be installed, got %v", kernelRoutes(t))
	}
}

// When a controller stops advertising, its routes must disappear on their own.
func TestIntegration_RoutesExpireWhenAControllerGoesQuiet(t *testing.T) {
	setupBridge(t)

	cfg := agentConfig(t, false, []string{"172.16.0.0/16"})
	startAgent(t, cfg)
	s := newSender(t)

	waitFor(t, "the route to be installed", func() bool {
		s.publish(t, "host-a", []string{"it"}, "", route("172.16.10.0/24", "10.100.0.1", 100))
		_, ok := kernelRoutes(t)["172.16.10.0/24"]
		return ok
	})

	// Stop advertising; the 3s lease must run out and take the route with it.
	waitFor(t, "the lease to expire and withdraw the route", func() bool {
		_, ok := kernelRoutes(t)["172.16.10.0/24"]
		return !ok
	})
}

// Each controller holds its own lease: one going quiet must not withdraw the
// other's routes.
func TestIntegration_LeasesAreIndependentPerController(t *testing.T) {
	setupBridge(t)

	cfg := agentConfig(t, false, []string{"172.16.0.0/16"})
	startAgent(t, cfg)
	s := newSender(t)

	// Both controllers advertise on a ticker. A datagram published before the
	// listener has joined the group is simply lost, so a single send would make
	// this test flaky for reasons that have nothing to do with leases.
	publisher := func(stop <-chan struct{}, id, prefix, nextHop string) {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.publish(t, id, []string{"it"}, "", route(prefix, nextHop, 100))
			}
		}
	}

	stopB := make(chan struct{})
	defer close(stopB)
	go publisher(stopB, "host-b", "172.16.20.0/24", "10.100.0.2")

	stopA := make(chan struct{})
	go publisher(stopA, "host-a", "172.16.10.0/24", "10.100.0.1")

	waitFor(t, "both routes to be installed", func() bool {
		routes := kernelRoutes(t)
		_, a := routes["172.16.10.0/24"]
		_, b := routes["172.16.20.0/24"]
		return a && b
	})

	// host-a goes quiet; host-b keeps going.
	close(stopA)

	waitFor(t, "host-a's route to expire", func() bool {
		_, ok := kernelRoutes(t)["172.16.10.0/24"]
		return !ok
	})

	if _, ok := kernelRoutes(t)["172.16.20.0/24"]; !ok {
		t.Fatal("host-b's route was withdrawn along with host-a's; leases are not independent")
	}
}

// Withdrawal must remove exactly the agent's routes and leave everything else.
func TestIntegration_WithdrawLeavesForeignRoutesAlone(t *testing.T) {
	setupBridge(t)

	cfg := agentConfig(t, false, []string{"172.16.0.0/16"})
	a := startAgent(t, cfg)
	s := newSender(t)

	waitFor(t, "the agent route to be installed", func() bool {
		s.publish(t, "host-a", []string{"it"}, "", route("172.16.10.0/24", "10.100.0.1", 100))
		_, ok := kernelRoutes(t)["172.16.10.0/24"]
		return ok
	})

	// A route owned by someone else, in the same table.
	_, foreignDst, _ := net.ParseCIDR("172.16.99.0/24")
	foreign := nlink.RouteConfig{
		Destination: foreignDst,
		Gateway:     net.ParseIP("10.100.0.1"),
		Table:       nlink.RouteTableMain,
		Protocol:    nlink.RouteProtocolNNetMan,
	}
	if err := nlink.NewRouteManager().Replace(foreign); err != nil {
		t.Fatalf("failed to install the foreign route: %v", err)
	}

	if err := a.Withdraw(); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}

	if _, ok := kernelRoutes(t)["172.16.10.0/24"]; ok {
		t.Fatal("the agent's route survived withdrawal")
	}

	survivors, err := nlink.NewRouteManager().ListByProtocol(nlink.RouteTableMain, nlink.RouteProtocolNNetMan)
	if err != nil {
		t.Fatalf("failed to list the foreign routes: %v", err)
	}
	found := false
	for _, r := range survivors {
		if r.Destination != nil && r.Destination.String() == "172.16.99.0/24" {
			found = true
		}
	}
	if !found {
		t.Fatal("withdrawal removed a route belonging to another protocol")
	}
	_ = fmt.Sprint()
}

// A default gateway is installed only when the interface opted in.
func TestIntegration_DefaultGatewayIsOptIn(t *testing.T) {
	setupBridge(t)

	t.Run("declined", func(t *testing.T) {
		cfg := agentConfig(t, false, []string{"172.16.0.0/16"})
		startAgent(t, cfg)
		s := newSender(t)

		waitFor(t, "the specific route to be installed", func() bool {
			s.publish(t, "host-a", []string{"it"}, "10.100.0.1", route("172.16.10.0/24", "10.100.0.1", 100))
			_, ok := kernelRoutes(t)["172.16.10.0/24"]
			return ok
		})

		if _, ok := kernelRoutes(t)["0.0.0.0/0"]; ok {
			t.Fatal("a default route was installed without opting in")
		}
	})
}
