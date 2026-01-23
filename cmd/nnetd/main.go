// n-netman daemon - Lightweight VXLAN overlay manager for Linux
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nishisan-dev/n-netman/internal/config"
	"github.com/nishisan-dev/n-netman/internal/controlplane"
	nlmgr "github.com/nishisan-dev/n-netman/internal/netlink"
	"github.com/nishisan-dev/n-netman/internal/observability"
	"github.com/nishisan-dev/n-netman/internal/reconciler"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	// Parse flags
	configPath := flag.String("config", "/etc/n-netman/n-netman.yaml", "Path to configuration file")
	showVersion := flag.Bool("version", false, "Show version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("n-netman %s (commit: %s, built: %s)\n", version, commit, buildDate)
		os.Exit(0)
	}

	// Setup structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("starting n-netman daemon",
		"version", version,
		"config", *configPath,
	)

	// Load configuration
	loader := config.NewLoader()
	cfg, err := loader.LoadFile(*configPath)
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	overlays := cfg.GetOverlays()
	peers := cfg.GetPeers()
	slog.Info("configuration loaded successfully",
		"node_id", cfg.Node.ID,
		"hostname", cfg.Node.Hostname,
		"config_version", cfg.Version,
		"overlays_count", len(overlays),
		"peers_count", len(peers),
	)

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		slog.Info("received shutdown signal", "signal", sig)
		cancel()
	}()

	// Initialize metrics
	metrics := observability.NewMetrics(prometheus.DefaultRegisterer)
	metrics.PeersConfigured.Set(float64(len(peers)))

	// Start observability server (metrics + health)
	obsServer := observability.NewServer(cfg, logger)
	if err := obsServer.Start(ctx); err != nil {
		slog.Error("failed to start observability server", "error", err)
		os.Exit(1)
	}
	defer obsServer.Stop(context.Background())

	// Initialize route manager for installing routes learned from peers.
	routeMgr := nlmgr.NewRouteManager()

	// Initialize route table for control plane
	routeTable := controlplane.NewRouteTable()

	// Create route installer callback (control plane -> kernel).
	routeInstaller := func(routes []controlplane.Route) {
		installReceivedRoutes(cfg, routeMgr, routes, logger)
	}

	// Start gRPC control plane server
	cpServer := controlplane.NewServer(cfg, routeTable, logger)
	cpServer.SetRoutesReceivedCallback(routeInstaller)
	if err := cpServer.Start(); err != nil {
		slog.Error("failed to start control plane server", "error", err)
		os.Exit(1)
	}
	defer cpServer.Stop()

	// Start control plane client (connect to peers and keep routes fresh).
	cpClient := controlplane.NewClient(cfg, routeTable, logger)
	// Set client as status provider for /status endpoint
	obsServer.SetStatusProvider(cpClient)
	go func() {
		// Wait a bit for local setup before connecting to peers
		time.Sleep(2 * time.Second)
		if err := cpClient.ConnectToPeers(ctx); err != nil {
			slog.Warn("failed to connect to some peers", "error", err)
		}

		// Perform initial state exchange
		localRoutes := getLocalExportableRoutes(cfg, routeTable)
		if err := cpClient.ExchangeStateWithPeers(ctx, localRoutes); err != nil {
			slog.Warn("failed to exchange state with peers", "error", err)
		}

		// Start periodic health checks and route refresh loop.
		go runRouteRefreshLoop(ctx, cpClient, cfg, routeTable, routeMgr, logger)
	}()
	defer cpClient.Disconnect()

	// Start reconciler
	rec := reconciler.New(cfg,
		reconciler.WithInterval(10*time.Second),
		reconciler.WithLogger(logger),
	)

	go func() {
		if err := rec.Run(ctx); err != nil && err != context.Canceled {
			slog.Error("reconciler error", "error", err)
		}
	}()

	// Mark as ready
	obsServer.SetReady(true)

	slog.Info("daemon initialized, waiting for events...",
		"grpc_port", cfg.Security.ControlPlane.Listen.Port,
		"metrics_port", cfg.Observability.Metrics.Listen.Port,
		"health_port", cfg.Observability.Healthcheck.Listen.Port,
	)

	// Wait for shutdown
	<-ctx.Done()

	slog.Info("shutting down n-netman daemon")

	// Cleanup: flush all routes installed by n-netman.
	table := cfg.Routing.Import.Install.Table
	if table == 0 {
		table = 100
	}
	slog.Info("flushing installed routes", "table", table, "protocol", nlmgr.RouteProtocolNNetMan)
	if err := routeMgr.FlushByProtocol(table, nlmgr.RouteProtocolNNetMan); err != nil {
		slog.Warn("failed to flush routes on shutdown", "error", err)
	} else {
		slog.Info("routes flushed successfully")
	}

	// Cleanup: delete VXLAN interface (optional, can be configured)
	// Note: This is commented out by default as the VXLAN might be shared
	// vxlanMgr := nlmgr.NewVXLANManager()
	// vxlanMgr.Delete(cfg.Overlay.VXLAN.Name)
}

// installReceivedRoutes installs routes received from peers into the kernel.
// Each route is installed in the table of its corresponding overlay (by VNI).
func installReceivedRoutes(cfg *config.Config, routeMgr *nlmgr.RouteManager, routes []controlplane.Route, logger *slog.Logger) {
	// Build a VNI -> table mapping from overlays
	vniToTable := make(map[uint32]int)
	for _, overlay := range cfg.GetOverlays() {
		table := overlay.Routing.Import.Install.Table
		if table == 0 {
			table = 100 // default
		}
		vniToTable[uint32(overlay.VNI)] = table
	}

	for _, r := range routes {
		// Parse the prefix
		_, ipnet, err := net.ParseCIDR(r.Prefix)
		if err != nil {
			logger.Warn("invalid prefix from peer",
				"prefix", r.Prefix,
				"peer", r.PeerID,
				"error", err,
			)
			continue
		}

		// Parse next-hop
		gw := net.ParseIP(r.NextHop)
		if gw == nil {
			logger.Warn("invalid next-hop from peer",
				"next_hop", r.NextHop,
				"peer", r.PeerID,
			)
			continue
		}

		// Get the routing table based on the route's VNI
		table, ok := vniToTable[r.VNI]
		if !ok {
			// Fallback: use legacy global table if VNI not found
			table = cfg.Routing.Import.Install.Table
			if table == 0 {
				table = 100
			}
			logger.Debug("route VNI not found in overlays, using default table",
				"vni", r.VNI,
				"table", table,
			)
		}

		// Install the route
		routeCfg := nlmgr.RouteConfig{
			Destination: ipnet,
			Gateway:     gw,
			Table:       table,
			Metric:      int(r.Metric),
			Protocol:    nlmgr.RouteProtocolNNetMan,
		}

		if err := routeMgr.Replace(routeCfg); err != nil {
			logger.Warn("failed to install route",
				"prefix", r.Prefix,
				"next_hop", r.NextHop,
				"table", table,
				"error", err,
			)
			continue
		}

		logger.Info("installed route from peer",
			"prefix", r.Prefix,
			"next_hop", r.NextHop,
			"peer", r.PeerID,
			"metric", r.Metric,
			"table", table,
			"vni", r.VNI,
		)
	}
}

// getLocalExportableRoutes returns routes that should be exported to peers.
// It derives the next-hop from the underlay or overlay bridge IP.
func getLocalExportableRoutes(cfg *config.Config, routeTable *controlplane.RouteTable) []controlplane.Route {
	routes := make([]controlplane.Route, 0)

	// Detect local IP by finding an interface that can reach our peers.
	localIP := detectLocalIP(cfg)
	if localIP == "" {
		return routes // Can't export routes without a valid next-hop
	}

	// Add routes from all overlays
	for _, overlay := range cfg.GetOverlays() {
		leaseSecs := uint32(overlay.Routing.Import.Install.RouteLeaseSeconds)
		if leaseSecs == 0 {
			leaseSecs = 30
		}
		metric := uint32(overlay.Routing.Export.Metric)
		if metric == 0 {
			metric = 100
		}

		// Determine next-hop: prefer bridge IP (overlay) over underlay IP.
		nextHop := localIP
		if overlay.Bridge.IPv4 != "" {
			// Extract IP from CIDR (e.g., "10.100.0.1/24" -> "10.100.0.1")
			nextHop = extractIPFromCIDR(overlay.Bridge.IPv4)
		}

		for _, prefix := range overlay.Routing.Export.Networks {
			routes = append(routes, controlplane.Route{
				Prefix:       prefix,
				NextHop:      nextHop,
				Metric:       metric,
				LeaseSeconds: leaseSecs,
				VNI:          uint32(overlay.VNI),
			})
		}
	}

	return routes
}

// detectLocalIP finds a local IP address to use for route announcements.
// It uses a heuristic: pick the first interface with a subnet that contains the first peer.
func detectLocalIP(cfg *config.Config) string {
	if len(cfg.Overlay.Peers) == 0 {
		return ""
	}

	// Get first peer's IP to determine our subnet
	peerIP := net.ParseIP(cfg.Overlay.Peers[0].Endpoint.Address)
	if peerIP == nil {
		return ""
	}

	// Get all network interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}

	for _, iface := range ifaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}

			// Check if peer IP is in same subnet
			if ipnet.Contains(peerIP) {
				return ipnet.IP.String()
			}
		}
	}

	return ""
}

// extractIPFromCIDR extracts the IP address from a CIDR string.
// e.g., "10.100.0.1/24" -> "10.100.0.1"
func extractIPFromCIDR(cidr string) string {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		// Try parsing as plain IP
		if parsedIP := net.ParseIP(cidr); parsedIP != nil {
			return parsedIP.String()
		}
		return cidr // Return as-is if parsing fails
	}
	return ip.String()
}

// runRouteRefreshLoop periodically refreshes routes with peers.
func runRouteRefreshLoop(ctx context.Context, client *controlplane.Client, cfg *config.Config, routeTable *controlplane.RouteTable, routeMgr *nlmgr.RouteManager, logger *slog.Logger) {
	// Refresh interval is half the lease time
	leaseSecs := cfg.Routing.Import.Install.RouteLeaseSeconds
	if leaseSecs <= 0 {
		leaseSecs = 30
	}
	refreshInterval := time.Duration(leaseSecs/2) * time.Second
	if refreshInterval < 30*time.Second {
		refreshInterval = 30 * time.Second
	}

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	healthTicker := time.NewTicker(30 * time.Second)
	defer healthTicker.Stop()

	// Build a VNI -> table mapping from overlays for route cleanup
	vniToTable := make(map[uint32]int)
	for _, overlay := range cfg.GetOverlays() {
		table := overlay.Routing.Import.Install.Table
		if table == 0 {
			table = 100 // default
		}
		vniToTable[uint32(overlay.VNI)] = table
	}

	// Default table for routes with unknown VNI
	defaultTable := cfg.Routing.Import.Install.Table
	if defaultTable == 0 {
		defaultTable = 100
	}
	flushOnPeerDown := cfg.Routing.Import.Install.FlushOnPeerDown

	// Helper to get table for a route based on its VNI
	getTableForRoute := func(r controlplane.Route) int {
		if table, ok := vniToTable[r.VNI]; ok {
			return table
		}
		return defaultTable
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-healthTicker.C:
			// Check peer health and get newly unhealthy peers
			downPeers, err := client.CheckPeerHealth(ctx)
			if err != nil {
				logger.Warn("peer health check failed", "error", err)
			}

			// Remove routes from peers that just went down
			if len(downPeers) > 0 && flushOnPeerDown {
				for _, peerID := range downPeers {
					// Get routes for this peer from route table
					peerRoutes := routeTable.GetByPeer(peerID)
					for _, r := range peerRoutes {
						_, ipnet, err := net.ParseCIDR(r.Prefix)
						if err != nil {
							continue
						}
						routeTable := getTableForRoute(r)
						if err := routeMgr.Delete(nlmgr.RouteConfig{
							Destination: ipnet,
							Table:       routeTable,
						}); err != nil {
							logger.Warn("failed to delete route for down peer",
								"prefix", r.Prefix,
								"peer", peerID,
								"table", routeTable,
								"error", err,
							)
						} else {
							logger.Info("removed route for down peer",
								"prefix", r.Prefix,
								"peer", peerID,
								"table", routeTable,
							)
						}
					}
					// Remove from route table
					removed := routeTable.RemoveByPeer(peerID)
					logger.Info("cleaned up routes for down peer",
						"peer_id", peerID,
						"routes_removed", removed,
					)
				}
			}

			// Expire stale routes and remove from kernel
			expiredRoutes := routeTable.ExpireStale()
			for _, r := range expiredRoutes {
				_, ipnet, err := net.ParseCIDR(r.Prefix)
				if err != nil {
					continue
				}
				routeTable := getTableForRoute(r)
				if err := routeMgr.Delete(nlmgr.RouteConfig{
					Destination: ipnet,
					Table:       routeTable,
				}); err != nil {
					logger.Warn("failed to delete expired route",
						"prefix", r.Prefix,
						"peer", r.PeerID,
						"table", routeTable,
						"error", err,
					)
				} else {
					logger.Info("removed expired route",
						"prefix", r.Prefix,
						"peer", r.PeerID,
						"table", routeTable,
					)
				}
			}
			if len(expiredRoutes) > 0 {
				logger.Info("expired stale routes", "count", len(expiredRoutes))
			}

		case <-ticker.C:
			// Re-announce our routes to peers
			localRoutes := getLocalExportableRoutes(cfg, routeTable)
			if len(localRoutes) > 0 {
				if err := client.AnnounceRoutes(ctx, localRoutes); err != nil {
					logger.Warn("failed to re-announce routes", "error", err)
				}
			}
		}
	}
}
