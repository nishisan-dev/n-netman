// Package routing implements route export and import policies.
package routing

import (
	"net"
	"strings"
	"sync"

	"github.com/nishisan-dev/n-netman/internal/config"
	"github.com/nishisan-dev/n-netman/internal/controlplane"
	"github.com/nishisan-dev/n-netman/internal/routepolicy"
)

// Manager handles route export and import according to configured policies.
type Manager struct {
	cfg *config.Config

	mu             sync.RWMutex
	exportedRoutes []controlplane.Route
}

// NewManager creates a new routing manager.
func NewManager(cfg *config.Config) *Manager {
	return &Manager{
		cfg: cfg,
	}
}

// GetExportRoutes returns the routes that should be exported according to policy.
// Currently, it only includes explicit config networks.
// DEPRECATED: Use GetExportRoutesForOverlay for multi-overlay support.
func (m *Manager) GetExportRoutes() []controlplane.Route {
	m.mu.RLock()
	if len(m.exportedRoutes) > 0 {
		routes := make([]controlplane.Route, len(m.exportedRoutes))
		copy(routes, m.exportedRoutes)
		m.mu.RUnlock()
		return routes
	}
	m.mu.RUnlock()

	// Build export routes from config (cached for future calls).
	var routes []controlplane.Route

	exportCfg := m.cfg.Routing.Export
	metric := uint32(exportCfg.Metric)
	if metric == 0 {
		metric = 100
	}

	// Add explicitly configured networks
	for _, network := range exportCfg.Networks {
		routes = append(routes, controlplane.Route{
			Prefix: network,
			Metric: metric,
			// NextHop will be set by the control plane based on local overlay IP
		})
	}

	// TODO: If include_connected, scan local interfaces and add connected routes
	// TODO: If include_netplan_static, parse netplan and add static routes

	m.mu.Lock()
	m.exportedRoutes = routes
	m.mu.Unlock()

	return routes
}

// GetExportRoutesForOverlay returns the routes that should be exported for a specific overlay.
// Each overlay has its own export policy defined in overlay.Routing.Export.
func (m *Manager) GetExportRoutesForOverlay(overlay config.OverlayDef) []controlplane.Route {
	var routes []controlplane.Route

	exportCfg := overlay.Routing.Export
	metric := uint32(exportCfg.Metric)
	if metric == 0 {
		metric = 100
	}

	// Add explicitly configured networks for this overlay
	for _, network := range exportCfg.Networks {
		routes = append(routes, controlplane.Route{
			Prefix: network,
			Metric: metric,
			VNI:    uint32(overlay.VNI),
			// NextHop will be set based on overlay bridge IP.
		})
	}

	return routes
}

// ShouldImport checks if a route should be imported according to policy.
// DEPRECATED: Use ShouldImportForOverlay for multi-overlay support.
func (m *Manager) ShouldImport(route controlplane.Route) bool {
	importCfg := m.cfg.Routing.Import

	// Parse the route's prefix
	_, routeNet, err := net.ParseCIDR(route.Prefix)
	if err != nil {
		return false
	}

	return evalImportPolicy(routeNet, importCfg)
}

// ShouldImportForOverlay checks if a route should be imported for a specific overlay.
// Each overlay has its own import policy defined in overlay.Routing.Import.
func (m *Manager) ShouldImportForOverlay(route controlplane.Route, overlay config.OverlayDef) bool {
	// Parse the route's prefix
	_, routeNet, err := net.ParseCIDR(route.Prefix)
	if err != nil {
		return false
	}

	return evalImportPolicy(routeNet, overlay.Routing.Import)
}

// evalImportPolicy applies an import policy to a parsed route network.
//
// The rules themselves live in internal/routepolicy so that nnet-agent can
// apply the identical admission logic without importing the control plane.
func evalImportPolicy(routeNet *net.IPNet, importCfg config.ImportConfig) bool {
	return PolicyFromImport(importCfg).AdmitsNet(routeNet)
}

// PolicyFromImport adapts an import configuration into a route policy.
func PolicyFromImport(importCfg config.ImportConfig) routepolicy.Policy {
	return routepolicy.Policy{
		AcceptAll: importCfg.AcceptAll,
		Allow:     importCfg.Allow,
		Deny:      importCfg.Deny,
	}
}

// GetImportTableForOverlay returns the routing table number for installing routes from an overlay.
func (m *Manager) GetImportTableForOverlay(overlay config.OverlayDef) int {
	table := overlay.Routing.Import.Install.Table
	if table == 0 {
		return 100 // default
	}
	return table
}

// FilterImportRoutes filters a list of routes according to import policy.
func (m *Manager) FilterImportRoutes(routes []controlplane.Route) []controlplane.Route {
	var filtered []controlplane.Route
	for _, r := range routes {
		if m.ShouldImport(r) {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// RefreshExportRoutes refreshes the cached export routes.
func (m *Manager) RefreshExportRoutes() {
	m.mu.Lock()
	m.exportedRoutes = nil
	m.mu.Unlock()
	m.GetExportRoutes()
}

// RouteToNetlink converts a control plane route to parameters for netlink installation.
func RouteToNetlink(r controlplane.Route, table int) (prefix *net.IPNet, gateway net.IP, err error) {
	_, prefix, err = net.ParseCIDR(r.Prefix)
	if err != nil {
		return nil, nil, err
	}

	if r.NextHop != "" {
		gateway = net.ParseIP(r.NextHop)
	}

	return prefix, gateway, nil
}

// IsIPv6Route checks if a route is for IPv6.
func IsIPv6Route(prefix string) bool {
	return strings.Contains(prefix, ":")
}
