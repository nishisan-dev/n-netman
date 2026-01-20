// Package netlink provides abstractions over the Linux netlink API for managing
// network interfaces, bridges, VXLANs, routes, and FDB entries.
package netlink

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// VXLANManager manages VXLAN interfaces.
type VXLANManager struct{}

// NewVXLANManager creates a new VXLAN manager.
func NewVXLANManager() *VXLANManager {
	return &VXLANManager{}
}

// VXLANConfig defines the configuration for a VXLAN interface.
type VXLANConfig struct {
	Name       string   // Interface name (e.g., "vxlan100")
	VNI        int      // VXLAN Network Identifier
	DstPort    int      // Destination UDP port (default 4789)
	LocalIP    net.IP   // Local underlay IP for VXLAN tunnel
	MTU        int      // MTU for the interface
	Learning   bool     // Enable MAC learning
	Bridge     string   // Bridge to attach to (optional)
}

// Create creates a new VXLAN interface.
func (m *VXLANManager) Create(cfg VXLANConfig) error {
	// Check if interface already exists
	existing, err := netlink.LinkByName(cfg.Name)
	if err == nil {
		// Interface exists, check if it's a VXLAN with matching config
		if vxlan, ok := existing.(*netlink.Vxlan); ok {
			if vxlan.VxlanId == cfg.VNI {
				// Already exists with same VNI, just ensure it's up
				return netlink.LinkSetUp(existing)
			}
		}
		// Wrong type or wrong VNI, delete and recreate
		if err := netlink.LinkDel(existing); err != nil {
			return fmt.Errorf("failed to delete existing interface %s: %w", cfg.Name, err)
		}
	}

	// Set defaults
	if cfg.DstPort == 0 {
		cfg.DstPort = 4789
	}
	if cfg.MTU == 0 {
		cfg.MTU = 1450
	}

	// Create VXLAN interface
	vxlan := &netlink.Vxlan{
		LinkAttrs: netlink.LinkAttrs{
			Name: cfg.Name,
			MTU:  cfg.MTU,
		},
		VxlanId:  cfg.VNI,
		Port:     cfg.DstPort,
		Learning: cfg.Learning,
	}

	// Set source IP if provided
	if cfg.LocalIP != nil {
		vxlan.SrcAddr = cfg.LocalIP
	}

	if err := netlink.LinkAdd(vxlan); err != nil {
		return fmt.Errorf("failed to create vxlan %s: %w", cfg.Name, err)
	}

	// Bring interface up
	link, err := netlink.LinkByName(cfg.Name)
	if err != nil {
		return fmt.Errorf("failed to get created vxlan %s: %w", cfg.Name, err)
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to bring up vxlan %s: %w", cfg.Name, err)
	}

	// Attach to bridge if specified
	if cfg.Bridge != "" {
		if err := m.AttachToBridge(cfg.Name, cfg.Bridge); err != nil {
			return err
		}
	}

	return nil
}

// Delete removes a VXLAN interface.
func (m *VXLANManager) Delete(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		// Interface doesn't exist, nothing to do
		return nil
	}

	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("failed to delete vxlan %s: %w", name, err)
	}

	return nil
}

// Exists checks if a VXLAN interface exists.
func (m *VXLANManager) Exists(name string) bool {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return false
	}
	_, ok := link.(*netlink.Vxlan)
	return ok
}

// Get returns information about a VXLAN interface.
func (m *VXLANManager) Get(name string) (*VXLANInfo, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("vxlan %s not found: %w", name, err)
	}

	vxlan, ok := link.(*netlink.Vxlan)
	if !ok {
		return nil, fmt.Errorf("%s is not a vxlan interface", name)
	}

	return &VXLANInfo{
		Name:     vxlan.Attrs().Name,
		VNI:      vxlan.VxlanId,
		DstPort:  vxlan.Port,
		LocalIP:  vxlan.SrcAddr,
		MTU:      vxlan.Attrs().MTU,
		Learning: vxlan.Learning,
		Up:       vxlan.Attrs().Flags&net.FlagUp != 0,
	}, nil
}

// VXLANInfo contains information about a VXLAN interface.
type VXLANInfo struct {
	Name     string
	VNI      int
	DstPort  int
	LocalIP  net.IP
	MTU      int
	Learning bool
	Up       bool
}

// AttachToBridge attaches a VXLAN interface to a bridge.
func (m *VXLANManager) AttachToBridge(vxlanName, bridgeName string) error {
	vxlanLink, err := netlink.LinkByName(vxlanName)
	if err != nil {
		return fmt.Errorf("vxlan %s not found: %w", vxlanName, err)
	}

	bridgeLink, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("bridge %s not found: %w", bridgeName, err)
	}

	bridge, ok := bridgeLink.(*netlink.Bridge)
	if !ok {
		return fmt.Errorf("%s is not a bridge", bridgeName)
	}

	if err := netlink.LinkSetMaster(vxlanLink, bridge); err != nil {
		return fmt.Errorf("failed to attach %s to bridge %s: %w", vxlanName, bridgeName, err)
	}

	return nil
}

// DetachFromBridge detaches a VXLAN interface from its master bridge.
func (m *VXLANManager) DetachFromBridge(vxlanName string) error {
	vxlanLink, err := netlink.LinkByName(vxlanName)
	if err != nil {
		return fmt.Errorf("vxlan %s not found: %w", vxlanName, err)
	}

	if err := netlink.LinkSetNoMaster(vxlanLink); err != nil {
		return fmt.Errorf("failed to detach %s from bridge: %w", vxlanName, err)
	}

	return nil
}
