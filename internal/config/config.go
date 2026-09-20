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
	Peers         []PeerConfig   `yaml:"peers"`    // Novo (v2): peers no nível raiz
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
	VNI      int       `yaml:"vni" validate:"omitempty,min=1,max=16777215"`
	Name     string    `yaml:"name"`
	DstPort  int       `yaml:"dstport" validate:"omitempty,min=1,max=65535"`
	Learning bool      `yaml:"learning"`
	MTU      int       `yaml:"mtu" validate:"omitempty,min=1280,max=9000"`
	Bridge   string    `yaml:"bridge"`
	BUM      BUMConfig `yaml:"bum"`
}

// OverlayDef defines a complete overlay with its own routing context.
// This is used in v2 multi-overlay configs.
type OverlayDef struct {
	VNI               int            `yaml:"vni" validate:"required,min=1,max=16777215"`
	Name              string         `yaml:"name" validate:"required"`
	DstPort           int            `yaml:"dstport" validate:"omitempty,min=1,max=65535"`
	Learning          bool           `yaml:"learning"`
	MTU               int            `yaml:"mtu" validate:"omitempty,min=1280,max=9000"`
	Bridge            BridgeConfig   `yaml:"bridge" validate:"required"`
	UnderlayInterface string         `yaml:"underlay_interface"`
	BUM               BUMConfig      `yaml:"bum"`
	Routing           OverlayRouting `yaml:"routing"`
}

// BridgeConfig defines the bridge interface for an overlay.
type BridgeConfig struct {
	Name string `yaml:"name" validate:"required"`
	IPv4 string `yaml:"ipv4,omitempty"` // CIDR format, e.g. "10.100.0.1/24" (validated in validateSemantics)
	IPv6 string `yaml:"ipv6,omitempty"` // CIDR format, e.g. "fd00:100::1/64" (validated in validateSemantics)
	// Tags are free-form labels describing the segment this bridge carries
	// (e.g. "it", "external"). routing.inject rules select bridges by tag, so
	// the controller decides what to advertise per segment without knowing
	// which VMs are attached.
	Tags []string `yaml:"tags,omitempty"`
}

// UnmarshalYAML implements custom unmarshaling to support both string and struct formats.
// This provides backward compatibility: "bridge: br-prod" (string) or "bridge: {name: br-prod, ipv4: ...}" (struct)
func (b *BridgeConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// Try string first (legacy format)
	var name string
	if err := unmarshal(&name); err == nil && name != "" {
		b.Name = name
		return nil
	}

	// Try struct format (new format with IP)
	type bridgeConfigAlias BridgeConfig // Alias to avoid infinite recursion
	var alias bridgeConfigAlias
	if err := unmarshal(&alias); err != nil {
		return err
	}
	*b = BridgeConfig(alias)
	return nil
}

// BUMConfig defines how BUM (Broadcast, Unknown Unicast, Multicast) traffic is handled.
// This is critical for VXLAN operation as it determines how the kernel forwards
// traffic to unknown destinations (e.g., ARP requests).
type BUMConfig struct {
	// Mode: "head-end-replication" (default) or "multicast"
	// - head-end-replication: FDB entries with MAC 00:00:00:00:00:00 for each peer
	// - multicast: Uses IP multicast group for BUM flooding
	Mode string `yaml:"mode" validate:"omitempty,oneof=head-end-replication multicast"`
	// Group: Multicast group IP address (only used when mode=multicast)
	// Example: "239.1.1.100" for VNI 100
	Group string `yaml:"group" validate:"omitempty,ip"`
}

// GetMode returns the BUM mode, defaulting to "head-end-replication" if not set.
func (b *BUMConfig) GetMode() string {
	if b.Mode == "" {
		return "head-end-replication"
	}
	return b.Mode
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
	// VNIs lists the overlay VNIs this peer participates in (v2). When empty,
	// the peer participates in all overlays (backward compatible).
	VNIs []int `yaml:"vnis"`
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
	Inject  InjectConfig `yaml:"inject"`
}

// ExportConfig defines which routes this node announces.
type ExportConfig struct {
	ExportAll            bool     `yaml:"export_all"`
	Networks             []string `yaml:"networks" validate:"dive,cidr"`
	IncludeConnected     bool     `yaml:"include_connected"`
	IncludeNetplanStatic bool     `yaml:"include_netplan_static"`
	Metric               int      `yaml:"metric"`
	// Tags are communities attached to every route exported by this overlay.
	// They travel with the route to peers and are what routing.inject rules
	// filter on via route_tags.
	Tags []string `yaml:"tags,omitempty"`
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
	Table             int               `yaml:"table" validate:"omitempty,min=1,max=252"`
	ReplaceExisting   bool              `yaml:"replace_existing"`
	FlushOnPeerDown   bool              `yaml:"flush_on_peer_down"`
	RouteLeaseSeconds int               `yaml:"route_lease_seconds"`
	LookupRules       LookupRulesConfig `yaml:"lookup_rules"`
}

// LookupRulesConfig defines policy-based routing rules (ip rule).
// When enabled, creates rules like: ip rule add iif <bridge> lookup <table>
type LookupRulesConfig struct {
	Enabled bool   `yaml:"enabled"`
	Mode    string `yaml:"mode" validate:"omitempty,oneof=interface prefix"` // "interface" (default) or "prefix"
}

// InjectConfig defines the multicast channel used to advertise routes to
// agents running inside VMs attached to a tagged bridge.
//
// Rules live at the root of 'routing:' rather than per overlay so a single
// policy can cover every segment carrying a given tag.
type InjectConfig struct {
	Enabled bool `yaml:"enabled"`
	// GroupBase is the base of the multicast range. The group for an overlay is
	// derived by OR-ing the low 16 bits of the VNI into the last two octets, so
	// the base must have both of them set to zero. Defaults to 239.8.0.0.
	GroupBase string `yaml:"group_base" validate:"omitempty,ipv4"`
	// Port is the UDP port of the inject channel. Defaults to 4790.
	Port int `yaml:"port" validate:"omitempty,min=1,max=65535"`
	// IntervalSeconds is how often an advertisement is republished.
	IntervalSeconds int `yaml:"interval_seconds" validate:"omitempty,min=1,max=300"`
	// LeaseSeconds is how long agents keep the routes without a refresh.
	LeaseSeconds int `yaml:"lease_seconds" validate:"omitempty,min=5,max=3600"`
	// PSKRef points at the shared key used to authenticate advertisements,
	// following the same "file:/path" convention as peers[].auth.psk_ref.
	PSKRef string `yaml:"psk_ref"`
	// KeyID identifies the key in published envelopes, enabling rotation.
	KeyID string       `yaml:"key_id"`
	Rules []InjectRule `yaml:"rules" validate:"dive"`
}

// InjectRule selects bridges by tag and describes what to advertise to them.
type InjectRule struct {
	// MatchTags uses AND semantics: every tag listed must be present on the
	// bridge for the rule to apply. What a segment receives is the union of
	// every rule that matched.
	MatchTags []string `yaml:"match_tags" validate:"required,min=1"`
	// Networks are prefixes advertised verbatim.
	Networks []string `yaml:"networks" validate:"dive,cidr"`
	// FromRIB also advertises routes learned from peers for the overlay.
	FromRIB bool `yaml:"from_rib"`
	// RouteTags filters RIB routes by their communities. Empty means no filter.
	RouteTags []string `yaml:"route_tags"`
	// NextHop overrides the advertised next-hop. Defaults to the bridge IPv4,
	// which is the controller's own address on the segment.
	NextHop string `yaml:"next_hop" validate:"omitempty,ip"`
	Metric  int    `yaml:"metric" validate:"omitempty,min=1,max=4294967295"`
	// DefaultGateway advertises a default route for the segment. Agents install
	// it only when the receiving interface opts in.
	DefaultGateway string `yaml:"default_gateway" validate:"omitempty,ip"`
}

// DefaultInjectGroupBase is the base of the multicast range used by the inject
// channel. It is deliberately distinct from the 239.1.1.x convention used for
// VXLAN BUM so the two never collide.
const DefaultInjectGroupBase = "239.8.0.0"

// Default values for the inject channel.
const (
	DefaultInjectPort     = 4790
	DefaultInjectInterval = 10
	DefaultInjectLease    = 30
)

// GetGroupBase returns the configured multicast base, or the default.
func (i *InjectConfig) GetGroupBase() string {
	if i.GroupBase == "" {
		return DefaultInjectGroupBase
	}
	return i.GroupBase
}

// GetPort returns the configured UDP port, or the default.
func (i *InjectConfig) GetPort() int {
	if i.Port <= 0 {
		return DefaultInjectPort
	}
	return i.Port
}

// GetInterval returns the advertisement interval.
func (i *InjectConfig) GetInterval() time.Duration {
	if i.IntervalSeconds <= 0 {
		return DefaultInjectInterval * time.Second
	}
	return time.Duration(i.IntervalSeconds) * time.Second
}

// GetLeaseSeconds returns the lease advertised to agents.
func (i *InjectConfig) GetLeaseSeconds() int {
	if i.LeaseSeconds <= 0 {
		return DefaultInjectLease
	}
	return i.LeaseSeconds
}

// MatchingRules returns the rules that apply to a bridge carrying the given
// tags. A rule matches when every one of its match_tags is present (AND).
func (i *InjectConfig) MatchingRules(bridgeTags []string) []InjectRule {
	if len(bridgeTags) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(bridgeTags))
	for _, t := range bridgeTags {
		have[t] = struct{}{}
	}

	var out []InjectRule
	for _, r := range i.Rules {
		matched := true
		for _, want := range r.MatchTags {
			if _, ok := have[want]; !ok {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, r)
		}
	}
	return out
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
		// Version intentionally 0 so that an absent 'version' key fails the
		// 'required' validation instead of being silently treated as v1.
		Version: 0,
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
				Bridge:   BridgeConfig{Name: c.Overlay.VXLAN.Bridge}, // Convert string to struct
				BUM:      c.Overlay.VXLAN.BUM,                        // Propagate BUM config
				Routing: OverlayRouting{
					Export: c.Routing.Export,
					Import: c.Routing.Import,
				},
			},
		}
	}

	return nil
}

// GetPeers returns the list of peers for the active config version.
// v2 configs declare peers at the root ('peers:'); v1 configs use the legacy
// 'overlay.peers' block.
func (c *Config) GetPeers() []PeerConfig {
	if c.Version >= 2 {
		return c.Peers
	}
	return c.Overlay.Peers
}

// GetPeersForVNI returns the peers that participate in the given overlay VNI.
// A peer with no explicit VNIs participates in every overlay (backward compatible).
func (c *Config) GetPeersForVNI(vni int) []PeerConfig {
	var out []PeerConfig
	for _, p := range c.GetPeers() {
		if len(p.VNIs) == 0 {
			out = append(out, p)
			continue
		}
		for _, v := range p.VNIs {
			if v == vni {
				out = append(out, p)
				break
			}
		}
	}
	return out
}
