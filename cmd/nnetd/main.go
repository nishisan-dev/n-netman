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

	"github.com/lucas/n-netman/internal/config"
	"github.com/lucas/n-netman/internal/controlplane"
	nlmgr "github.com/lucas/n-netman/internal/netlink"
	"github.com/lucas/n-netman/internal/observability"
	"github.com/lucas/n-netman/internal/reconciler"
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

	slog.Info("configuration loaded successfully",
		"node_id", cfg.Node.ID,
		"hostname", cfg.Node.Hostname,
		"vxlan_vni", cfg.Overlay.VXLAN.VNI,
		"peers_count", len(cfg.Overlay.Peers),
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
	metrics.PeersConfigured.Set(float64(len(cfg.Overlay.Peers)))

	// Start observability server (metrics + health)
	obsServer := observability.NewServer(cfg, logger)
	if err := obsServer.Start(ctx); err != nil {
		slog.Error("failed to start observability server", "error", err)
		os.Exit(1)
	}
	defer obsServer.Stop(context.Background())

	// Initialize route manager for installing routes
	routeMgr := nlmgr.NewRouteManager()

	// Initialize route table for control plane
	routeTable := controlplane.NewRouteTable()

	// Create route installer callback
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

	// Start control plane client (connect to peers)
	cpClient := controlplane.NewClient(cfg, routeTable, logger)
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

		// Start periodic health checks and route refresh
		go runRouteRefreshLoop(ctx, cpClient, cfg, routeTable, logger)
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
}

// installReceivedRoutes installs routes received from peers into the kernel.
func installReceivedRoutes(cfg *config.Config, routeMgr *nlmgr.RouteManager, routes []controlplane.Route, logger *slog.Logger) {
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

		// Get the routing table from config
		table := cfg.Routing.Import.Install.Table

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
				"error", err,
			)
			continue
		}

		logger.Info("installed route from peer",
			"prefix", r.Prefix,
			"next_hop", r.NextHop,
			"peer", r.PeerID,
			"metric", r.Metric,
		)
	}
}

// getLocalExportableRoutes returns routes that should be exported to peers.
func getLocalExportableRoutes(cfg *config.Config, routeTable *controlplane.RouteTable) []controlplane.Route {
	routes := make([]controlplane.Route, 0)

	// Detect local IP by finding interface that can reach our peers
	localIP := detectLocalIP(cfg)
	if localIP == "" {
		return routes // Can't export routes without a valid next-hop
	}

	// Add routes from config exports
	for _, prefix := range cfg.Routing.Export.Networks {
		routes = append(routes, controlplane.Route{
			Prefix:       prefix,
			NextHop:      localIP,
			Metric:       uint32(cfg.Routing.Export.Metric),
			LeaseSeconds: uint32(cfg.Routing.Import.Install.RouteLeaseSeconds),
		})
	}

	return routes
}

// detectLocalIP finds the local IP address that should be used for route announcements.
// It looks for the IP on the same subnet as configured peers.
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

// runRouteRefreshLoop periodically refreshes routes with peers.
func runRouteRefreshLoop(ctx context.Context, client *controlplane.Client, cfg *config.Config, routeTable *controlplane.RouteTable, logger *slog.Logger) {
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

	for {
		select {
		case <-ctx.Done():
			return
		case <-healthTicker.C:
			// Check peer health
			if err := client.CheckPeerHealth(ctx); err != nil {
				logger.Warn("peer health check failed", "error", err)
			}

			// Expire stale routes
			expired := routeTable.ExpireStale()
			if expired > 0 {
				logger.Info("expired stale routes", "count", expired)
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
