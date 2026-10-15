//go:build integration

package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/nishisan-dev/n-netman/internal/config"
	nl "github.com/nishisan-dev/n-netman/internal/netlink"
	"github.com/nishisan-dev/n-netman/internal/observability"
	"github.com/nishisan-dev/n-netman/internal/pki"
	"github.com/nishisan-dev/n-netman/internal/reconciler"
	kernel "github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

type labStatus struct {
	Peers      map[string]observability.PeerStatus
	Device     string
	Candidates int
	Traffic    uint64
}

func atomicJSON(path string, value any) {
	b, _ := json.Marshal(value)
	_ = os.WriteFile(path+".tmp", b, 0600)
	_ = os.Rename(path+".tmp", path)
}

// Each node runs in a separate process so all gRPC/dialer/runtime threads inherit
// the correct network namespace. No shell utilities are needed by this lab.
func TestTransportLabNode(t *testing.T) {
	dir, node := os.Getenv("NNET_LAB_DIR"), os.Getenv("NNET_LAB_NODE")
	if dir == "" {
		t.Skip("lab subprocess")
	}
	if join := os.Getenv("NNET_LAB_JOIN"); join != "" {
		runtime.LockOSThread()
		ns, e := netns.GetFromPath(join)
		if e != nil {
			t.Fatal(e)
		}
		if e = netns.Set(ns); e != nil {
			t.Fatal(e)
		}
		env := []string{}
		for _, v := range os.Environ() {
			if !strings.HasPrefix(v, "NNET_LAB_JOIN=") {
				env = append(env, v)
			}
		}
		if e = syscall.Exec(os.Args[0], os.Args, env); e != nil {
			t.Fatal(e)
		}
	}
	configPath := filepath.Join(dir, node+".json")
	deadline := time.Now().Add(10 * time.Second)
	var data []byte
	for {
		var e error
		data, e = os.ReadFile(configPath)
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	c := &config.Config{}
	if e := json.Unmarshal(data, c); e != nil {
		t.Fatal(e)
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := reconciler.New(c, reconciler.WithLogger(logger), reconciler.WithInterval(200*time.Millisecond))
	if e := rec.RunOnce(ctx); e != nil {
		t.Fatal(e)
	}
	go func() { _ = rec.Run(ctx) }()
	fib, e := nl.NewCandidateManager(nl.NewRouteManager(), c.StateFile)
	if e != nil {
		t.Fatal(e)
	}
	manager := NewSessionManager(c, fib, logger, nil)
	localProd, remoteProd := "192.168.101.1", "192.168.103.1"
	if node == "rj" {
		localProd, remoteProd = remoteProd, localProd
	}
	listener, e := net.Listen("tcp4", net.JoinHostPort(localProd, "18080"))
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	go func() {
		for {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				var b [1]byte
				if _, e := io.ReadFull(conn, b[:]); e == nil {
					_, _ = conn.Write(b[:])
				}
			}()
		}
	}()
	var traffic atomic.Uint64
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(localProd)}, Timeout: 150 * time.Millisecond}
			conn, e := d.DialContext(ctx, "tcp4", net.JoinHostPort(remoteProd, "18080"))
			if e == nil {
				_ = conn.SetDeadline(time.Now().Add(150 * time.Millisecond))
				_, e = conn.Write([]byte{42})
				if e == nil {
					var b [1]byte
					_, e = io.ReadFull(conn, b[:])
					if e == nil && b[0] == 42 {
						traffic.Add(1)
					}
				}
				conn.Close()
			}
		}
	}()
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		lastCommand := ""
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			state := labStatus{Peers: manager.GetPeerStatuses(), Traffic: traffic.Load()}
			if routes, e := kernel.RouteGetWithOptions(net.ParseIP(remoteProd), &kernel.RouteGetOptions{SrcAddr: net.ParseIP(localProd)}); e == nil && len(routes) == 1 {
				if link, e := kernel.LinkByIndex(routes[0].LinkIndex); e == nil {
					state.Device = link.Attrs().Name
				}
			}
			for _, r := range c.RoutePolicies {
				routes, _ := nl.NewRouteManager().ListByProtocol(int(r.Table), 99)
				state.Candidates += len(routes)
			}
			atomicJSON(filepath.Join(dir, node+".status"), state)
			if _, e := os.Stat(filepath.Join(dir, node+".stop")); e == nil {
				cancel()
				return
			}
			command, _ := os.ReadFile(filepath.Join(dir, node+".command"))
			if string(command) != lastCommand {
				lastCommand = string(command)
				var networks []string
				if strings.HasPrefix(lastCommand, "restore") {
					networks = c.Peers[0].Routing.Export.Networks
				}
				if lastCommand != "" {
					callCtx, cc := context.WithTimeout(ctx, time.Second)
					_ = manager.SetExports(callCtx, c.Peers[0].ID, networks)
					cc()
				}
			}
		}
	}()
	if e := manager.Run(ctx); e != nil {
		t.Fatal(e)
	}
}

type labNode struct {
	name   string
	cmd    *exec.Cmd
	ns     netns.NsHandle
	handle *kernel.Handle
	done   chan error
	log    *os.File
}

func spawnLabNode(t *testing.T, dir, name, join string) *labNode {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestTransportLabNode$", "-test.v")
	cmd.Env = append(os.Environ(), "NNET_LAB_DIR="+dir, "NNET_LAB_NODE="+name)
	if join == "" {
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	} else {
		cmd.Env = append(cmd.Env, "NNET_LAB_JOIN="+join)
	}
	log, e := os.OpenFile(filepath.Join(dir, name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stdout = log
	cmd.Stderr = log
	if e = cmd.Start(); e != nil {
		log.Close()
		t.Fatal(e)
	}
	n := &labNode{name: name, cmd: cmd, log: log, done: make(chan error, 1)}
	go func() { n.done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); log.Close() })
	if join == "" {
		n.ns, e = netns.GetFromPid(cmd.Process.Pid)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { n.ns.Close() })
		n.handle, e = kernel.NewHandleAt(n.ns)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(n.handle.Close)
	}
	return n
}
func labAddress(t *testing.T, h *kernel.Handle, link kernel.Link, cidr string) {
	t.Helper()
	a, e := kernel.ParseAddr(cidr)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.AddrAdd(link, a); e != nil {
		t.Fatal(e)
	}
	if e = h.LinkSetUp(link); e != nil {
		t.Fatal(e)
	}
}
func setupLabLinks(t *testing.T, sp, rj *labNode) {
	t.Helper()
	for _, n := range []*labNode{sp, rj} {
		lo, e := n.handle.LinkByName("lo")
		if e != nil {
			t.Fatal(e)
		}
		_ = n.handle.LinkSetUp(lo)
		bridge := &kernel.Bridge{LinkAttrs: kernel.LinkAttrs{Name: "br185", MTU: 1500}}
		if e = n.handle.LinkAdd(bridge); e != nil {
			t.Fatal(e)
		}
		prod := &kernel.Dummy{LinkAttrs: kernel.LinkAttrs{Name: "prod"}}
		if e = n.handle.LinkAdd(prod); e != nil {
			t.Fatal(e)
		}
		transit, production := "192.168.185.1/24", "192.168.101.1/24"
		if n.name == "rj" {
			transit, production = "192.168.185.3/24", "192.168.103.1/24"
		}
		labAddress(t, n.handle, bridge, transit)
		labAddress(t, n.handle, prod, production)
	}
	for _, name := range []string{"pw-link", "underlay0"} {
		v := &kernel.Veth{LinkAttrs: kernel.LinkAttrs{Name: name, MTU: 1500}, PeerName: "peer-temp"}
		if e := sp.handle.LinkAdd(v); e != nil {
			t.Fatal(e)
		}
		peer, e := sp.handle.LinkByName("peer-temp")
		if e != nil {
			t.Fatal(e)
		}
		if e = sp.handle.LinkSetNsFd(peer, int(rj.ns)); e != nil {
			t.Fatal(e)
		}
		peer, e = rj.handle.LinkByName("peer-temp")
		if e != nil {
			t.Fatal(e)
		}
		if e = rj.handle.LinkSetName(peer, name); e != nil {
			t.Fatal(e)
		}
		for _, n := range []*labNode{sp, rj} {
			link, e := n.handle.LinkByName(name)
			if e != nil {
				t.Fatal(e)
			}
			if name == "pw-link" {
				bridge, _ := n.handle.LinkByName("br185")
				if e = n.handle.LinkSetMaster(link, bridge); e != nil {
					t.Fatal(e)
				}
				_ = n.handle.LinkSetUp(link)
			} else {
				ip := "198.51.100.11/24"
				if n.name == "rj" {
					ip = "198.51.100.13/24"
				}
				labAddress(t, n.handle, link, ip)
			}
		}
	}
}
func labConfig(dir, node string) *config.Config {
	local, remote, source, neighbor, export, imported := "1", "3", "11", "13", "192.168.101.0/24", "192.168.103.0/24"
	peerID := "rj"
	if node == "rj" {
		local, remote, source, neighbor, export, imported = "3", "1", "13", "11", imported, export
		peerID = "sp"
	}
	c := &config.Config{Version: 3, Node: config.NodeConfig{ID: node}, StateFile: filepath.Join(dir, node+".ownership")}
	c.Security.ControlPlane.InsecureLab = true
	c.Transports = []config.Transport{{ID: "pw-" + peerID, Type: "external", Interface: "br185", LocalIP: "192.168.185." + local, PeerIP: "192.168.185." + remote}, {ID: "vx-" + peerID, Type: "vxlan", Managed: true, Name: "vxlan200", VNI: 200, DstPort: 4789, MTU: 1450, Bridge: config.TransportBridge{Name: "br200", Managed: true, IPv4: "192.168.200." + local + "/24"}, Underlay: config.TransportUnderlay{Interface: "underlay0", LocalVTEPIP: "198.51.100." + source, RemoteVTEPIP: "198.51.100." + neighbor}, PeerIP: "192.168.200." + remote}}
	p := config.PeerConfig{ID: peerID, Routing: config.OverlayRouting{Export: config.ExportConfig{Networks: []string{export}}, Import: config.ImportConfig{Allow: []string{imported}}}}
	policy := config.RoutePolicy{ID: "remote", Peer: peerID, Prefix: imported, Table: 254}
	for i, tr := range c.Transports {
		id := "pw"
		metric := 100
		if i == 1 {
			id = "vx"
			metric = 200
		}
		p.Sessions = append(p.Sessions, config.PeerSession{ID: id, Transport: tr.ID, Protocol: "grpc", ListenIP: tr.Source(), LocalIP: tr.Source(), RemoteIP: tr.PeerIP, Port: 19898, BindDevice: tr.Device(), Health: config.SessionHealth{IntervalMS: 200, AckTimeoutMS: 100, DownAfterMisses: 3, UpAfterSuccesses: 3}})
		policy.Candidates = append(policy.Candidates, config.RouteCandidate{Session: id, Via: tr.PeerIP, Dev: tr.Device(), Metric: metric})
	}
	c.Peers = []config.PeerConfig{p}
	c.RoutePolicies = []config.RoutePolicy{policy}
	return c
}
func readLabStatus(dir, node string) labStatus {
	var state labStatus
	b, _ := os.ReadFile(filepath.Join(dir, node+".status"))
	_ = json.Unmarshal(b, &state)
	return state
}
func awaitLab(t *testing.T, dir, label string, predicate func() bool) {
	t.Helper()
	until := time.Now().Add(12 * time.Second)
	for time.Now().Before(until) {
		if predicate() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, n := range []string{"sp", "rj"} {
		b, _ := os.ReadFile(filepath.Join(dir, n+".log"))
		t.Log(string(b))
		t.Logf("%s status: %+v", n, readLabStatus(dir, n))
	}
	t.Fatalf("timeout: %s", label)
}
func blackholePW(t *testing.T, n *labNode, remote string, enabled bool) {
	t.Helper()
	link, e := n.handle.LinkByName("br185")
	if e != nil {
		t.Fatal(e)
	}
	neigh := &kernel.Neigh{LinkIndex: link.Attrs().Index, IP: net.ParseIP(remote), State: kernel.NUD_PERMANENT, HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 99}}
	if enabled {
		e = n.handle.NeighSet(neigh)
	} else {
		e = n.handle.NeighDel(neigh)
	}
	if e != nil {
		t.Fatal(e)
	}
}
func TestTransportFailoverNetworkNamespaces(t *testing.T) {
	if os.Getenv("NNET_LAB_DIR") != "" {
		t.Skip("parent only")
	}
	probe := exec.Command("/proc/self/exe", "-test.run=^TestTransportLabNode$")
	probe.SysProcAttr = &syscall.SysProcAttr{Cloneflags: syscall.CLONE_NEWNET}
	if e := probe.Run(); e != nil {
		t.Skipf("requires CAP_NET_ADMIN and namespace creation (use unshare --user --map-root-user --net): %v", e)
	}
	dir := t.TempDir()
	sp, rj := spawnLabNode(t, dir, "sp", ""), spawnLabNode(t, dir, "rj", "")
	setupLabLinks(t, sp, rj)
	tlsDir := filepath.Join(dir, "tls")
	if e := pki.GenerateCA(tlsDir, 1); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"sp", "rj"} {
		if e := pki.GenerateHostCert(tlsDir, filepath.Join(tlsDir, "ca.crt"), filepath.Join(tlsDir, "ca.key"), name, nil, 1); e != nil {
			t.Fatal(e)
		}
	}
	pwBefore, _ := sp.handle.LinkByName("br185")
	portBefore, _ := sp.handle.LinkByName("pw-link")
	for _, n := range []*labNode{sp, rj} {
		c := labConfig(dir, n.name)
		c.Security.ControlPlane.InsecureLab = false
		c.Security.ControlPlane.TLS = config.TLSConfig{Enabled: true, CertFile: filepath.Join(tlsDir, n.name+".crt"), KeyFile: filepath.Join(tlsDir, n.name+".key"), CAFile: filepath.Join(tlsDir, "ca.crt")}
		if e := config.NewLoader().Validate(c); e != nil {
			t.Fatal(e)
		}
		atomicJSON(filepath.Join(dir, n.name+".json"), c)
	}
	awaitLab(t, dir, "both metrics and real PW traffic", func() bool {
		a, b := readLabStatus(dir, "sp"), readLabStatus(dir, "rj")
		return a.Candidates == 2 && b.Candidates == 2 && a.Device == "br185" && b.Device == "br185" && a.Traffic > 0 && b.Traffic > 0
	})
	// Install unrelated proto99 route to prove protocol alone is not ownership.
	_, foreign, _ := net.ParseCIDR("203.0.113.0/24")
	if e := sp.handle.RouteAdd(&kernel.Route{Dst: foreign, LinkIndex: pwBefore.Attrs().Index, Table: 254, Protocol: 99}); e != nil {
		t.Fatal(e)
	}
	failoverStarted := time.Now()
	before := readLabStatus(dir, "sp").Traffic
	blackholePW(t, sp, "192.168.185.3", true)
	awaitLab(t, dir, "application timeout with carrier UP selects VXLAN", func() bool {
		a := readLabStatus(dir, "sp")
		return a.Candidates == 2 && a.Device == "br200" && a.Peers["rj/pw"].Status == "DOWN" && a.Traffic > before
	}) // 1 managed + 1 foreign
	if elapsed := time.Since(failoverStarted); elapsed < 300*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("failover outside three-cycle scheduling allowance: %s", elapsed)
	}
	// Even with an alternative route to the PW control IP, it must stay DOWN.
	vxLink, _ := sp.handle.LinkByName("br200")
	_, controlHost, _ := net.ParseCIDR("192.168.185.3/32")
	alternative := &kernel.Route{Dst: controlHost, Gw: net.ParseIP("192.168.200.3"), LinkIndex: vxLink.Attrs().Index, Table: 254, Protocol: 4}
	if e := sp.handle.RouteAdd(alternative); e != nil {
		t.Fatal(e)
	}
	time.Sleep(time.Second)
	if state := readLabStatus(dir, "sp"); state.Peers["rj/pw"].Status != "DOWN" || state.Device != "br200" {
		t.Fatalf("PW control escaped through backup: %+v", state)
	}
	if e := sp.handle.RouteDel(alternative); e != nil {
		t.Fatal(e)
	}
	link, _ := sp.handle.LinkByName("br185")
	if link.Attrs().Flags&net.FlagUp == 0 {
		t.Fatal("fault changed link state")
	}
	blackholePW(t, sp, "192.168.185.3", false)
	awaitLab(t, dir, "PW recovering retains backup", func() bool {
		a := readLabStatus(dir, "sp")
		return a.Peers["rj/pw"].Status == "RECOVERING" && a.Device == "br200"
	})
	awaitLab(t, dir, "stable primary recovery", func() bool {
		a, b := readLabStatus(dir, "sp"), readLabStatus(dir, "rj")
		return a.Device == "br185" && a.Candidates == 3 && b.Candidates == 2
	})
	vxBefore, _ := sp.handle.LinkByName("vxlan200")
	// Crash a node with stale candidates; retain its namespace using ns handle.
	_ = sp.cmd.Process.Kill()
	<-sp.done
	blackholePW(t, rj, "192.168.185.1", true)
	_ = os.Remove(filepath.Join(dir, "sp.status"))
	join := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), int(sp.ns))
	spRestart := spawnLabNode(t, dir, "sp", join)
	awaitLab(t, dir, "restart cleans own primary before health and retains foreign route", func() bool {
		a := readLabStatus(dir, "sp")
		return a.Device == "br200" && a.Candidates == 2 && a.Peers["rj/pw"].Status == "DOWN"
	})
	blackholePW(t, rj, "192.168.185.1", false)
	awaitLab(t, dir, "primary after restart", func() bool {
		return readLabStatus(dir, "sp").Candidates == 3 && readLabStatus(dir, "sp").Device == "br185"
	})
	// Prefix withdrawal must remove both candidates despite healthy sessions.
	_ = os.WriteFile(filepath.Join(dir, "rj.command"), []byte("withdraw-1"), 0600)
	awaitLab(t, dir, "withdraw with sessions UP", func() bool {
		a := readLabStatus(dir, "sp")
		return a.Candidates == 1 && a.Peers["rj/pw"].Status == "UP" && a.Peers["rj/vx"].Status == "UP"
	})
	_ = os.WriteFile(filepath.Join(dir, "rj.command"), []byte("restore-2"), 0600)
	awaitLab(t, dir, "explicit reannouncement", func() bool { return readLabStatus(dir, "sp").Candidates == 3 })
	// Underlay failure affects only VXLAN; primary and external bridge survive.
	underlay, _ := sp.handle.LinkByName("underlay0")
	_ = sp.handle.LinkSetDown(underlay)
	awaitLab(t, dir, "VXLAN-only failure", func() bool {
		a := readLabStatus(dir, "sp")
		return a.Candidates == 2 && a.Device == "br185" && a.Peers["rj/vx"].Status == "DOWN"
	})
	blackholePW(t, sp, "192.168.185.3", true)
	awaitLab(t, dir, "both down leave foreign route", func() bool { return readLabStatus(dir, "sp").Candidates == 1 })
	vxAfter, _ := sp.handle.LinkByName("vxlan200")
	pwAfter, _ := sp.handle.LinkByName("br185")
	portAfter, _ := sp.handle.LinkByName("pw-link")
	if vxBefore.Attrs().Index != vxAfter.Attrs().Index || pwBefore.Attrs().Index != pwAfter.Attrs().Index || pwBefore.Attrs().MTU != pwAfter.Attrs().MTU || portBefore.Attrs().MasterIndex != portAfter.Attrs().MasterIndex {
		t.Fatal("failover/restart changed VXLAN or external bridge")
	}
	for _, n := range []*labNode{spRestart, rj} {
		_ = os.WriteFile(filepath.Join(dir, n.name+".stop"), nil, 0600)
		select {
		case e := <-n.done:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("node shutdown deadlock")
		}
	}
}
