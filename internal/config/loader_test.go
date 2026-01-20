package config

import (
	"testing"
)

func TestLoader_Load_ValidConfig(t *testing.T) {
	yaml := `
version: 1
node:
  id: "test-node"
  hostname: "test-host"
overlay:
  vxlan:
    vni: 100
    name: "vxlan100"
    bridge: "br-test"
  peers:
    - id: "peer-1"
      endpoint:
        address: "10.0.0.2"
`
	loader := NewLoader()
	cfg, err := loader.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if cfg.Node.ID != "test-node" {
		t.Errorf("expected node.id = 'test-node', got '%s'", cfg.Node.ID)
	}

	if cfg.Overlay.VXLAN.VNI != 100 {
		t.Errorf("expected vxlan.vni = 100, got %d", cfg.Overlay.VXLAN.VNI)
	}

	if len(cfg.Overlay.Peers) != 1 {
		t.Errorf("expected 1 peer, got %d", len(cfg.Overlay.Peers))
	}
}

func TestLoader_Load_DefaultValues(t *testing.T) {
	yaml := `
version: 1
node:
  id: "test-node"
overlay:
  vxlan:
    vni: 100
    name: "vxlan100"
    bridge: "br-test"
`
	loader := NewLoader()
	cfg, err := loader.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	// Check defaults are applied
	if cfg.Overlay.VXLAN.DstPort != 4789 {
		t.Errorf("expected default dstport = 4789, got %d", cfg.Overlay.VXLAN.DstPort)
	}

	if cfg.Observability.Logging.Level != "info" {
		t.Errorf("expected default logging.level = 'info', got '%s'", cfg.Observability.Logging.Level)
	}

	if cfg.Topology.Transit != "deny" {
		t.Errorf("expected default transit = 'deny', got '%s'", cfg.Topology.Transit)
	}
}

func TestLoader_Load_MissingRequired(t *testing.T) {
	yaml := `
version: 1
node:
  id: "test-node"
# Missing overlay.vxlan
`
	loader := NewLoader()
	_, err := loader.Load([]byte(yaml))
	if err == nil {
		t.Fatal("expected validation error for missing overlay.vxlan")
	}
}

func TestLoader_Load_InvalidVNI(t *testing.T) {
	yaml := `
version: 1
node:
  id: "test-node"
overlay:
  vxlan:
    vni: 0
    name: "vxlan0"
    bridge: "br-test"
`
	loader := NewLoader()
	_, err := loader.Load([]byte(yaml))
	if err == nil {
		t.Fatal("expected validation error for vni = 0")
	}
}

func TestLoader_Load_MissingNodeID(t *testing.T) {
	yaml := `
version: 1
node:
  hostname: "test-host"
overlay:
  vxlan:
    vni: 100
    name: "vxlan100"
    bridge: "br-test"
`
	loader := NewLoader()
	_, err := loader.Load([]byte(yaml))
	if err == nil {
		t.Fatal("expected validation error for missing node.id")
	}
}

func TestLoader_Load_IPv6Peer(t *testing.T) {
	yaml := `
version: 1
node:
  id: "test-node"
overlay:
  vxlan:
    vni: 100
    name: "vxlan100"
    bridge: "br-test"
  peers:
    - id: "peer-ipv6"
      endpoint:
        address: "2001:db8::1"
`
	loader := NewLoader()
	cfg, err := loader.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("expected no error for IPv6 peer, got: %v", err)
	}

	if cfg.Overlay.Peers[0].Endpoint.Address != "2001:db8::1" {
		t.Errorf("expected IPv6 address, got '%s'", cfg.Overlay.Peers[0].Endpoint.Address)
	}
}

func TestLoader_Load_FullConfig(t *testing.T) {
	// Test with a config similar to the user's example
	yaml := `
version: 1
node:
  id: "curitiba-a-01"
  hostname: "host-a"
  tags:
    - "lab"
    - "kvm"
netplan:
  enabled: true
  config_paths:
    - "/etc/netplan"
  underlay:
    prefer_interfaces:
      - "ens3"
    prefer_address_families:
      - "ipv4"
      - "ipv6"
kvm:
  enabled: true
  provider: "libvirt"
  libvirt:
    uri: "qemu:///system"
    mode: "linux-bridge"
  bridges:
    - name: "br-nnet-100"
      stp: false
      mtu: 1450
      manage: true
overlay:
  vxlan:
    vni: 100
    name: "vxlan100"
    dstport: 4789
    learning: true
    mtu: 1450
    bridge: "br-nnet-100"
  peers:
    - id: "curitiba-b-01"
      endpoint:
        address: "10.10.0.12"
        via_interface: "ens3"
      auth:
        mode: "psk"
        psk_ref: "file:/etc/n-netman/psk/curitiba-b-01.key"
      health:
        keepalive_interval_ms: 1500
        dead_after_ms: 6000
routing:
  enabled: true
  export:
    networks:
      - "172.16.10.0/24"
      - "2001:db8:10::/64"
    include_connected: true
    metric: 100
  import:
    accept_all: false
    allow:
      - "172.16.0.0/16"
      - "2001:db8::/32"
    deny:
      - "0.0.0.0/0"
    install:
      table: 100
      flush_on_peer_down: true
      route_lease_seconds: 30
topology:
  mode: "direct-preferred"
  relay_fallback: true
  transit: "deny"
security:
  control_plane:
    transport: "grpc"
    listen:
      address: "0.0.0.0"
      port: 9898
observability:
  logging:
    level: "info"
    format: "json"
  metrics:
    enabled: true
    listen:
      address: "127.0.0.1"
      port: 9109
`
	loader := NewLoader()
	cfg, err := loader.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("expected no error for full config, got: %v", err)
	}

	// Verify key fields
	if cfg.Node.ID != "curitiba-a-01" {
		t.Errorf("unexpected node.id: %s", cfg.Node.ID)
	}

	if len(cfg.Node.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(cfg.Node.Tags))
	}

	if cfg.Overlay.VXLAN.VNI != 100 {
		t.Errorf("expected vni = 100, got %d", cfg.Overlay.VXLAN.VNI)
	}

	if len(cfg.Routing.Export.Networks) != 2 {
		t.Errorf("expected 2 export networks, got %d", len(cfg.Routing.Export.Networks))
	}
}
