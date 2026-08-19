package agentconfig

import (
	"fmt"
	"net"
	"os"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"

	"github.com/nishisan-dev/n-netman/internal/inject"
)

// Loader loads and validates nnet-agent configuration.
type Loader struct {
	validate *validator.Validate
}

// NewLoader creates a new loader.
func NewLoader() *Loader {
	return &Loader{validate: validator.New()}
}

// LoadFile loads and validates configuration from a YAML file.
func (l *Loader) LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read agent config: %w", err)
	}
	return l.Load(data)
}

// Load parses and validates configuration from YAML bytes.
func (l *Loader) Load(data []byte) (*Config, error) {
	cfg := Defaults()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse agent config: %w", err)
	}
	if err := l.Validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks a configuration struct.
func (l *Loader) Validate(cfg *Config) error {
	if err := l.validate.Struct(cfg); err != nil {
		if verrs, ok := err.(validator.ValidationErrors); ok {
			return fmt.Errorf("agent config validation failed: %s", formatValidationErrors(verrs))
		}
		return fmt.Errorf("agent config validation failed: %w", err)
	}
	return l.validateSemantics(cfg)
}

func (l *Loader) validateSemantics(cfg *Config) error {
	base := net.ParseIP(cfg.Inject.GetGroupBase())
	if base == nil || !base.IsMulticast() {
		return fmt.Errorf("inject.group_base %q is not a multicast address", cfg.Inject.GetGroupBase())
	}

	seenNames := make(map[string]int, len(cfg.Interfaces))
	for i, iface := range cfg.Interfaces {
		if prev, dup := seenNames[iface.Name]; dup {
			return fmt.Errorf("interfaces[%d]: duplicate interface %q (already configured at interfaces[%d])", i, iface.Name, prev)
		}
		seenNames[iface.Name] = i

		// Phase 1 is static addressing: the address is what makes the NIC
		// usable at all, so it is required rather than inferred.
		if _, _, err := net.ParseCIDR(iface.Address); err != nil {
			return fmt.Errorf("interfaces[%d] (%s): address %q is not a valid CIDR: %w", i, iface.Name, iface.Address, err)
		}

		// Without one of these the agent has no group to join.
		if iface.VNI == 0 && iface.Group == "" {
			return fmt.Errorf("interfaces[%d] (%s): either vni or group is required", i, iface.Name)
		}

		group, err := l.resolveGroup(cfg, iface)
		if err != nil {
			return fmt.Errorf("interfaces[%d] (%s): %w", i, iface.Name, err)
		}
		if !group.IsMulticast() {
			return fmt.Errorf("interfaces[%d] (%s): group %s is not a multicast address", i, iface.Name, group)
		}

		for _, tag := range iface.ExpectTags {
			if tag == "" {
				return fmt.Errorf("interfaces[%d] (%s): expect_tags contains an empty tag", i, iface.Name)
			}
		}
	}

	return nil
}

func (l *Loader) resolveGroup(cfg *Config, iface InterfaceConfig) (net.IP, error) {
	if iface.Group != "" {
		ip := net.ParseIP(iface.Group)
		if ip == nil {
			return nil, fmt.Errorf("group %q is not a valid IP", iface.Group)
		}
		return ip, nil
	}
	return inject.GroupForVNI(net.ParseIP(cfg.Inject.GetGroupBase()), uint32(iface.VNI))
}

// GroupFor returns the multicast group an interface listens on.
//
// It derives the group from the VNI with the same formula the controller uses,
// unless the interface pins one explicitly.
func (c *Config) GroupFor(iface InterfaceConfig) (net.IP, error) {
	if iface.Group != "" {
		ip := net.ParseIP(iface.Group)
		if ip == nil {
			return nil, fmt.Errorf("interface %s: group %q is not a valid IP", iface.Name, iface.Group)
		}
		return ip, nil
	}
	return inject.GroupForVNI(net.ParseIP(c.Inject.GetGroupBase()), uint32(iface.VNI))
}

// PortFor returns the UDP port an interface listens on.
func (c *Config) PortFor(iface InterfaceConfig) int {
	if iface.Port > 0 {
		return iface.Port
	}
	return c.Inject.GetPort()
}

func formatValidationErrors(errors validator.ValidationErrors) string {
	var result string
	for i, err := range errors {
		if i > 0 {
			result += "; "
		}
		result += fmt.Sprintf("field '%s' failed on '%s' validation", err.Field(), err.Tag())
	}
	return result
}
