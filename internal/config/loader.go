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
	// Validate VXLAN bridge reference exists in KVM bridges (if KVM enabled)
	if cfg.KVM.Enabled && len(cfg.KVM.Bridges) > 0 {
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

	// Validate at least one peer is defined (warning only)
	if len(cfg.Overlay.Peers) == 0 {
		// This is fine for initial setup, just a warning
	}

	// Validate routing policies don't overlap
	if cfg.Routing.Import.AcceptAll && len(cfg.Routing.Import.Deny) > 0 {
		// accept_all with deny list is valid (accept all except denied)
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
