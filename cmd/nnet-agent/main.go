// nnet-agent - route injection agent for VMs attached to an n-netman bridge.
//
// The agent runs after netplan, applies the static address declared for each
// interface, and programs routes from advertisements the controller publishes
// on the segment's multicast group.
package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nishisan-dev/n-netman/internal/agentconfig"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"

	configPath string
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "nnet-agent",
		Short: "n-netman VM agent - receive routes from the overlay controller",
		Long: `nnet-agent runs inside a VM attached to an n-netman bridge.

It applies the static address configured for each interface and installs the
routes the controller advertises on the segment, so a VM does not need its
routing table maintained by hand as the overlay changes.`,
	}

	rootCmd.PersistentFlags().StringVarP(&configPath, "config", "c",
		"/etc/n-netman/agent.yaml", "Path to the agent configuration file")

	rootCmd.AddCommand(versionCmd())
	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(statusCmd())
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
			fmt.Printf("nnet-agent %s (commit: %s, built: %s)\n", version, commit, buildDate)
		},
	}
}

func loadConfig() (*agentconfig.Config, error) {
	cfg, err := agentconfig.NewLoader().LoadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load agent config from %s: %w", configPath, err)
	}
	return cfg, nil
}

// newLogger builds a slog logger honoring the configured level and format,
// matching how nnetd is configured.
func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	if strings.ToLower(format) == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
