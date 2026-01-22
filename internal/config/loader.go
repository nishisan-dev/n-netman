// Package config provides configuration loading and validation for n-netman.
package config

import (
	"fmt"
	"os"

	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

// Loader handles configuration loading and validation.
type Loader struct {
	validate *validator.Validate
}

// NewLoader creates a new configuration loader.
func NewLoader() *Loader {
	return &Loader{
		validate: validator.New(),
	}
}

// LoadFile loads and validates configuration from a YAML file.
func (l *Loader) LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	return l.Load(data)
}

// Load parses and validates configuration from YAML bytes.
func (l *Loader) Load(data []byte) (*Config, error) {
	// Start with defaults
	cfg := Defaults()

	// Parse YAML over defaults
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	// Validate
	if err := l.Validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// Validate validates a configuration struct.
func (l *Loader) Validate(cfg *Config) error {
	if err := l.validate.Struct(cfg); err != nil {
		if validationErrors, ok := err.(validator.ValidationErrors); ok {
			return fmt.Errorf("config validation failed: %s", formatValidationErrors(validationErrors))
		}
		return fmt.Errorf("config validation failed: %w", err)
	}

	// Additional semantic validations
	if err := l.validateSemantics(cfg); err != nil {
		return err
	}

	return nil
}

// validateSemantics performs additional validation beyond struct tags.
func (l *Loader) validateSemantics(cfg *Config) error {
	// Validate overlay configuration based on version
	if cfg.Version == 2 {
		// V2: require overlays array
		if len(cfg.Overlays) == 0 {
			return fmt.Errorf("v2 config requires at least one overlay in 'overlays' array")
		}
		// Validate each overlay
		for i, o := range cfg.Overlays {
			if o.VNI == 0 {
				return fmt.Errorf("overlay[%d]: vni is required", i)
			}
			if o.Name == "" {
				return fmt.Errorf("overlay[%d]: name is required", i)
			}
			if o.Bridge.Name == "" {
				return fmt.Errorf("overlay[%d]: bridge.name is required", i)
			}
		}
	} else {
		// V1: require legacy overlay.vxlan
		if cfg.Overlay.VXLAN.Name == "" {
			return fmt.Errorf("v1 config requires overlay.vxlan.name")
		}
		if cfg.Overlay.VXLAN.VNI == 0 {
			return fmt.Errorf("v1 config requires overlay.vxlan.vni")
		}
		if cfg.Overlay.VXLAN.Bridge == "" {
			return fmt.Errorf("v1 config requires overlay.vxlan.bridge")
		}
	}

	// Validate VXLAN bridge reference exists in KVM bridges (if KVM enabled)
	// Only for v1 configs - v2 would need to check each overlay
	if cfg.Version == 1 && cfg.KVM.Enabled && len(cfg.KVM.Bridges) > 0 {
		bridgeName := cfg.Overlay.VXLAN.Bridge
		found := false
		for _, b := range cfg.KVM.Bridges {
			if b.Name == bridgeName {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("vxlan.bridge %q not found in kvm.bridges", bridgeName)
		}
	}

	// Validate TLS configuration
	if cfg.Security.ControlPlane.TLS.Enabled {
		if cfg.Security.ControlPlane.TLS.CertFile == "" {
			return fmt.Errorf("TLS enabled but cert_file not specified")
		}
		if cfg.Security.ControlPlane.TLS.KeyFile == "" {
			return fmt.Errorf("TLS enabled but key_file not specified")
		}
		// Check that certificate files exist
		for name, path := range map[string]string{
			"cert_file": cfg.Security.ControlPlane.TLS.CertFile,
			"key_file":  cfg.Security.ControlPlane.TLS.KeyFile,
		} {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return fmt.Errorf("TLS %s not found: %s", name, path)
			}
		}
		// CA file is optional (enables mTLS when present)
		if cfg.Security.ControlPlane.TLS.CAFile != "" {
			if _, err := os.Stat(cfg.Security.ControlPlane.TLS.CAFile); os.IsNotExist(err) {
				return fmt.Errorf("TLS ca_file not found: %s", cfg.Security.ControlPlane.TLS.CAFile)
			}
		}
	}

	return nil
}

// formatValidationErrors formats validation errors into a readable string.
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
