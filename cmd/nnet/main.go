// nnet CLI - Command line interface for n-netman
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/lucas/n-netman/internal/config"
	nlink "github.com/lucas/n-netman/internal/netlink"
	"github.com/lucas/n-netman/internal/observability"
	"github.com/lucas/n-netman/internal/reconciler"
	"github.com/lucas/n-netman/internal/routing"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"

	configPath string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "nnet",
		Short: "n-netman CLI - Manage VXLAN overlays",
		Long: `nnet is the command line interface for n-netman.
It allows you to apply configurations, check status, and manage your VXLAN overlay network.`,
	}

	// Global flags
	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c", "/etc/n-netman/n-netman.yaml", "Path to configuration file")

	// Add subcommands
	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(applyCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(routesCmd())
	rootCmd.AddCommand(doctorCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("nnet %s (commit: %s, built: %s)\n", version, commit, buildDate)
		},
	}
}

func loadConfig() (*config.Config, error) {
	loader := config.NewLoader()
	cfg, err := loader.LoadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load config from %s: %w", configPath, err)
	}
	return cfg, nil
}

func applyCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply configuration and reconcile state",
		Long: `Apply reads the configuration file and reconciles the system state.
It creates/updates VXLAN interfaces, bridges, and FDB entries as needed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			fmt.Printf("📋 Loading configuration from: %s\n", configPath)
			fmt.Printf("   Node ID: %s\n", cfg.Node.ID)
			fmt.Printf("   VXLAN: %s (VNI %d)\n", cfg.Overlay.VXLAN.Name, cfg.Overlay.VXLAN.VNI)
			fmt.Printf("   Bridge: %s\n", cfg.Overlay.VXLAN.Bridge)
			fmt.Printf("   Peers: %d\n\n", len(cfg.Overlay.Peers))

			if dryRun {
				fmt.Println("🔍 Dry-run mode - no changes will be made")
				fmt.Println("\nWould perform:")
				fmt.Printf("  • Create bridge: %s\n", cfg.Overlay.VXLAN.Bridge)
				fmt.Printf("  • Create VXLAN: %s (VNI %d)\n", cfg.Overlay.VXLAN.Name, cfg.Overlay.VXLAN.VNI)
				for _, peer := range cfg.Overlay.Peers {
					fmt.Printf("  • Add FDB entry for peer: %s (%s)\n", peer.ID, peer.Endpoint.Address)
				}
				return nil
			}

			fmt.Println("🔧 Applying configuration...")

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			rec := reconciler.New(cfg)
			if err := rec.RunOnce(ctx); err != nil {
				return fmt.Errorf("reconciliation failed: %w", err)
			}

			fmt.Println("\n✅ Configuration applied successfully!")
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without making changes")

	return cmd
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current overlay status",
		Long:  `Display the current state of VXLAN interfaces, bridges, and peer connections.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			fmt.Printf("🖥️  Node: %s (%s)\n\n", cfg.Node.ID, cfg.Node.Hostname)

			// Check VXLAN status
			vxlanMgr := nlink.NewVXLANManager()
			bridgeMgr := nlink.NewBridgeManager()
			routeMgr := nlink.NewRouteManager()

			fmt.Println("📡 VXLAN Interfaces:")
			fmt.Println("─────────────────────────────────────────")

			vxlanInfo, err := vxlanMgr.Get(cfg.Overlay.VXLAN.Name)
			if err != nil {
				fmt.Printf("  ❌ %s: not found\n", cfg.Overlay.VXLAN.Name)
			} else {
				status := "🔴 DOWN"
				if vxlanInfo.Up {
					status = "🟢 UP"
				}
				fmt.Printf("  %s %s (VNI %d, MTU %d)\n", status, vxlanInfo.Name, vxlanInfo.VNI, vxlanInfo.MTU)
			}

			fmt.Println()
			fmt.Println("🌉 Bridges:")
			fmt.Println("─────────────────────────────────────────")

			bridgeInfo, err := bridgeMgr.Get(cfg.Overlay.VXLAN.Bridge)
			if err != nil {
				fmt.Printf("  ❌ %s: not found\n", cfg.Overlay.VXLAN.Bridge)
			} else {
				status := "🔴 DOWN"
				if bridgeInfo.Up {
					status = "🟢 UP"
				}
				fmt.Printf("  %s %s (MTU %d)\n", status, bridgeInfo.Name, bridgeInfo.MTU)
				if len(bridgeInfo.AttachedInterfaces) > 0 {
					fmt.Printf("      Attached: %v\n", bridgeInfo.AttachedInterfaces)
				}
			}

			// Try to get live status from daemon
			daemonStatus := getDaemonStatus(cfg)

			fmt.Println()
			fmt.Println("👥 Configured Peers:")
			fmt.Println("─────────────────────────────────────────")

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "  ID\tENDPOINT\tSTATUS\tROUTES")
			fmt.Fprintln(w, "  ──\t────────\t──────\t──────")

			if daemonStatus != nil {
				// Use live status from daemon
				for _, peer := range cfg.Overlay.Peers {
					ps, ok := daemonStatus.Peers[peer.ID]
					if ok {
						statusIcon := getStatusIcon(ps.Status)
						lastSeen := ""
						if ps.LastSeen != "" {
							lastSeen = " (" + ps.LastSeen + " ago)"
						}
						fmt.Fprintf(w, "  %s\t%s\t%s %s%s\t%d\n", ps.ID, ps.Endpoint, statusIcon, ps.Status, lastSeen, ps.Routes)
					} else {
						fmt.Fprintf(w, "  %s\t%s\t⏳ unknown\t-\n", peer.ID, peer.Endpoint.Address)
					}
				}
			} else {
				// Fallback: daemon not running
				for _, peer := range cfg.Overlay.Peers {
					fmt.Fprintf(w, "  %s\t%s\t⚠️  daemon offline\t-\n", peer.ID, peer.Endpoint.Address)
				}
			}
			w.Flush()

			// Route Statistics
			fmt.Println()
			fmt.Println("📊 Route Statistics:")
			fmt.Println("─────────────────────────────────────────")

			// Exported routes (from config)
			exportCount := len(cfg.Routing.Export.Networks)
			fmt.Printf("  📤 Exported:   %d route(s)", exportCount)
			if exportCount > 0 && exportCount <= 3 {
				fmt.Printf(" (%s)", formatPrefixList(cfg.Routing.Export.Networks))
			}
			fmt.Println()

			// Installed routes (from kernel routing table)
			table := cfg.Routing.Import.Install.Table
			if table == 0 {
				table = 100 // default
			}
			installedRoutes, err := routeMgr.ListByProtocol(table, nlink.RouteProtocolNNetMan)
			installedCount := 0
			if err == nil {
				installedCount = len(installedRoutes)
			}
			fmt.Printf("  📥 Installed:  %d route(s) in table %d\n", installedCount, table)

			// Show installed route details if not too many
			if installedCount > 0 && installedCount <= 5 {
				for _, r := range installedRoutes {
					if r.Destination != nil {
						gw := "-"
						if r.Gateway != nil {
							gw = r.Gateway.String()
						}
						fmt.Printf("      • %s via %s\n", r.Destination.String(), gw)
					}
				}
			}

			return nil
		},
	}
}

// formatPrefixList formats a list of prefixes for display
func formatPrefixList(prefixes []string) string {
	if len(prefixes) == 0 {
		return ""
	}
	result := prefixes[0]
	for i := 1; i < len(prefixes); i++ {
		result += ", " + prefixes[i]
	}
	return result
}

// getDaemonStatus fetches status from the nnetd daemon's /status endpoint.
func getDaemonStatus(cfg *config.Config) *observability.NodeStatus {
	port := cfg.Observability.Healthcheck.Listen.Port
	if port == 0 {
		port = 9110 // default
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/status", port)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil // daemon not reachable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var status observability.NodeStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil
	}

	return &status
}

// getStatusIcon returns an emoji icon for peer status.
func getStatusIcon(status string) string {
	switch status {
	case "healthy":
		return "🟢"
	case "unhealthy":
		return "🟡"
	case "disconnected":
		return "🔴"
	default:
		return "⏳"
	}
}

func routesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "routes",
		Short: "List announced and learned routes",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			routingMgr := routing.NewManager(cfg)
			exportRoutes := routingMgr.GetExportRoutes()

			fmt.Println("📤 Exported Routes (announced to peers):")
			fmt.Println("─────────────────────────────────────────")

			if len(exportRoutes) == 0 {
				fmt.Println("  (none configured)")
			} else {
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "  PREFIX\tMETRIC")
				fmt.Fprintln(w, "  ──────\t──────")
				for _, r := range exportRoutes {
					fmt.Fprintf(w, "  %s\t%d\n", r.Prefix, r.Metric)
				}
				w.Flush()
			}

			fmt.Println()
			fmt.Println("📥 Imported Routes (learned from peers):")
			fmt.Println("─────────────────────────────────────────")
			fmt.Println("  (no active peer connections)")

			return nil
		},
	}
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostics on the network and environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("🩺 Running n-netman diagnostics...")

			checks := []struct {
				name  string
				check func() (bool, string)
			}{
				{"Config file", func() (bool, string) {
					_, err := loadConfig()
					if err != nil {
						return false, err.Error()
					}
					return true, configPath
				}},
				{"Root privileges", func() (bool, string) {
					if os.Geteuid() != 0 {
						return false, "netlink operations require root"
					}
					return true, "running as root"
				}},
				{"VXLAN support", func() (bool, string) {
					// Check if vxlan module is loaded
					if _, err := os.Stat("/sys/module/vxlan"); err != nil {
						return false, "vxlan kernel module not loaded"
					}
					return true, "vxlan module loaded"
				}},
				{"Bridge support", func() (bool, string) {
					if _, err := os.Stat("/sys/module/bridge"); err != nil {
						return false, "bridge kernel module not loaded"
					}
					return true, "bridge module loaded"
				}},
			}

			passed := 0
			for _, c := range checks {
				ok, msg := c.check()
				if ok {
					fmt.Printf("  ✅ %s: %s\n", c.name, msg)
					passed++
				} else {
					fmt.Printf("  ❌ %s: %s\n", c.name, msg)
				}
			}

			fmt.Printf("\n📊 %d/%d checks passed\n", passed, len(checks))

			if passed < len(checks) {
				return fmt.Errorf("some checks failed")
			}
			return nil
		},
	}
}
