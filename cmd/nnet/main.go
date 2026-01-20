// nnet CLI - Command line interface for n-netman
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
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

func applyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "apply",
		Short: "Apply configuration and reconcile state",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Implement apply logic
			fmt.Println("Applying configuration from:", configPath)
			fmt.Println("TODO: Implement apply")
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current overlay status",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Implement status logic
			fmt.Println("TODO: Show status of VXLAN, bridges, peers")
			return nil
		},
	}
}

func routesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "routes",
		Short: "List announced and learned routes",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Implement routes logic
			fmt.Println("TODO: Show exported and imported routes")
			return nil
		},
	}
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run diagnostics on the network and environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			// TODO: Implement doctor logic
			fmt.Println("Running diagnostics...")
			fmt.Println("TODO: Check netlink, bridges, VXLAN, peer connectivity")
			return nil
		},
	}
}
