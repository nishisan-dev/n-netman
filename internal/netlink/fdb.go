package netlink

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// FDBManager manages Forwarding Database entries for VXLAN interfaces.
type FDBManager struct{}

// NewFDBManager creates a new FDB manager.
func NewFDBManager() *FDBManager {
	return &FDBManager{}
}

// FDBEntry represents a forwarding database entry.
type FDBEntry struct {
	MAC        net.HardwareAddr // MAC address (use all zeros for BUM traffic)
	RemoteIP   net.IP           // Remote VTEP IP
	VXLANName  string           // VXLAN interface name
	Permanent  bool             // Permanent entry (won't age out)
}

// Add adds an FDB entry to a VXLAN interface.
// For VXLAN with static peers, this is used to add remote VTEP endpoints.
// Use MAC 00:00:00:00:00:00 for flooding unknown unicast/multicast to all peers.
func (m *FDBManager) Add(entry FDBEntry) error {
	link, err := netlink.LinkByName(entry.VXLANName)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", entry.VXLANName, err)
	}

	// Build neigh entry
	neigh := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		Family:       netlink.NDA_VNI, // For VXLAN FDB
		HardwareAddr: entry.MAC,
		IP:           entry.RemoteIP,
	}

	// Set flags
	if entry.Permanent {
		neigh.State = netlink.NUD_PERMANENT
	} else {
		neigh.State = netlink.NUD_REACHABLE
	}

	// Use NeighAppend to add FDB entry (avoids conflicts with existing entries)
	if err := netlink.NeighAppend(neigh); err != nil {
		return fmt.Errorf("failed to add FDB entry: %w", err)
	}

	return nil
}

// Delete removes an FDB entry from a VXLAN interface.
func (m *FDBManager) Delete(entry FDBEntry) error {
	link, err := netlink.LinkByName(entry.VXLANName)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", entry.VXLANName, err)
	}

	neigh := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		Family:       netlink.NDA_VNI,
		HardwareAddr: entry.MAC,
		IP:           entry.RemoteIP,
	}

	if err := netlink.NeighDel(neigh); err != nil {
		// Ignore "no such file or directory" errors (entry doesn't exist)
		return fmt.Errorf("failed to delete FDB entry: %w", err)
	}

	return nil
}

// List returns all FDB entries for a VXLAN interface.
func (m *FDBManager) List(vxlanName string) ([]FDBEntry, error) {
	link, err := netlink.LinkByName(vxlanName)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", vxlanName, err)
	}

	// Get all neighbors (FDB entries) for this link
	neighs, err := netlink.NeighList(link.Attrs().Index, netlink.FAMILY_ALL)
	if err != nil {
		return nil, fmt.Errorf("failed to list FDB entries: %w", err)
	}

	var entries []FDBEntry
	for _, n := range neighs {
		// Filter to only FDB entries (those with IP addresses - VTEP destinations)
		if n.IP != nil {
			entries = append(entries, FDBEntry{
				MAC:        n.HardwareAddr,
				RemoteIP:   n.IP,
				VXLANName:  vxlanName,
				Permanent:  n.State == netlink.NUD_PERMANENT,
			})
		}
	}

	return entries, nil
}

// AddPeer adds a remote VXLAN peer (VTEP) to the FDB.
// This is a convenience method that adds an FDB entry with MAC 00:00:00:00:00:00
// to enable flooding to this peer for unknown destinations.
func (m *FDBManager) AddPeer(vxlanName string, remoteIP net.IP) error {
	return m.Add(FDBEntry{
		MAC:        net.HardwareAddr{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		RemoteIP:   remoteIP,
		VXLANName:  vxlanName,
		Permanent:  true,
	})
}

// DeletePeer removes a remote VXLAN peer (VTEP) from the FDB.
func (m *FDBManager) DeletePeer(vxlanName string, remoteIP net.IP) error {
	return m.Delete(FDBEntry{
		MAC:        net.HardwareAddr{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		RemoteIP:   remoteIP,
		VXLANName:  vxlanName,
	})
}

// SyncPeers synchronizes the FDB with a list of peer IPs.
// Adds missing peers and removes peers that are no longer in the list.
func (m *FDBManager) SyncPeers(vxlanName string, desiredPeers []net.IP) error {
	// Get current FDB entries
	current, err := m.List(vxlanName)
	if err != nil {
		return err
	}

	// Build set of current peer IPs
	currentPeers := make(map[string]bool)
	for _, entry := range current {
		currentPeers[entry.RemoteIP.String()] = true
	}

	// Build set of desired peer IPs
	desiredSet := make(map[string]bool)
	for _, ip := range desiredPeers {
		desiredSet[ip.String()] = true
	}

	// Add missing peers
	for _, ip := range desiredPeers {
		if !currentPeers[ip.String()] {
			if err := m.AddPeer(vxlanName, ip); err != nil {
				return fmt.Errorf("failed to add peer %s: %w", ip, err)
			}
		}
	}

	// Remove stale peers
	for _, entry := range current {
		if !desiredSet[entry.RemoteIP.String()] {
			if err := m.DeletePeer(vxlanName, entry.RemoteIP); err != nil {
				// Log but continue
				fmt.Printf("warning: failed to remove stale peer %s: %v\n", entry.RemoteIP, err)
			}
		}
	}

	return nil
}
