package libvirt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	// SystemdDropInDir is the directory for libvirt service overrides.
	SystemdDropInDir = "/etc/systemd/system/libvirt.service.d"
	// DropInFileName is the name of the n-netman dependency drop-in.
	DropInFileName = "n-netman.conf"
)

// dropInContent is the systemd drop-in that makes libvirt depend on n-netman.
const dropInContent = `[Unit]
After=n-netman.service
Requires=n-netman.service
`

// GetDropInPath returns the full path to the drop-in file.
func GetDropInPath() string {
	return filepath.Join(SystemdDropInDir, DropInFileName)
}

// EnableDependency creates the systemd drop-in to make libvirt depend on n-netman.
func EnableDependency() error {
	// Create directory if it doesn't exist
	if err := os.MkdirAll(SystemdDropInDir, 0755); err != nil {
		return fmt.Errorf("failed to create drop-in directory: %w", err)
	}

	// Write the drop-in file
	dropInPath := GetDropInPath()
	if err := os.WriteFile(dropInPath, []byte(dropInContent), 0644); err != nil {
		return fmt.Errorf("failed to write drop-in file: %w", err)
	}

	// Reload systemd
	if err := daemonReload(); err != nil {
		return err
	}

	return nil
}

// DisableDependency removes the systemd drop-in.
func DisableDependency() error {
	dropInPath := GetDropInPath()

	// Check if file exists
	if _, err := os.Stat(dropInPath); os.IsNotExist(err) {
		return nil // Already disabled
	}

	// Remove the file
	if err := os.Remove(dropInPath); err != nil {
		return fmt.Errorf("failed to remove drop-in file: %w", err)
	}

	// Try to remove the directory if empty
	_ = os.Remove(SystemdDropInDir)

	// Reload systemd
	if err := daemonReload(); err != nil {
		return err
	}

	return nil
}

// IsDependencyEnabled checks if the systemd drop-in exists.
func IsDependencyEnabled() bool {
	_, err := os.Stat(GetDropInPath())
	return err == nil
}

// daemonReload runs systemctl daemon-reload.
func daemonReload() error {
	out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %s - %w", string(out), err)
	}
	return nil
}

// GetServiceStatus returns the status of a systemd service.
func GetServiceStatus(service string) (string, error) {
	out, err := exec.Command("systemctl", "is-active", service).Output()
	if err != nil {
		// is-active returns non-zero for inactive services
		return "inactive", nil
	}
	return string(out), nil
}
