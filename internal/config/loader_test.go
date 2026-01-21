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

func TestLoader_Load_MultiOverlayV2(t *testing.T) {
	yaml := `
version: 2
node:
  id: "test-node"
overlays:
  - vni: 100
    name: "vxlan-prod"
    bridge: "br-prod"
    underlay_interface: "ens3"
    routing:
      export:
        networks:
          - "172.16.0.0/16"
      import:
        install:
          table: 100
  - vni: 200
    name: "vxlan-mgmt"
    bridge: "br-mgmt"
    underlay_interface: "ens4"
    routing:
      export:
        networks:
          - "10.200.0.0/16"
      import:
        install:
          table: 200
`
	loader := NewLoader()
	cfg, err := loader.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("expected no error for v2 multi-overlay, got: %v", err)
	}

	if cfg.Version != 2 {
		t.Errorf("expected version = 2, got %d", cfg.Version)
	}

	if len(cfg.Overlays) != 2 {
		t.Fatalf("expected 2 overlays, got %d", len(cfg.Overlays))
	}

	// Check first overlay
	if cfg.Overlays[0].VNI != 100 {
		t.Errorf("expected overlay[0].vni = 100, got %d", cfg.Overlays[0].VNI)
	}
	if cfg.Overlays[0].UnderlayInterface != "ens3" {
		t.Errorf("expected overlay[0].underlay_interface = 'ens3', got '%s'", cfg.Overlays[0].UnderlayInterface)
	}
	if cfg.Overlays[0].Routing.Import.Install.Table != 100 {
		t.Errorf("expected table = 100, got %d", cfg.Overlays[0].Routing.Import.Install.Table)
	}

	// Check second overlay
	if cfg.Overlays[1].VNI != 200 {
		t.Errorf("expected overlay[1].vni = 200, got %d", cfg.Overlays[1].VNI)
	}
}

func TestConfig_GetOverlays_V2(t *testing.T) {
	cfg := &Config{
		Version: 2,
		Overlays: []OverlayDef{
			{VNI: 100, Name: "vxlan100", Bridge: "br-100"},
			{VNI: 200, Name: "vxlan200", Bridge: "br-200"},
		},
	}

	overlays := cfg.GetOverlays()
	if len(overlays) != 2 {
		t.Fatalf("expected 2 overlays, got %d", len(overlays))
	}

	if overlays[0].VNI != 100 || overlays[1].VNI != 200 {
		t.Errorf("unexpected VNIs: got %d and %d", overlays[0].VNI, overlays[1].VNI)
	}
}

func TestConfig_GetOverlays_V1Compatibility(t *testing.T) {
	cfg := &Config{
		Version: 1,
		Overlay: OverlayConfig{
			VXLAN: VXLANConfig{
				VNI:      100,
				Name:     "vxlan100",
				DstPort:  4789,
				Learning: true,
				MTU:      1450,
				Bridge:   "br-nnet-100",
			},
		},
		Routing: RoutingConfig{
			Export: ExportConfig{
				Networks: []string{"172.16.10.0/24"},
				Metric:   100,
			},
			Import: ImportConfig{
				Install: InstallConfig{
					Table: 100,
				},
			},
		},
	}

	overlays := cfg.GetOverlays()
	if len(overlays) != 1 {
		t.Fatalf("expected 1 overlay from v1 conversion, got %d", len(overlays))
	}

	o := overlays[0]
	if o.VNI != 100 {
		t.Errorf("expected VNI = 100, got %d", o.VNI)
	}
	if o.Name != "vxlan100" {
		t.Errorf("expected Name = 'vxlan100', got '%s'", o.Name)
	}
	if o.Bridge != "br-nnet-100" {
		t.Errorf("expected Bridge = 'br-nnet-100', got '%s'", o.Bridge)
	}

	// Verify routing was migrated
	if len(o.Routing.Export.Networks) != 1 {
		t.Errorf("expected 1 export network, got %d", len(o.Routing.Export.Networks))
	}
	if o.Routing.Import.Install.Table != 100 {
		t.Errorf("expected import table = 100, got %d", o.Routing.Import.Install.Table)
	}
}

func TestConfig_GetOverlays_EmptyConfig(t *testing.T) {
	cfg := &Config{}

	overlays := cfg.GetOverlays()
	if overlays != nil {
		t.Errorf("expected nil for empty config, got %v", overlays)
	}
}
