package netlink

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// BridgeManager manages Linux bridge interfaces.
type BridgeManager struct{}

// NewBridgeManager creates a new bridge manager.
func NewBridgeManager() *BridgeManager {
	return &BridgeManager{}
}

// BridgeConfig defines the configuration for a Linux bridge.
type BridgeConfig struct {
	Name string // Bridge name (e.g., "br-nnet-100")
	STP  bool   // Enable Spanning Tree Protocol
	MTU  int    // MTU for the bridge
}

// Create creates a new Linux bridge.
func (m *BridgeManager) Create(cfg BridgeConfig) error {
	// Check if bridge already exists
	existing, err := netlink.LinkByName(cfg.Name)
	if err == nil {
		// Bridge exists, check if it's actually a bridge
		if _, ok := existing.(*netlink.Bridge); ok {
			// Already exists as a bridge, just ensure it's up
			return netlink.LinkSetUp(existing)
		}
		// Wrong type, delete and recreate
		if err := netlink.LinkDel(existing); err != nil {
			return fmt.Errorf("failed to delete existing interface %s: %w", cfg.Name, err)
		}
	}

	// Set defaults
	if cfg.MTU == 0 {
		cfg.MTU = 1500
	}

	// Create bridge
	bridge := &netlink.Bridge{
		LinkAttrs: netlink.LinkAttrs{
			Name: cfg.Name,
			MTU:  cfg.MTU,
		},
	}

	if err := netlink.LinkAdd(bridge); err != nil {
		return fmt.Errorf("failed to create bridge %s: %w", cfg.Name, err)
	}

	// Get the created bridge
	link, err := netlink.LinkByName(cfg.Name)
	if err != nil {
		return fmt.Errorf("failed to get created bridge %s: %w", cfg.Name, err)
	}

	// Configure STP
	// Note: STP is controlled via sysfs, not netlink directly
	// For now, we skip STP configuration (can be added later via sysfs)

	// Bring bridge up
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to bring up bridge %s: %w", cfg.Name, err)
	}

	return nil
}

// Delete removes a Linux bridge.
func (m *BridgeManager) Delete(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		// Bridge doesn't exist, nothing to do
		return nil
	}

	// Bring down first
	if err := netlink.LinkSetDown(link); err != nil {
		return fmt.Errorf("failed to bring down bridge %s: %w", name, err)
	}

	if err := netlink.LinkDel(link); err != nil {
		return fmt.Errorf("failed to delete bridge %s: %w", name, err)
	}

	return nil
}

// Exists checks if a bridge exists.
func (m *BridgeManager) Exists(name string) bool {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return false
	}
	_, ok := link.(*netlink.Bridge)
	return ok
}

// Get returns information about a bridge.
func (m *BridgeManager) Get(name string) (*BridgeInfo, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return nil, fmt.Errorf("bridge %s not found: %w", name, err)
	}

	bridge, ok := link.(*netlink.Bridge)
	if !ok {
		return nil, fmt.Errorf("%s is not a bridge", name)
	}

	// Get attached interfaces
	links, err := netlink.LinkList()
	if err != nil {
		return nil, fmt.Errorf("failed to list links: %w", err)
	}

	var attachedInterfaces []string
	for _, l := range links {
		if l.Attrs().MasterIndex == bridge.Attrs().Index {
			attachedInterfaces = append(attachedInterfaces, l.Attrs().Name)
		}
	}

	return &BridgeInfo{
		Name:               bridge.Attrs().Name,
		MTU:                bridge.Attrs().MTU,
		Up:                 bridge.Attrs().Flags&net.FlagUp != 0,
		AttachedInterfaces: attachedInterfaces,
	}, nil
}

// BridgeInfo contains information about a bridge.
type BridgeInfo struct {
	Name               string
	MTU                int
	Up                 bool
	AttachedInterfaces []string
}

// AddInterface adds an interface to the bridge.
func (m *BridgeManager) AddInterface(bridgeName, ifaceName string) error {
	bridgeLink, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("bridge %s not found: %w", bridgeName, err)
	}

	bridge, ok := bridgeLink.(*netlink.Bridge)
	if !ok {
		return fmt.Errorf("%s is not a bridge", bridgeName)
	}

	ifaceLink, err := netlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifaceName, err)
	}

	if err := netlink.LinkSetMaster(ifaceLink, bridge); err != nil {
		return fmt.Errorf("failed to add %s to bridge %s: %w", ifaceName, bridgeName, err)
	}

	return nil
}

// RemoveInterface removes an interface from its master bridge.
func (m *BridgeManager) RemoveInterface(ifaceName string) error {
	ifaceLink, err := netlink.LinkByName(ifaceName)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifaceName, err)
	}

	if err := netlink.LinkSetNoMaster(ifaceLink); err != nil {
		return fmt.Errorf("failed to remove %s from bridge: %w", ifaceName, err)
	}

	return nil
}
