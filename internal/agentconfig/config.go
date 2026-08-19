// Package agentconfig defines the configuration for nnet-agent, the daemon that
// runs inside a VM.
//
// It is deliberately separate from internal/config: an agent has no overlays,
// no peers, no libvirt and no PKI, and should not be able to be pointed at a
// controller configuration by mistake. The observability block is shared,
// because the endpoints behave identically on both sides.
package agentconfig

import (
	"fmt"

	"github.com/nishisan-dev/n-netman/internal/config"
	"github.com/nishisan-dev/n-netman/internal/routepolicy"
)

// Config is the root configuration for nnet-agent.
type Config struct {
	Version       int               `yaml:"version" validate:"required,eq=1"`
	Agent         AgentIdentity     `yaml:"agent" validate:"required"`
	Inject        ChannelDefaults   `yaml:"inject"`
	Interfaces    []InterfaceConfig `yaml:"interfaces" validate:"required,min=1,dive"`
	Observability config.ObsConfig  `yaml:"observability"`
}

// AgentIdentity names this agent in logs and status output.
type AgentIdentity struct {
	ID string `yaml:"id" validate:"required"`
}

// ChannelDefaults holds settings shared by every interface.
type ChannelDefaults struct {
	// GroupBase must match the controller's routing.inject.group_base.
	GroupBase string `yaml:"group_base" validate:"omitempty,ipv4"`
	// Port is the default UDP port, overridable per interface.
	Port int `yaml:"port" validate:"omitempty,min=1,max=65535"`
}

// GetGroupBase returns the multicast base, defaulting to the controller's.
func (c ChannelDefaults) GetGroupBase() string {
	if c.GroupBase == "" {
		return config.DefaultInjectGroupBase
	}
	return c.GroupBase
}

// GetPort returns the default UDP port.
func (c ChannelDefaults) GetPort() int {
	if c.Port <= 0 {
		return config.DefaultInjectPort
	}
	return c.Port
}

// InterfaceConfig describes one NIC the agent manages.
type InterfaceConfig struct {
	// Name is the interface inside the VM, e.g. "ens3".
	Name string `yaml:"name" validate:"required"`

	// Address is the static address in CIDR form. Phase 1 is static only.
	Address string `yaml:"address" validate:"required"`

	// VNI identifies the segment and derives the multicast group. Either this
	// or an explicit Group is required.
	VNI int `yaml:"vni" validate:"omitempty,min=1,max=16777215"`

	// Group overrides the derived multicast group.
	Group string `yaml:"group" validate:"omitempty,ipv4"`

	// Port overrides the channel default.
	Port int `yaml:"port" validate:"omitempty,min=1,max=65535"`

	// ExpectTags guards against a NIC cabled to the wrong segment: an
	// advertisement is only accepted when its segment carries every tag listed
	// here. Empty accepts any segment.
	ExpectTags []string `yaml:"expect_tags"`

	// PSKRef points at the shared key, using the same "file:/path" convention
	// as the controller.
	PSKRef string `yaml:"psk_ref" validate:"required"`

	// KeyIDs pins which key ids are accepted. Empty accepts any.
	KeyIDs []string `yaml:"key_ids"`

	Install InstallConfig `yaml:"install"`
	Import  ImportPolicy  `yaml:"import"`
}

// InstallConfig controls how received routes reach the kernel.
type InstallConfig struct {
	// Table is the routing table. Zero means the main table, which is what a VM
	// wants: ordinary traffic uses it without any policy rule.
	Table int `yaml:"table" validate:"omitempty,min=1,max=252"`

	// Metric applied to installed routes.
	Metric int `yaml:"metric" validate:"omitempty,min=1,max=2147483647"`

	// AcceptDefaultGateway opts in to installing an advertised default route.
	// It is off by default so an advertisement cannot silently take over the
	// default route netplan already configured.
	AcceptDefaultGateway bool `yaml:"accept_default_gateway"`
}

// GetMetric returns the metric applied to installed routes.
func (i InstallConfig) GetMetric() int {
	if i.Metric <= 0 {
		return DefaultRouteMetric
	}
	return i.Metric
}

// DefaultRouteMetric matches the project's default route metric.
const DefaultRouteMetric = 100

// ImportPolicy is the agent's allow/deny list over advertised prefixes.
type ImportPolicy struct {
	AcceptAll bool     `yaml:"accept_all"`
	Allow     []string `yaml:"allow" validate:"dive,cidr"`
	Deny      []string `yaml:"deny" validate:"dive,cidr"`
}

// Policy adapts the configuration into the shared evaluation type.
func (p ImportPolicy) Policy() routepolicy.Policy {
	return routepolicy.Policy{
		AcceptAll: p.AcceptAll,
		Allow:     p.Allow,
		Deny:      p.Deny,
	}
}

// Defaults returns a configuration with sensible defaults applied.
func Defaults() *Config {
	return &Config{
		// Left at zero so a missing 'version' key fails validation rather than
		// being silently treated as v1, matching the controller loader.
		Version: 0,
		Observability: config.ObsConfig{
			Logging: config.LoggingConfig{Level: "info", Format: "json"},
			Metrics: config.MetricsConfig{
				Enabled: true,
				Listen:  config.ListenConfig{Address: "127.0.0.1", Port: 9111},
			},
			Healthcheck: config.HealthcheckConfig{
				Enabled: true,
				Listen:  config.ListenConfig{Address: "127.0.0.1", Port: 9112},
			},
		},
	}
}

// Warnings reports configuration that loads but deserves operator attention.
func (c *Config) Warnings() []string {
	var out []string
	for _, iface := range c.Interfaces {
		if len(iface.ExpectTags) == 0 {
			out = append(out, fmt.Sprintf(
				"interface %s has no expect_tags: it will accept advertisements from any segment reachable on that NIC",
				iface.Name))
		}
		if iface.Install.AcceptDefaultGateway {
			out = append(out, fmt.Sprintf(
				"interface %s accepts an advertised default gateway: it may replace the default route configured locally",
				iface.Name))
		}
	}
	return out
}
