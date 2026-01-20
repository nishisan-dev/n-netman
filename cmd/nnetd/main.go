// n-netman daemon - Lightweight VXLAN overlay manager for Linux
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lucas/n-netman/internal/config"
	"github.com/lucas/n-netman/internal/controlplane"
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

	// Initialize route table for control plane
	routeTable := controlplane.NewRouteTable()

	// Start gRPC control plane server
	cpServer := controlplane.NewServer(cfg, routeTable, logger)
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
