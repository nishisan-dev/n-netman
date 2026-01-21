// Package config defines the configuration structures for n-netman.
package config

import "time"

// Config is the root configuration structure for n-netman.
type Config struct {
	Version       int            `yaml:"version" validate:"required,min=1,max=2"`
	Node          NodeConfig     `yaml:"node" validate:"required"`
	Netplan       NetplanConfig  `yaml:"netplan"`
	KVM           KVMConfig      `yaml:"kvm"`
	Overlay       OverlayConfig  `yaml:"overlay"`  // Legado (v1)
	Overlays      []OverlayDef   `yaml:"overlays"` // Novo (v2)
	Routing       RoutingConfig  `yaml:"routing"`  // Global fallback
	Topology      TopologyConfig `yaml:"topology"`
	Security      SecurityConfig `yaml:"security"`
	Observability ObsConfig      `yaml:"observability"`
}

// NodeConfig defines the identity of this host.
type NodeConfig struct {
	ID       string   `yaml:"id" validate:"required"`
	Hostname string   `yaml:"hostname"`
	Tags     []string `yaml:"tags"`
}

// NetplanConfig defines integration with netplan for underlay inference.
type NetplanConfig struct {
	Enabled     bool           `yaml:"enabled"`
	ConfigPaths []string       `yaml:"config_paths"`
	Underlay    UnderlayConfig `yaml:"underlay"`
}

// UnderlayConfig defines preferences for underlay interface selection.
type UnderlayConfig struct {
	PreferInterfaces      []string `yaml:"prefer_interfaces"`
	PreferAddressFamilies []string `yaml:"prefer_address_families" validate:"dive,oneof=ipv4 ipv6"`
}

// KVMConfig defines integration with KVM/libvirt.
type KVMConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Provider string        `yaml:"provider" validate:"omitempty,eq=libvirt"`
	Libvirt  LibvirtConfig `yaml:"libvirt"`
	Bridges  []BridgeDef   `yaml:"bridges"`
	Attach   AttachConfig  `yaml:"attach"`
}

// LibvirtConfig defines libvirt-specific settings.
type LibvirtConfig struct {
	URI     string        `yaml:"uri"`
	Mode    string        `yaml:"mode" validate:"omitempty,oneof=linux-bridge libvirt-network"`
	Network NetworkConfig `yaml:"network"`
}

// NetworkConfig defines libvirt network settings.
type NetworkConfig struct {
	Name        string `yaml:"name"`
	Autostart   bool   `yaml:"autostart"`
	ForwardMode string `yaml:"forward_mode" validate:"omitempty,oneof=bridge nat route"`
}

// BridgeDef defines a Linux bridge to be managed.
type BridgeDef struct {
	Name   string `yaml:"name" validate:"required"`
	STP    bool   `yaml:"stp"`
	MTU    int    `yaml:"mtu" validate:"omitempty,min=1280,max=9000"`
	Manage bool   `yaml:"manage"`
}

// AttachConfig defines VM attachment settings.
type AttachConfig struct {
	Enabled  bool           `yaml:"enabled"`
	Strategy string         `yaml:"strategy" validate:"omitempty,oneof=by-name by-tag regex"`
	Targets  []AttachTarget `yaml:"targets"`
}

// AttachTarget defines a VM to bridge mapping.
type AttachTarget struct {
	VM     string `yaml:"vm" validate:"required"`
	Bridge string `yaml:"bridge" validate:"required"`
	Model  string `yaml:"model"`
}

// OverlayConfig defines the VXLAN overlay settings.
type OverlayConfig struct {
	VXLAN VXLANConfig  `yaml:"vxlan" validate:"required"`
	Peers []PeerConfig `yaml:"peers"`
}

// VXLANConfig defines VXLAN tunnel settings.
// Note: required validation is done in validateSemantics based on config version.
type VXLANConfig struct {
	VNI      int    `yaml:"vni" validate:"omitempty,min=1,max=16777215"`
	Name     string `yaml:"name"`
	DstPort  int    `yaml:"dstport" validate:"omitempty,min=1,max=65535"`
	Learning bool   `yaml:"learning"`
	MTU      int    `yaml:"mtu" validate:"omitempty,min=1280,max=9000"`
	Bridge   string `yaml:"bridge"`
}

// OverlayDef defines a complete overlay with its own routing context.
// This is used in v2 multi-overlay configs.
type OverlayDef struct {
	VNI               int            `yaml:"vni" validate:"required,min=1,max=16777215"`
	Name              string         `yaml:"name" validate:"required"`
	DstPort           int            `yaml:"dstport" validate:"omitempty,min=1,max=65535"`
	Learning          bool           `yaml:"learning"`
	MTU               int            `yaml:"mtu" validate:"omitempty,min=1280,max=9000"`
	Bridge            string         `yaml:"bridge" validate:"required"`
	UnderlayInterface string         `yaml:"underlay_interface"`
	Routing           OverlayRouting `yaml:"routing"`
}

// OverlayRouting defines routing policies specific to an overlay.
type OverlayRouting struct {
	Export ExportConfig `yaml:"export"`
	Import ImportConfig `yaml:"import"`
}

// PeerConfig defines a remote peer for VXLAN overlay.
type PeerConfig struct {
	ID       string         `yaml:"id" validate:"required"`
	Endpoint EndpointConfig `yaml:"endpoint" validate:"required"`
	Auth     AuthConfig     `yaml:"auth"`
	Health   HealthConfig   `yaml:"health"`
}

// EndpointConfig defines the network endpoint of a peer.
type EndpointConfig struct {
	Address      string `yaml:"address" validate:"required,ip"`
	ViaInterface string `yaml:"via_interface"`
}

// AuthConfig defines authentication settings for a peer.
type AuthConfig struct {
	Mode   string `yaml:"mode" validate:"omitempty,oneof=psk none"`
	PSKRef string `yaml:"psk_ref"`
}

// HealthConfig defines health check settings for a peer.
type HealthConfig struct {
	KeepaliveIntervalMs int `yaml:"keepalive_interval_ms"`
	DeadAfterMs         int `yaml:"dead_after_ms"`
}

// RoutingConfig defines route export/import settings.
type RoutingConfig struct {
	Enabled bool         `yaml:"enabled"`
	Export  ExportConfig `yaml:"export"`
	Import  ImportConfig `yaml:"import"`
}

// ExportConfig defines which routes this node announces.
type ExportConfig struct {
	ExportAll            bool     `yaml:"export_all"`
	Networks             []string `yaml:"networks" validate:"dive,cidr"`
	IncludeConnected     bool     `yaml:"include_connected"`
	IncludeNetplanStatic bool     `yaml:"include_netplan_static"`
	Metric               int      `yaml:"metric"`
}

// ImportConfig defines which routes this node accepts.
type ImportConfig struct {
	AcceptAll bool          `yaml:"accept_all"`
	Allow     []string      `yaml:"allow" validate:"dive,cidr"`
	Deny      []string      `yaml:"deny" validate:"dive,cidr"`
	Install   InstallConfig `yaml:"install"`
}

// InstallConfig defines how imported routes are installed.
type InstallConfig struct {
	Table             int  `yaml:"table" validate:"omitempty,min=1,max=252"`
	ReplaceExisting   bool `yaml:"replace_existing"`
	FlushOnPeerDown   bool `yaml:"flush_on_peer_down"`
	RouteLeaseSeconds int  `yaml:"route_lease_seconds"`
}

// TopologyConfig defines the network topology mode.
type TopologyConfig struct {
	Mode          string              `yaml:"mode" validate:"omitempty,oneof=direct-preferred full-mesh hub-spoke static"`
	RelayFallback bool                `yaml:"relay_fallback"`
	Transit       string              `yaml:"transit" validate:"omitempty,oneof=deny allow"`
	TransitPolicy TransitPolicyConfig `yaml:"transit_policy"`
}

// TransitPolicyConfig defines transit routing policies.
type TransitPolicyConfig struct {
	AllowedTransitPeers []string `yaml:"allowed_transit_peers"`
	DeniedTransitPeers  []string `yaml:"denied_transit_peers"`
}

// SecurityConfig defines control plane security settings.
type SecurityConfig struct {
	ControlPlane ControlPlaneConfig `yaml:"control_plane"`
}

// ControlPlaneConfig defines the gRPC control plane settings.
type ControlPlaneConfig struct {
	Transport string       `yaml:"transport" validate:"omitempty,eq=grpc"`
	Listen    ListenConfig `yaml:"listen"`
	TLS       TLSConfig    `yaml:"tls"`
}

// ListenConfig defines listen address and port.
type ListenConfig struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port" validate:"omitempty,min=1,max=65535"`
}

// TLSConfig defines TLS settings.
type TLSConfig struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	CAFile   string `yaml:"ca_file"`
}

// ObsConfig defines observability settings.
type ObsConfig struct {
	Logging     LoggingConfig     `yaml:"logging"`
	Metrics     MetricsConfig     `yaml:"metrics"`
	Healthcheck HealthcheckConfig `yaml:"healthcheck"`
}

// LoggingConfig defines logging settings.
type LoggingConfig struct {
	Level  string `yaml:"level" validate:"omitempty,oneof=debug info warn error"`
	Format string `yaml:"format" validate:"omitempty,oneof=json text"`
}

// MetricsConfig defines Prometheus metrics settings.
type MetricsConfig struct {
	Enabled bool         `yaml:"enabled"`
	Listen  ListenConfig `yaml:"listen"`
}

// HealthcheckConfig defines healthcheck endpoint settings.
type HealthcheckConfig struct {
	Enabled bool         `yaml:"enabled"`
	Listen  ListenConfig `yaml:"listen"`
}

// Defaults returns a Config with sensible default values.
func Defaults() *Config {
	return &Config{
		Version: 1,
		Netplan: NetplanConfig{
			Enabled:     true,
			ConfigPaths: []string{"/etc/netplan"},
			Underlay: UnderlayConfig{
				PreferAddressFamilies: []string{"ipv4"},
			},
		},
		KVM: KVMConfig{
			Enabled:  false,
			Provider: "libvirt",
			Libvirt: LibvirtConfig{
				URI:  "qemu:///system",
				Mode: "linux-bridge",
			},
		},
		Overlay: OverlayConfig{
			VXLAN: VXLANConfig{
				DstPort:  4789,
				Learning: true,
				MTU:      1450,
			},
		},
		Routing: RoutingConfig{
			Enabled: true,
			Export: ExportConfig{
				Metric: 100,
			},
			Import: ImportConfig{
				Install: InstallConfig{
					Table:             100,
					ReplaceExisting:   true,
					FlushOnPeerDown:   true,
					RouteLeaseSeconds: 30,
				},
			},
		},
		Topology: TopologyConfig{
			Mode:          "direct-preferred",
			RelayFallback: true,
			Transit:       "deny",
		},
		Security: SecurityConfig{
			ControlPlane: ControlPlaneConfig{
				Transport: "grpc",
				Listen: ListenConfig{
					Address: "0.0.0.0",
					Port:    9898,
				},
			},
		},
		Observability: ObsConfig{
			Logging: LoggingConfig{
				Level:  "info",
				Format: "json",
			},
			Metrics: MetricsConfig{
				Enabled: true,
				Listen: ListenConfig{
					Address: "127.0.0.1",
					Port:    9109,
				},
			},
			Healthcheck: HealthcheckConfig{
				Enabled: true,
				Listen: ListenConfig{
					Address: "127.0.0.1",
					Port:    9110,
				},
			},
		},
	}
}

// KeepAliveDuration returns the keepalive interval as a time.Duration.
func (h *HealthConfig) KeepAliveDuration() time.Duration {
	if h.KeepaliveIntervalMs <= 0 {
		return 1500 * time.Millisecond
	}
	return time.Duration(h.KeepaliveIntervalMs) * time.Millisecond
}

// DeadAfterDuration returns the dead after timeout as a time.Duration.
func (h *HealthConfig) DeadAfterDuration() time.Duration {
	if h.DeadAfterMs <= 0 {
		return 6000 * time.Millisecond
	}
	return time.Duration(h.DeadAfterMs) * time.Millisecond
}

// GetOverlays returns the list of overlay definitions.
// For v1 configs with singular overlay, it converts to []OverlayDef format.
// For v2 configs, it returns the Overlays slice directly.
func (c *Config) GetOverlays() []OverlayDef {
	// If v2 overlays are defined, use them directly
	if len(c.Overlays) > 0 {
		return c.Overlays
	}

	// Convert legacy v1 config to OverlayDef format
	if c.Overlay.VXLAN.Name != "" {
		return []OverlayDef{
			{
				VNI:      c.Overlay.VXLAN.VNI,
				Name:     c.Overlay.VXLAN.Name,
				DstPort:  c.Overlay.VXLAN.DstPort,
				Learning: c.Overlay.VXLAN.Learning,
				MTU:      c.Overlay.VXLAN.MTU,
				Bridge:   c.Overlay.VXLAN.Bridge,
				Routing: OverlayRouting{
					Export: c.Routing.Export,
					Import: c.Routing.Import,
				},
			},
		}
	}

	return nil
}

// GetPeers returns the list of peers from the legacy config.
// In v2, peers are referenced differently, but this maintains compatibility.
func (c *Config) GetPeers() []PeerConfig {
	return c.Overlay.Peers
}
