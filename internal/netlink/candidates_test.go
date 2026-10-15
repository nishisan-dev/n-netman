package netlink

import (
	"fmt"
	"net"
	"path/filepath"
	"testing"
)

type memoryFIB struct {
	routes        map[string]RouteConfig
	adds, deletes int
}

func (f *memoryFIB) List(table int) ([]RouteInfo, error) {
	var out []RouteInfo
	for _, r := range f.routes {
		if r.Table == table {
			out = append(out, RouteInfo{Destination: r.Destination, Gateway: r.Gateway, Device: r.Device, Table: r.Table, Metric: r.Metric, Protocol: r.Protocol})
		}
	}
	return out, nil
}
func (f *memoryFIB) Add(r RouteConfig) error {
	for _, existing := range f.routes {
		if routeSlot(existing) == routeSlot(r) {
			return fmt.Errorf("EEXIST")
		}
	}
	f.routes[routeKey(r)] = r
	f.adds++
	return nil
}
func (f *memoryFIB) Delete(r RouteConfig) error {
	delete(f.routes, routeKey(r))
	f.deletes++
	return nil
}
func pathRoute(session, gw, dev string, metric int) PathRoute {
	_, dst, _ := net.ParseCIDR("192.168.103.0/24")
	return PathRoute{Peer: "rj", Session: session, Transport: session, Route: RouteConfig{Destination: dst, Gateway: net.ParseIP(gw), Device: dev, Table: 254, Metric: metric, Protocol: 99}}
}
func TestCandidatesCoexistFailoverRestartAndOwnership(t *testing.T) {
	f := &memoryFIB{routes: map[string]RouteConfig{}}
	file := filepath.Join(t.TempDir(), "routes.json")
	m, e := NewCandidateManager(f, file)
	if e != nil {
		t.Fatal(e)
	}
	pw, vx := pathRoute("pw", "192.168.185.3", "br185", 100), pathRoute("vx", "192.168.200.3", "br200", 200)
	foreign := pw.Route
	foreign.Protocol = 4
	foreign.Metric = 50
	f.routes[routeKey(foreign)] = foreign
	for _, desired := range [][]PathRoute{{pw, vx}, {pw, vx}, {vx}, {pw, vx}, {pw}, nil} {
		if e := m.SyncCandidates(desired); e != nil {
			t.Fatal(e)
		}
		if len(f.routes) != len(desired)+1 {
			t.Fatalf("routes = %v", f.routes)
		}
		if _, ok := f.routes[routeKey(foreign)]; !ok {
			t.Fatal("foreign route removed")
		}
	}
	if e := m.SyncCandidates([]PathRoute{pw, vx}); e != nil {
		t.Fatal(e)
	}
	restarted, e := NewCandidateManager(f, file)
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.SyncCandidates(nil); e != nil {
		t.Fatal(e)
	}
	if len(f.routes) != 1 {
		t.Fatal("restart retained healthy candidates")
	}
}
func TestCandidatesRefuseUnownedProto99AndPreserveDownDeletion(t *testing.T) {
	f := &memoryFIB{routes: map[string]RouteConfig{}}
	m, _ := NewCandidateManager(f, filepath.Join(t.TempDir(), "routes.json"))
	pw, vx := pathRoute("pw", "192.168.185.3", "br185", 100), pathRoute("vx", "192.168.200.3", "br200", 200)
	if e := m.SyncCandidates([]PathRoute{pw}); e != nil {
		t.Fatal(e)
	}
	f.routes[routeKey(vx.Route)] = vx.Route // Same proto and exact tuple, but no ownership record.
	if e := m.SyncCandidates([]PathRoute{vx}); e == nil {
		t.Fatal("unowned candidate adopted")
	}
	if _, ok := f.routes[routeKey(pw.Route)]; ok {
		t.Fatal("conflict stranded DOWN primary")
	}
	if _, ok := f.routes[routeKey(vx.Route)]; !ok {
		t.Fatal("unowned proto99 removed")
	}
}
func TestCandidatePlanIsReadOnlyAndLockExclusive(t *testing.T) {
	f := &memoryFIB{routes: map[string]RouteConfig{}}
	m, _ := NewCandidateManager(f, filepath.Join(t.TempDir(), "routes.json"))
	plan, e := m.Plan([]PathRoute{pathRoute("pw", "192.168.185.3", "br185", 100)})
	if e != nil || len(plan) != 1 || f.adds != 0 {
		t.Fatalf("plan=%v err=%v", plan, e)
	}
	unlock, e := m.Lock()
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	other, _ := NewCandidateManager(f, m.file)
	if release, e := other.Lock(); e == nil {
		release()
		t.Fatal("second daemon got ownership lock")
	}
}

func TestCandidatesTwoPeersSamePrefix(t *testing.T) {
	f := &memoryFIB{routes: map[string]RouteConfig{}}
	m, _ := NewCandidateManager(f, filepath.Join(t.TempDir(), "routes.json"))
	a := pathRoute("pw", "192.168.185.3", "br185", 100)
	b := pathRoute("pw", "192.168.185.4", "br185", 150)
	b.Peer = "other"
	if e := m.SyncCandidates([]PathRoute{a, b}); e != nil {
		t.Fatal(e)
	}
	if len(f.routes) != 2 {
		t.Fatal("one peer overwrote another")
	}
	if e := m.SyncCandidates([]PathRoute{b}); e != nil {
		t.Fatal(e)
	}
	if _, ok := f.routes[routeKey(b.Route)]; !ok {
		t.Fatal("withdrawal crossed peer boundary")
	}
}
