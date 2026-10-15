package config

import (
	"os"
	"strings"
	"testing"
)

const sampleV3 = `version: 3
node: {id: sp}
security: {control_plane: {insecure_lab: true}}
transports:
 - {id: pw, type: external, managed: false, interface: br185, local_ip: 192.168.185.1, peer_ip: 192.168.185.3}
peers:
 - id: rj
   sessions:
    - {id: pw, transport: pw, listen_ip: 192.168.185.1, local_ip: 192.168.185.1, remote_ip: 192.168.185.3, bind_device: br185}
   routing:
    export: {networks: [192.168.101.0/24]}
    import: {allow: [192.168.103.0/24], deny: [0.0.0.0/0]}
route_policies:
 - id: rj
   peer: rj
   prefix: 192.168.103.0/24
   table: main
   candidates:
    - {session: pw, via: 192.168.185.3, dev: br185, metric: 100}
`

func TestV3ConfigDefaultsAndPolicy(t *testing.T) {
	c, e := NewLoader().Load([]byte(sampleV3))
	if e != nil {
		t.Fatal(e)
	}
	if len(c.GetOverlays()) != 0 || c.RoutePolicies[0].Table != 254 || c.Peers[0].Sessions[0].Health.UpAfterSuccesses != 10 {
		t.Fatalf("bad defaults: %+v", c)
	}
	p := c.Peers[0].Routing.Import
	if !AllowsPrefix(p, "192.168.103.0/24") || AllowsPrefix(p, "192.168.103.0/25") || AllowsPrefix(p, "0.0.0.0/0") {
		t.Fatal("allowlist not exact/default rejected")
	}
	p.Deny = append(p.Deny, "192.168.103.0/24")
	if AllowsPrefix(p, "192.168.103.0/24") {
		t.Fatal("deny did not win")
	}
}
func TestV3RejectsUnsafeConfigurations(t *testing.T) {
	cases := map[string][2]string{
		"implicit insecure": {"insecure_lab: true", "insecure_lab: false"},
		"external mutation": {"managed: false", "managed: true"},
		"wrong egress":      {"bind_device: br185", "bind_device: bond1.185"},
		"wrong gateway":     {"via: 192.168.185.3", "via: 192.168.200.3"},
		"unknown transport": {"transport: pw,", "transport: missing,"},
		"wildcard listener": {"listen_ip: 192.168.185.1", "listen_ip: 0.0.0.0"},
		"table zero":        {"table: main", "table: 0"},
		"ipv6":              {"prefix: 192.168.103.0/24", "prefix: fd00::/64"},
		"no metric":         {"metric: 100", "metric: 0"},
		"bad timing":        {"bind_device: br185}", "bind_device: br185, health: {interval_ms: 100, ack_timeout_ms: 100}}"},
		"auto export":       {"export: {networks:", "export: {include_connected: true, networks:"},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			_, e := NewLoader().Load([]byte(strings.Replace(sampleV3, change[0], change[1], 1)))
			if e == nil {
				t.Fatal("accepted unsafe configuration")
			}
		})
	}
	// Two policies cannot claim the same kernel slot, even with different peers.
	c, _ := NewLoader().Load([]byte(sampleV3))
	extra := c.RoutePolicies[0]
	extra.ID = "duplicate-slot"
	c.RoutePolicies = append(c.RoutePolicies, extra)
	if e := NewLoader().Validate(c); e == nil {
		t.Fatal("accepted ECMP/ambiguous ownership")
	}
}

func TestV3ReferenceConfigurations(t *testing.T) {
	for _, city := range []string{"sp", "rj"} {
		t.Run(city, func(t *testing.T) {
			data, e := os.ReadFile("../../configs/v3-t1-" + city + ".yaml")
			if e != nil {
				t.Fatal(e)
			}
			// Examples are production mTLS; isolate parsing from real /etc credentials.
			text := strings.Replace(string(data), "    tls:\n      enabled: true", "    insecure_lab: true\n    tls:\n      enabled: false", 1)
			c, e := NewLoader().Load([]byte(text))
			if e != nil {
				t.Fatal(e)
			}
			if len(c.Transports) != 2 || len(c.Peers[0].Sessions) != 2 || c.RoutePolicies[0].Table != 254 {
				t.Fatal("incomplete SP/RJ example")
			}
		})
	}
}
