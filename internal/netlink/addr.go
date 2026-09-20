package netlink

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// AddrManager manages IP addresses on any link.
//
// Bridges are configured through BridgeManager, which delegates here; the agent
// running inside a VM uses this directly against an ordinary NIC.
type AddrManager struct{}

// NewAddrManager creates a new address manager.
func NewAddrManager() *AddrManager {
	return &AddrManager{}
}

// Ensure adds an address to a link if it is not already present.
//
// The address must be in CIDR form, e.g. "10.100.0.50/24". The call is
// idempotent, so it is safe to run on every reconciliation pass.
func (m *AddrManager) Ensure(ifname, cidr string) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}

	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("invalid address %s: %w", cidr, err)
	}

	existing, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("failed to list addresses on %s: %w", ifname, err)
	}
	for _, a := range existing {
		if a.IPNet.String() == addr.IPNet.String() {
			return nil
		}
	}

	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("failed to add address %s to %s: %w", cidr, ifname, err)
	}

	return nil
}

// Remove deletes an address from a link. A missing address is not an error, so
// teardown is idempotent.
func (m *AddrManager) Remove(ifname, cidr string) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}

	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return fmt.Errorf("invalid address %s: %w", cidr, err)
	}

	existing, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("failed to list addresses on %s: %w", ifname, err)
	}
	found := false
	for _, a := range existing {
		if a.IPNet.String() == addr.IPNet.String() {
			found = true
			break
		}
	}
	if !found {
		return nil
	}

	if err := netlink.AddrDel(link, addr); err != nil {
		return fmt.Errorf("failed to remove address %s from %s: %w", cidr, ifname, err)
	}

	return nil
}

// SetUp brings a link administratively up if it is not already.
func (m *AddrManager) SetUp(ifname string) error {
	link, err := netlink.LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", ifname, err)
	}
	if link.Attrs().Flags&net.FlagUp != 0 {
		return nil
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to bring %s up: %w", ifname, err)
	}
	return nil
}
