package netlink

import (
	"errors"
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
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

// Protocol constants for route identification.
//
// Every route the project installs carries one of these, so deletions can be
// scoped to routes we own and never touch a route someone else installed.
const (
	// RouteProtocolNNetMan marks routes the controller installs on a host from
	// prefixes learned over the gRPC control plane.
	RouteProtocolNNetMan = 99
	// RouteProtocolNNetAgent marks routes nnet-agent installs inside a VM from
	// advertisements received on the inject channel. It is distinct from
	// RouteProtocolNNetMan so the two remain separable on a host that runs both.
	RouteProtocolNNetAgent = 98
)

// RouteTableMain is the kernel's main routing table.
//
// Pass this rather than 0 when the main table is what you mean: a table of 0 is
// RT_TABLE_UNSPEC, which the kernel resolves to main on a write but which makes
// List return routes from every table on a read. Being explicit keeps the two
// directions symmetric.
const RouteTableMain = unix.RT_TABLE_MAIN

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
// When cfg.Protocol is set, the deletion is scoped to routes installed by that
// protocol, so we never remove a route the daemon did not install.
func (m *RouteManager) Delete(cfg RouteConfig) error {
	route := &netlink.Route{
		Dst: cfg.Destination,
		Gw:  cfg.Gateway,
	}

	if cfg.Table > 0 {
		route.Table = cfg.Table
	}

	if cfg.Protocol != 0 {
		route.Protocol = netlink.RouteProtocol(cfg.Protocol)
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
//
// A table of 0 (RT_TABLE_UNSPEC) disables the filter and returns routes from
// every table, so callers that mean the main table should pass RouteTableMain.
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

	// Keep deleting after a failure so one stuck route cannot strand the rest,
	// but report what failed instead of printing to stdout: this package is a
	// library used by daemons that log structurally.
	var errs []error
	for _, r := range routes {
		cfg := RouteConfig{
			Destination: r.Destination,
			Gateway:     r.Gateway,
			Table:       table,
			Protocol:    protocol,
		}
		if err := m.Delete(cfg); err != nil {
			errs = append(errs, fmt.Errorf("route %s: %w", r.Destination, err))
		}
	}

	return errors.Join(errs...)
}

// Sync reconciles the routes owned by a protocol in a table against a desired
// set: missing routes are added, changed ones replaced, and routes this
// protocol installed but no longer wants are removed.
//
// The kernel keeps one route per destination in a table, so the desired set
// must already hold at most one entry per destination; the caller is what
// decides which of several candidates wins.
//
// Removal is scoped to the given protocol, so a route installed by anything
// else is never touched.
func (m *RouteManager) Sync(table, protocol int, desired []RouteConfig) error {
	current, err := m.ListByProtocol(table, protocol)
	if err != nil {
		return err
	}

	currentSet := make(map[string]RouteInfo, len(current))
	for _, r := range current {
		if r.Destination != nil {
			currentSet[r.Destination.String()] = r
		}
	}

	desiredSet := make(map[string]RouteConfig, len(desired))
	for _, r := range desired {
		if r.Destination != nil {
			desiredSet[r.Destination.String()] = r
		}
	}

	var errs []error

	for _, r := range desired {
		if r.Destination == nil {
			continue
		}
		r.Table = table
		r.Protocol = protocol

		existing, ok := currentSet[r.Destination.String()]
		if !ok {
			if err := m.Add(r); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if !existing.Gateway.Equal(r.Gateway) || existing.Metric != r.Metric {
			if err := m.Replace(r); err != nil {
				errs = append(errs, err)
			}
		}
	}

	for _, r := range current {
		if r.Destination == nil {
			continue
		}
		if _, ok := desiredSet[r.Destination.String()]; ok {
			continue
		}
		if err := m.Delete(RouteConfig{
			Destination: r.Destination,
			Gateway:     r.Gateway,
			Table:       table,
			Protocol:    protocol,
		}); err != nil {
			errs = append(errs, fmt.Errorf("stale route %s: %w", r.Destination, err))
		}
	}

	return errors.Join(errs...)
}
