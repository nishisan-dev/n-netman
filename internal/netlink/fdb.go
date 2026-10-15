package netlink

import (
	"errors"
	"fmt"
	"net"

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
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ESRCH) {
			return nil
		}
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

// AddPeer appends a permanent, device-self BUM entry using Netlink only.
// NLM_F_APPEND permits the same zero MAC to fan out to multiple VTEPs.
func (m *FDBManager) AddPeer(vxlanName string, remoteIP net.IP) error {
	e := m.Add(FDBEntry{MAC: net.HardwareAddr{0, 0, 0, 0, 0, 0}, RemoteIP: remoteIP, VXLANName: vxlanName, Permanent: true})
	if errors.Is(e, unix.EEXIST) {
		return nil
	}
	return e
}

// DeletePeer removes a remote VXLAN peer (VTEP) from the FDB.
func (m *FDBManager) DeletePeer(vxlanName string, remoteIP net.IP) error {
	return m.Delete(FDBEntry{
		MAC:       net.HardwareAddr{0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
		RemoteIP:  remoteIP,
		VXLANName: vxlanName,
	})
}

// isZeroMAC reports whether a hardware address is the all-zeros BUM MAC
// (00:00:00:00:00:00). Some kernels report it as an empty slice.
func isZeroMAC(mac net.HardwareAddr) bool {
	if len(mac) == 0 {
		return true
	}
	for _, b := range mac {
		if b != 0 {
			return false
		}
	}
	return true
}

// SyncPeers synchronizes the BUM (head-end replication) FDB entries with a list
// of peer IPs. It only considers and removes the all-zeros BUM entries: learned
// unicast entries (real MACs) are left untouched so they are neither mistaken
// for managed peers nor wrongly deleted.
func (m *FDBManager) SyncPeers(vxlanName string, desiredPeers []net.IP) error {
	// Get current FDB entries
	current, err := m.List(vxlanName)
	if err != nil {
		return err
	}

	// Build set of current BUM peer IPs (all-zeros MAC entries only).
	currentPeers := make(map[string]bool)
	for _, entry := range current {
		if isZeroMAC(entry.MAC) {
			currentPeers[entry.RemoteIP.String()] = true
		}
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

	// Remove stale BUM entries (only all-zeros entries we manage).
	for _, entry := range current {
		if !isZeroMAC(entry.MAC) {
			continue
		}
		if !desiredSet[entry.RemoteIP.String()] {
			if err := m.DeletePeer(vxlanName, entry.RemoteIP); err != nil {
				return fmt.Errorf("failed to remove stale peer %s: %w", entry.RemoteIP, err)
			}
		}
	}

	return nil
}
