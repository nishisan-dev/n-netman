package main

import (
	"fmt"
	"net"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/nishisan-dev/n-netman/internal/config"
	"github.com/nishisan-dev/n-netman/internal/inject"
	"github.com/nishisan-dev/n-netman/internal/observability"
)

func injectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inject",
		Short: "Inspect the route injection channel",
		Long: `The injection channel advertises routes to nnet-agent instances running inside
VMs attached to a tagged bridge. See docs/inject.md.`,
	}

	cmd.AddCommand(injectStatusCmd())
	return cmd
}

func injectStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what each bridge is advertising",
		Long: `Reports, per bridge, the tags it carries, the group it publishes on, how many
inject rules matched and what the last advertisement contained.

Live figures come from the running daemon. With the daemon stopped, the command
still reports what the configuration resolves to, so a rule that matches
nothing can be spotted without starting anything.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			if !cfg.Routing.Inject.Enabled {
				fmt.Println("Route injection is disabled (routing.inject.enabled is false).")
				return nil
			}

			printInjectConfig(cfg)

			status := getDaemonStatus(cfg)
			if status == nil {
				fmt.Println("\nDaemon not reachable; showing configuration only.")
				return nil
			}
			if len(status.Inject) == 0 {
				fmt.Println("\nThe daemon reports no active inject publisher.")
				return nil
			}

			printInjectLive(status.Inject)
			return nil
		},
	}
}

// printInjectConfig resolves the configuration without contacting the daemon.
func printInjectConfig(cfg *config.Config) {
	injectCfg := cfg.Routing.Inject

	fmt.Printf("Inject channel: group base %s, port %d, every %s, lease %ds\n",
		injectCfg.GetGroupBase(), injectCfg.GetPort(),
		injectCfg.GetInterval(), injectCfg.GetLeaseSeconds())

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\nBRIDGE\tVNI\tTAGS\tGROUP\tRULES MATCHED")

	for _, overlay := range cfg.GetOverlays() {
		matched := injectCfg.MatchingRules(overlay.Bridge.Tags)

		group := "-"
		if base := net.ParseIP(injectCfg.GetGroupBase()); base != nil {
			if g, err := inject.GroupForVNI(base, uint32(overlay.VNI)); err == nil {
				group = fmt.Sprintf("%s:%d", g, injectCfg.GetPort())
			}
		}

		tags := "-"
		if len(overlay.Bridge.Tags) > 0 {
			tags = fmt.Sprintf("%v", overlay.Bridge.Tags)
		}

		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%d\n",
			overlay.Bridge.Name, overlay.VNI, tags, group, len(matched))
	}
	w.Flush()
}

func printInjectLive(segments []observability.InjectSegmentStatus) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\nBRIDGE\tROUTES\tGATEWAY\tSEQ\tLAST PUBLISHED\tERROR")

	for _, s := range segments {
		last := "never"
		if !s.LastPublished.IsZero() {
			last = fmt.Sprintf("%s ago", time.Since(s.LastPublished).Round(time.Second))
		}
		gateway := s.DefaultGateway
		if gateway == "" {
			gateway = "-"
		}
		errText := s.LastError
		if errText == "" {
			errText = "-"
		}
		fmt.Fprintf(w, "%s\t%d\t%s\t%d\t%s\t%s\n",
			s.Bridge, s.Routes, gateway, s.Sequence, last, errText)
	}
	w.Flush()
}
