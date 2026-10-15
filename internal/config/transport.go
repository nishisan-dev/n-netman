package config

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Transport separates an observed L3 path from an explicitly managed overlay.
type Transport struct {
	ID        string            `yaml:"id"`
	Type      string            `yaml:"type"`
	Managed   bool              `yaml:"managed"`
	Interface string            `yaml:"interface"`
	LocalIP   string            `yaml:"local_ip"`
	PeerIP    string            `yaml:"peer_ip"`
	Name      string            `yaml:"name"`
	VNI       int               `yaml:"vni"`
	DstPort   int               `yaml:"dstport"`
	MTU       int               `yaml:"mtu"`
	Bridge    TransportBridge   `yaml:"bridge"`
	Underlay  TransportUnderlay `yaml:"underlay"`
	BUM       BUMConfig         `yaml:"bum"`
}
type TransportBridge struct {
	Name    string `yaml:"name"`
	Managed bool   `yaml:"managed"`
	IPv4    string `yaml:"ipv4"`
}
type TransportUnderlay struct {
	Interface    string `yaml:"interface"`
	LocalVTEPIP  string `yaml:"local_vtep_ip"`
	RemoteVTEPIP string `yaml:"remote_vtep_ip"`
}

func (t Transport) Device() string {
	if t.Type == "vxlan" {
		return t.Bridge.Name
	}
	return t.Interface
}
func (t Transport) Source() string {
	if t.Type == "vxlan" {
		ip, _, _ := net.ParseCIDR(t.Bridge.IPv4)
		if ip != nil {
			return ip.String()
		}
	}
	return t.LocalIP
}

type PeerSession struct {
	ID         string `yaml:"id"`
	Transport  string `yaml:"transport"`
	Protocol   string `yaml:"protocol"`
	ListenIP   string `yaml:"listen_ip"`
	LocalIP    string `yaml:"local_ip"`
	RemoteIP   string `yaml:"remote_ip"`
	Port       int    `yaml:"port"`
	BindDevice string `yaml:"bind_device"`
	// Optional remote IDs permit different naming on either side of a link.
	RemoteSessionID   string        `yaml:"remote_session_id"`
	RemoteTransportID string        `yaml:"remote_transport_id"`
	Health            SessionHealth `yaml:"health"`
}
type SessionHealth struct {
	IntervalMS       int `yaml:"interval_ms"`
	AckTimeoutMS     int `yaml:"ack_timeout_ms"`
	DownAfterMisses  int `yaml:"down_after_misses"`
	UpAfterSuccesses int `yaml:"up_after_successes"`
}

func (s PeerSession) RemoteSession() string {
	if s.RemoteSessionID != "" {
		return s.RemoteSessionID
	}
	return s.ID
}

type RoutePolicy struct {
	ID         string           `yaml:"id"`
	Peer       string           `yaml:"peer"`
	Prefix     string           `yaml:"prefix"`
	Table      RoutingTable     `yaml:"table"`
	Candidates []RouteCandidate `yaml:"candidates"`
}
type RouteCandidate struct {
	Session string `yaml:"session"`
	Via     string `yaml:"via"`
	Dev     string `yaml:"dev"`
	Metric  int    `yaml:"metric"`
}
type RoutingTable int

func (t *RoutingTable) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == "main" {
		*t = 254
		return nil
	}
	v, err := strconv.ParseUint(n.Value, 10, 31)
	if err != nil || v == 0 {
		return fmt.Errorf("invalid route table %q", n.Value)
	}
	*t = RoutingTable(v)
	return nil
}
func ipv4(s string) bool {
	a, e := netip.ParseAddr(s)
	return e == nil && a.Is4() && a.IsGlobalUnicast()
}
func prefix4(s string) bool {
	p, e := netip.ParsePrefix(s)
	return e == nil && p.Addr().Is4() && p == p.Masked()
}
func validID(s string) bool {
	return s != "" && !strings.ContainsAny(s, "/\\ \t\n\x00") && len(s) <= 128
}
func validDev(s string) bool { return validID(s) && len(s) <= 15 }

func validateV3(c *Config) error {
	fail := func(s string) error { return fmt.Errorf("v3: %s", s) }
	if !validID(c.Node.ID) {
		return fail("invalid node.id")
	}
	if c.StateFile == "" {
		c.StateFile = "/var/lib/n-netman/path-routes.json"
	}
	if !strings.HasPrefix(c.StateFile, "/") {
		return fail("state_file must be absolute")
	}
	tls := c.Security.ControlPlane.TLS
	if !tls.Enabled && !c.Security.ControlPlane.InsecureLab {
		return fail("mTLS is required (or explicit insecure_lab: true)")
	}
	if tls.Enabled {
		for _, p := range []string{tls.CertFile, tls.KeyFile, tls.CAFile} {
			if p == "" {
				return fail("mTLS requires cert_file, key_file and ca_file")
			}
			if _, e := os.Stat(p); e != nil {
				return e
			}
		}
	}
	transports := map[string]Transport{}
	devices := map[string]bool{}
	for i := range c.Transports {
		t := &c.Transports[i]
		if !validID(t.ID) {
			return fail("invalid transport id")
		}
		if _, ok := transports[t.ID]; ok {
			return fail("duplicate transport " + t.ID)
		}
		if t.Type != "external" && t.Type != "vxlan" {
			return fail("invalid transport type")
		}
		if t.Type == "external" && t.Managed {
			return fail("external transport must be observe-only")
		}
		if !validDev(t.Device()) || !ipv4(t.Source()) || !ipv4(t.PeerIP) || t.Source() == t.PeerIP {
			return fail("transport requires distinct IPv4 endpoints and L3 device: " + t.ID)
		}
		if t.Type == "vxlan" && t.Managed {
			if !t.Bridge.Managed || !validDev(t.Name) || t.VNI < 1 || t.VNI > 16777215 || !validDev(t.Underlay.Interface) || !ipv4(t.Underlay.LocalVTEPIP) || !ipv4(t.Underlay.RemoteVTEPIP) || t.Underlay.LocalVTEPIP == t.Underlay.RemoteVTEPIP {
				return fail("managed VXLAN requires explicit bridge ownership, VNI, name and underlay VTEPs")
			}
			if t.DstPort == 0 {
				t.DstPort = 4789
			}
			if t.MTU == 0 {
				t.MTU = 1450
			}
			if t.DstPort < 1 || t.DstPort > 65535 || t.MTU < 1280 || t.MTU > 9000 || t.BUM.GetMode() != "head-end-replication" {
				return fail("invalid VXLAN port/MTU/BUM mode")
			}
			_, sub, e := net.ParseCIDR(t.Bridge.IPv4)
			if e != nil || !sub.Contains(net.ParseIP(t.PeerIP)) {
				return fail("VXLAN peer must be on bridge.ipv4 subnet")
			}
			if sub.Contains(net.ParseIP(t.Underlay.RemoteVTEPIP)) || sub.Contains(net.ParseIP(t.Underlay.LocalVTEPIP)) || t.Underlay.Interface == t.Device() {
				return fail("underlay must be independent of overlay transit")
			}
			if t.Name == t.Device() || devices[t.Name] || devices[t.Device()] {
				return fail("duplicate managed device")
			}
			devices[t.Name], devices[t.Device()] = true, true
		}
		transports[t.ID] = *t
	}
	// A managed object cannot share an external transport's interface or underlay.
	for _, t := range c.Transports {
		if t.Type == "external" && devices[t.Device()] {
			return fail("managed device overlaps external transport")
		}
		if t.Type == "vxlan" && t.Managed {
			for _, other := range c.Transports {
				if t.Underlay.Interface == other.Device() {
					return fail("VXLAN underlay overlaps transport device")
				}
			}
		}
	}
	peers := map[string]map[string]PeerSession{}
	listeners := map[string]string{}
	listenerDevices := map[string]string{}
	for pi := range c.Peers {
		p := &c.Peers[pi]
		if !validID(p.ID) || p.ID == c.Node.ID || peers[p.ID] != nil {
			return fail("invalid/duplicate peer id")
		}
		peers[p.ID] = map[string]PeerSession{}
		if len(p.Sessions) == 0 {
			return fail("peer requires sessions")
		}
		for si := range p.Sessions {
			s := &p.Sessions[si]
			t, ok := transports[s.Transport]
			if !ok || !validID(s.ID) || (s.RemoteSessionID != "" && !validID(s.RemoteSessionID)) || (s.RemoteTransportID != "" && !validID(s.RemoteTransportID)) {
				return fail("invalid session or transport reference")
			}
			if _, ok := peers[p.ID][s.ID]; ok {
				return fail("duplicate session id")
			}
			if s.Protocol == "" {
				s.Protocol = "grpc"
			}
			if s.Port == 0 {
				s.Port = 9898
			}
			if s.Protocol != "grpc" || s.Port < 1 || s.Port > 65535 || !ipv4(s.LocalIP) || !ipv4(s.RemoteIP) || s.ListenIP != s.LocalIP || s.LocalIP != t.Source() || s.RemoteIP != t.PeerIP || s.BindDevice != t.Device() {
				return fail("session endpoints/bind_device must match transport L3 endpoints")
			}
			listenAddress := net.JoinHostPort(s.ListenIP, strconv.Itoa(s.Port))
			if dev, ok := listenerDevices[listenAddress]; ok && dev != s.BindDevice {
				return fail("listener address shared across devices")
			}
			listenerDevices[listenAddress] = s.BindDevice
			key := net.JoinHostPort(s.ListenIP, strconv.Itoa(s.Port)) + "/" + s.RemoteIP
			if listeners[key] != "" {
				return fail("ambiguous session endpoints")
			}
			listeners[key] = s.ID
			h := &s.Health
			if h.IntervalMS == 0 {
				h.IntervalMS = 1000
			}
			if h.AckTimeoutMS == 0 {
				h.AckTimeoutMS = 700
			}
			if h.DownAfterMisses == 0 {
				h.DownAfterMisses = 3
			}
			if h.UpAfterSuccesses == 0 {
				h.UpAfterSuccesses = 10
			}
			if h.IntervalMS < 100 || h.IntervalMS > 60000 || h.AckTimeoutMS < 50 || h.AckTimeoutMS >= h.IntervalMS || h.DownAfterMisses < 1 || h.DownAfterMisses > 100 || h.UpAfterSuccesses < 1 || h.UpAfterSuccesses > 1000 {
				return fail("invalid heartbeat limits (interval 100..60000ms, ACK 50ms..<interval)")
			}
			peers[p.ID][s.ID] = *s
		}
		if p.Routing.Export.ExportAll || p.Routing.Export.IncludeConnected || p.Routing.Export.IncludeNetplanStatic {
			return fail("v3 exports must be explicit")
		}
		for _, list := range [][]string{p.Routing.Export.Networks, p.Routing.Import.Allow, p.Routing.Import.Deny} {
			if len(list) > 4096 {
				return fail("prefix list exceeds 4096 entries")
			}
			seen := map[string]bool{}
			for _, v := range list {
				if !prefix4(v) || seen[v] {
					return fail("invalid/duplicate canonical IPv4 prefix " + v)
				}
				seen[v] = true
			}
		}
		for _, v := range p.Routing.Export.Networks {
			if v == "0.0.0.0/0" {
				return fail("default export is disabled")
			}
		}
	}
	policies := map[string]bool{}
	slots := map[string]bool{}
	for i := range c.RoutePolicies {
		r := &c.RoutePolicies[i]
		if r.Table == 0 {
			r.Table = 254
		}
		if !validID(r.ID) || policies[r.ID] || !prefix4(r.Prefix) || r.Prefix == "0.0.0.0/0" || r.Table < 1 || len(r.Candidates) == 0 || peers[r.Peer] == nil {
			return fail("invalid route policy")
		}
		policies[r.ID] = true
		seen := map[string]bool{}
		for _, cand := range r.Candidates {
			s, ok := peers[r.Peer][cand.Session]
			key := fmt.Sprintf("%d/%s/%d", r.Table, r.Prefix, cand.Metric)
			if !ok || seen[cand.Session] || cand.Via != s.RemoteIP || cand.Dev != s.BindDevice || cand.Metric < 1 || slots[key] {
				return fail("invalid/duplicate candidate, gateway/device mismatch or ECMP metric")
			}
			seen[cand.Session] = true
			slots[key] = true
		}
	}
	// Explicit v2 overlays/injection remain available, with the same validation.
	if len(c.Overlays) > 0 {
		clone := *c
		clone.Version = 2
		clone.Peers = nil
		if err := NewLoader().Validate(&clone); err != nil {
			return err
		}
		for _, o := range c.Overlays {
			if devices[o.Name] || devices[o.Bridge.Name] {
				return fail("legacy overlay overlaps transport")
			}
			for _, t := range c.Transports {
				if o.Name == t.Device() || o.Bridge.Name == t.Device() {
					return fail("legacy overlay overlaps observed transport")
				}
			}
		}
	}
	return validateInject(c)
}

// AllowsPrefix uses exact prefix allow/deny lists; /0 in deny means default only,
// not a blanket deny of all more-specific networks.
func AllowsPrefix(p ImportConfig, prefix string) bool {
	if !prefix4(prefix) || prefix == "0.0.0.0/0" {
		return false
	}
	for _, v := range p.Deny {
		if v == prefix {
			return false
		}
	}
	if p.AcceptAll {
		return true
	}
	for _, v := range p.Allow {
		if v == prefix {
			return true
		}
	}
	return false
}
