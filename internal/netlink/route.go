package netlink

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// RouteManager manages Linux routing table entries.
type RouteManager struct{}

// NewRouteManager creates a new route manager.
func NewRouteManager() *RouteManager {
	return &RouteManager{}
}

// RouteConfig defines a route to be installed.
type RouteConfig struct {
	Destination *net.IPNet // Destination network (e.g., 172.16.10.0/24)
	Gateway     net.IP     // Next-hop gateway (can be nil for directly connected)
	Device      string     // Output interface (optional, used if no gateway)
	Table       int        // Routing table (0 = main, or custom table number)
	Metric      int        // Route metric/priority
	Protocol    int        // Protocol that added route (for identification)
}

// Protocol constants for route identification
const (
	RouteProtocolNNetMan = 99 // Custom protocol ID for n-netman routes
)

// Add adds a route to the routing table.
func (m *RouteManager) Add(cfg RouteConfig) error {
	route := &netlink.Route{
		Dst:      cfg.Destination,
		Gw:       cfg.Gateway,
		Protocol: netlink.RouteProtocol(cfg.Protocol),
	}

	// Set table if specified
	if cfg.Table > 0 {
		route.Table = cfg.Table
	}

	// Set metric if specified
	if cfg.Metric > 0 {
		route.Priority = cfg.Metric
	}

	// Set output device if specified
	if cfg.Device != "" {
		link, err := netlink.LinkByName(cfg.Device)
		if err != nil {
			return fmt.Errorf("device %s not found: %w", cfg.Device, err)
		}
		route.LinkIndex = link.Attrs().Index
	}

	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("failed to add route to %s: %w", cfg.Destination, err)
	}

	return nil
}

// Delete removes a route from the routing table.
func (m *RouteManager) Delete(cfg RouteConfig) error {
	route := &netlink.Route{
		Dst: cfg.Destination,
		Gw:  cfg.Gateway,
	}

	if cfg.Table > 0 {
		route.Table = cfg.Table
	}

	if err := netlink.RouteDel(route); err != nil {
		return fmt.Errorf("failed to delete route to %s: %w", cfg.Destination, err)
	}

	return nil
}

// Replace adds or replaces a route.
func (m *RouteManager) Replace(cfg RouteConfig) error {
	route := &netlink.Route{
		Dst:      cfg.Destination,
		Gw:       cfg.Gateway,
		Protocol: netlink.RouteProtocol(cfg.Protocol),
	}

	if cfg.Table > 0 {
		route.Table = cfg.Table
	}

	if cfg.Metric > 0 {
		route.Priority = cfg.Metric
	}

	if cfg.Device != "" {
		link, err := netlink.LinkByName(cfg.Device)
		if err != nil {
			return fmt.Errorf("device %s not found: %w", cfg.Device, err)
		}
		route.LinkIndex = link.Attrs().Index
	}

	if err := netlink.RouteReplace(route); err != nil {
		return fmt.Errorf("failed to replace route to %s: %w", cfg.Destination, err)
	}

	return nil
}

// List returns all routes in a routing table.
func (m *RouteManager) List(table int) ([]RouteInfo, error) {
	filter := &netlink.Route{}
	if table > 0 {
		filter.Table = table
	}

	routes, err := netlink.RouteListFiltered(netlink.FAMILY_ALL, filter, netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, fmt.Errorf("failed to list routes: %w", err)
	}

	var result []RouteInfo
	for _, r := range routes {
		info := RouteInfo{
			Destination: r.Dst,
			Gateway:     r.Gw,
			Table:       r.Table,
			Metric:      r.Priority,
			Protocol:    int(r.Protocol),
		}

		// Get device name if available
		if r.LinkIndex > 0 {
			link, err := netlink.LinkByIndex(r.LinkIndex)
			if err == nil {
				info.Device = link.Attrs().Name
			}
		}

		result = append(result, info)
	}

	return result, nil
}

// RouteInfo contains information about a route.
type RouteInfo struct {
	Destination *net.IPNet
	Gateway     net.IP
	Device      string
	Table       int
	Metric      int
	Protocol    int
}

// ListByProtocol returns routes installed by a specific protocol.
func (m *RouteManager) ListByProtocol(table, protocol int) ([]RouteInfo, error) {
	all, err := m.List(table)
	if err != nil {
		return nil, err
	}

	var result []RouteInfo
	for _, r := range all {
		if r.Protocol == protocol {
			result = append(result, r)
		}
	}

	return result, nil
}

// FlushByProtocol removes all routes installed by a specific protocol.
func (m *RouteManager) FlushByProtocol(table, protocol int) error {
	routes, err := m.ListByProtocol(table, protocol)
	if err != nil {
		return err
	}

	for _, r := range routes {
		cfg := RouteConfig{
			Destination: r.Destination,
			Gateway:     r.Gateway,
			Table:       table,
		}
		if err := m.Delete(cfg); err != nil {
			// Log but continue
			fmt.Printf("warning: failed to delete route %s: %v\n", r.Destination, err)
		}
	}

	return nil
}

// Sync synchronizes routes with a desired state.
// Adds missing routes, removes extra routes (installed by n-netman).
func (m *RouteManager) Sync(table int, desired []RouteConfig) error {
	// Get current n-netman routes
	current, err := m.ListByProtocol(table, RouteProtocolNNetMan)
	if err != nil {
		return err
	}

	// Build set of current routes by destination
	currentSet := make(map[string]RouteInfo)
	for _, r := range current {
		if r.Destination != nil {
			currentSet[r.Destination.String()] = r
		}
	}

	// Build set of desired routes
	desiredSet := make(map[string]RouteConfig)
	for _, r := range desired {
		if r.Destination != nil {
			desiredSet[r.Destination.String()] = r
		}
	}

	// Add/update missing routes
	for _, r := range desired {
		if r.Destination == nil {
			continue
		}
		key := r.Destination.String()
		r.Protocol = RouteProtocolNNetMan

		if existing, ok := currentSet[key]; ok {
			// Route exists, check if it needs updating
			if !existing.Gateway.Equal(r.Gateway) || existing.Metric != r.Metric {
				if err := m.Replace(r); err != nil {
					return err
				}
			}
		} else {
			// Route doesn't exist, add it
			if err := m.Add(r); err != nil {
				return err
			}
		}
	}

	// Remove stale routes
	for _, r := range current {
		if r.Destination == nil {
			continue
		}
		key := r.Destination.String()
		if _, ok := desiredSet[key]; !ok {
			cfg := RouteConfig{
				Destination: r.Destination,
				Gateway:     r.Gateway,
				Table:       table,
			}
			if err := m.Delete(cfg); err != nil {
				fmt.Printf("warning: failed to remove stale route %s: %v\n", r.Destination, err)
			}
		}
	}

	return nil
}
