// Package libvirt provides integration with libvirt/virsh for VM interface management.
package libvirt

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Domain represents a libvirt virtual machine.
type Domain struct {
	Name  string
	State string // running, shut off, paused
}

// Interface represents a network interface attached to a VM.
type Interface struct {
	MAC    string
	Bridge string
	Model  string
	Target string // vnetX
}

// Client wraps virsh CLI commands.
type Client struct{}

// NewClient creates a new libvirt client.
func NewClient() *Client {
	return &Client{}
}

// ListDomains returns all libvirt domains.
// If all is true, includes shut off VMs.
func (c *Client) ListDomains(all bool) ([]Domain, error) {
	args := []string{"list", "--name"}
	if all {
		args = append(args, "--all")
	}

	out, err := exec.Command("virsh", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("virsh list failed: %w", err)
	}

	var domains []Domain
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name == "" {
			continue
		}

		state, err := c.getDomainState(name)
		if err != nil {
			state = "unknown"
		}

		domains = append(domains, Domain{
			Name:  name,
			State: state,
		})
	}

	return domains, nil
}

// getDomainState returns the current state of a domain.
func (c *Client) getDomainState(name string) (string, error) {
	out, err := exec.Command("virsh", "domstate", name).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// GetDomainInterfaces returns network interfaces for a domain.
func (c *Client) GetDomainInterfaces(name string) ([]Interface, error) {
	out, err := exec.Command("virsh", "domiflist", name).Output()
	if err != nil {
		return nil, fmt.Errorf("virsh domiflist failed: %w", err)
	}

	var interfaces []Interface
	scanner := bufio.NewScanner(bytes.NewReader(out))

	// Skip header lines
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum <= 2 {
			continue
		}

		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		// Format: Interface  Type  Source  Model  MAC
		iface := Interface{
			Target: fields[0],
			Bridge: fields[2],
			Model:  fields[3],
			MAC:    fields[4],
		}
		interfaces = append(interfaces, iface)
	}

	return interfaces, nil
}

// IsRunning reports whether the domain is in the running state.
func (c *Client) IsRunning(domain string) bool {
	state, err := c.getDomainState(domain)
	return err == nil && strings.TrimSpace(state) == "running"
}

// macsOnBridge returns the set of MAC addresses attached to the given bridge.
func (c *Client) macsOnBridge(domain, bridge string) map[string]bool {
	set := make(map[string]bool)
	ifaces, err := c.GetDomainInterfaces(domain)
	if err != nil {
		return set
	}
	for _, i := range ifaces {
		if i.Bridge == bridge {
			set[i.MAC] = true
		}
	}
	return set
}

// AttachInterface adds a new network interface to a domain. The interface is
// always persisted (--config) and applied live (--live) only when the VM is
// running, so attaching to a stopped VM does not fail. model defaults to virtio.
func (c *Client) AttachInterface(domain, bridge, model, mac string) (string, error) {
	if model == "" {
		model = "virtio"
	}

	// Snapshot existing MACs on the bridge so we can identify the new one.
	var before map[string]bool
	if mac == "" {
		before = c.macsOnBridge(domain, bridge)
	}

	args := []string{
		"attach-interface", domain,
		"--type", "bridge",
		"--source", bridge,
		"--model", model,
		"--config",
	}
	if c.IsRunning(domain) {
		args = append(args, "--live")
	}
	if mac != "" {
		args = append(args, "--mac", mac)
	}

	out, err := exec.Command("virsh", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("virsh attach-interface failed: %s - %w", string(out), err)
	}

	// If no MAC was provided, identify the newly added interface by diffing.
	if mac == "" {
		after := c.macsOnBridge(domain, bridge)
		for m := range after {
			if !before[m] {
				return m, nil
			}
		}
		return "", nil // could not determine the assigned MAC
	}

	return mac, nil
}

// DetachInterface removes a network interface from a domain by MAC address.
func (c *Client) DetachInterface(domain, mac string) error {
	// First, find the interface type for this MAC
	interfaces, err := c.GetDomainInterfaces(domain)
	if err != nil {
		return err
	}

	var found bool
	for _, iface := range interfaces {
		if iface.MAC == mac {
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("interface with MAC %s not found on domain %s", mac, domain)
	}

	args := []string{
		"detach-interface", domain,
		"--type", "bridge",
		"--mac", mac,
		"--config",
	}
	if c.IsRunning(domain) {
		args = append(args, "--live")
	}

	out, err := exec.Command("virsh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("virsh detach-interface failed: %s - %w", string(out), err)
	}

	return nil
}

// DomainExists checks if a domain exists.
func (c *Client) DomainExists(name string) bool {
	err := exec.Command("virsh", "dominfo", name).Run()
	return err == nil
}
