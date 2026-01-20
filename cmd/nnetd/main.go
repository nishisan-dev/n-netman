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

	"github.com/lucas/n-netman/internal/config"
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

	// TODO: Start reconciler loop
	// TODO: Start gRPC server
	// TODO: Start metrics/health servers

	slog.Info("daemon initialized, waiting for events...")

	// Wait for shutdown
	<-ctx.Done()

	slog.Info("shutting down n-netman daemon")
}
