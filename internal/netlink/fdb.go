package netlink

import (
	"fmt"
	"net"
	"os/exec"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// FDBManager manages Forwarding Database entries for VXLAN interfaces.
type FDBManager struct{}

// NewFDBManager creates a new FDB manager.
func NewFDBManager() *FDBManager {
	return &FDBManager{}
}

// FDBEntry represents a forwarding database entry.
type FDBEntry struct {
	MAC       net.HardwareAddr // MAC address (use all zeros for BUM traffic)
	RemoteIP  net.IP           // Remote VTEP IP
	VXLANName string           // VXLAN interface name
	Permanent bool             // Permanent entry (won't age out)
}

// Add adds an FDB entry to a VXLAN interface.
// For VXLAN with static peers, this is used to add remote VTEP endpoints.
// Use MAC 00:00:00:00:00:00 for flooding unknown unicast/multicast to all peers.
func (m *FDBManager) Add(entry FDBEntry) error {
	link, err := netlink.LinkByName(entry.VXLANName)
	if err != nil {
		return fmt.Errorf("interface %s not found: %w", entry.VXLANName, err)
	}

	// Verify it's a VXLAN interface
	if _, ok := link.(*netlink.Vxlan); !ok {
		return fmt.Errorf("interface %s is not a VXLAN", entry.VXLANName)
	}

	// Determine flags based on whether VXLAN is attached to a bridge
	// If attached to bridge (MasterIndex > 0), we need NTF_SELF for VXLAN FDB
	// NTF_SELF means the entry is for the VXLAN device itself (not the bridge)
	flags := unix.NTF_SELF

	// Build neigh entry for VXLAN FDB
	neigh := &netlink.Neigh{
		LinkIndex:    link.Attrs().Index,
		Family:       unix.AF_BRIDGE, // FDB entries use AF_BRIDGE
		Flags:        flags,
		HardwareAddr: entry.MAC,
		IP:           entry.RemoteIP,
	}

	// Set state
	if entry.Permanent {
		neigh.State = netlink.NUD_PERMANENT
	} else {
		neigh.State = netlink.NUD_REACHABLE
	}

	// Use NeighAppend to add FDB entry - this allows multiple entries
	// with the same MAC (00:00:00:00:00:00) pointing to different destinations
	// This is required for head-end replication where BUM traffic goes to all peers
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
		Family:       unix.AF_BRIDGE,
		Flags:        unix.NTF_SELF,
		HardwareAddr: entry.MAC,
		IP:           entry.RemoteIP,
	}

	if err := netlink.NeighDel(neigh); err != nil {
		// Ignore errors (entry might not exist)
		return nil
	}

	return nil
}

// List returns all FDB entries for a VXLAN interface.
func (m *FDBManager) List(vxlanName string) ([]FDBEntry, error) {
	link, err := netlink.LinkByName(vxlanName)
	if err != nil {
		return nil, fmt.Errorf("interface %s not found: %w", vxlanName, err)
	}

	// Get all bridge FDB neighbors for this link
	neighs, err := netlink.NeighList(link.Attrs().Index, unix.AF_BRIDGE)
	if err != nil {
		return nil, fmt.Errorf("failed to list FDB entries: %w", err)
	}

	var entries []FDBEntry
	for _, n := range neighs {
		// Filter to only FDB entries with IP addresses (VTEP destinations)
		if len(n.IP) > 0 {
			entries = append(entries, FDBEntry{
				MAC:       n.HardwareAddr,
				RemoteIP:  n.IP,
				VXLANName: vxlanName,
				Permanent: n.State == netlink.NUD_PERMANENT,
			})
		}
	}

	return entries, nil
}

// AddPeer adds a remote VXLAN peer (VTEP) to the FDB.
// This is a convenience method that adds an FDB entry with MAC 00:00:00:00:00:00
// to enable flooding to this peer for unknown destinations.
// Uses 'bridge fdb append' command directly as vishvananda/netlink NeighAppend
// doesn't work correctly for VXLAN FDB entries.
func (m *FDBManager) AddPeer(vxlanName string, remoteIP net.IP) error {
	// Use bridge command directly - more reliable than netlink library for VXLAN FDB
	// bridge fdb append 00:00:00:00:00:00 dev <vxlan> dst <remote_ip>
	cmd := exec.Command("bridge", "fdb", "append",
		"00:00:00:00:00:00",
		"dev", vxlanName,
		"dst", remoteIP.String())

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's a duplicate entry error (not really an error)
		outputStr := string(output)
		if strings.Contains(outputStr, "File exists") {
			return nil // Entry already exists, that's fine
		}
		return fmt.Errorf("bridge fdb append failed: %s: %w", outputStr, err)
	}

	return nil
}

// DeletePeer removes a remote VXLAN peer (VTEP) from the FDB.
func (m *FDBManager) DeletePeer(vxlanName string, remoteIP net.IP) error {
	return m.Delete(FDBEntry{
		MAC:       net.HardwareAddr{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		RemoteIP:  remoteIP,
		VXLANName: vxlanName,
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
