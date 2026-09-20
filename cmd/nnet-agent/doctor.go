package main

import (
	"fmt"
	"net"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/nishisan-dev/n-netman/internal/inject"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that this VM can run the agent",
		Long: `Verifies the things that make an agent silently useless: an unreadable or
world-readable key, a missing interface, and insufficient privileges to program
routes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("🩺 Running nnet-agent diagnostics...")

			cfg, cfgErr := loadConfig()

			checks := []struct {
				name  string
				check func() (bool, string)
			}{
				{"Config file", func() (bool, string) {
					if cfgErr != nil {
						return false, cfgErr.Error()
					}
					return true, configPath
				}},
				{"Route privileges", func() (bool, string) {
					return checkRoutePrivileges(unix.Capget)
				}},
			}

			if cfgErr == nil {
				for _, iface := range cfg.Interfaces {
					ifaceCfg := iface

					checks = append(checks, struct {
						name  string
						check func() (bool, string)
					}{
						name: "Interface " + ifaceCfg.Name,
						check: func() (bool, string) {
							if _, err := net.InterfaceByName(ifaceCfg.Name); err != nil {
								return false, err.Error()
							}
							return true, "present"
						},
					})

					checks = append(checks, struct {
						name  string
						check func() (bool, string)
					}{
						name: "Key for " + ifaceCfg.Name,
						check: func() (bool, string) {
							if _, err := inject.LoadPSK(ifaceCfg.PSKRef); err != nil {
								return false, err.Error()
							}
							return true, "readable and correctly protected"
						},
					})

					checks = append(checks, struct {
						name  string
						check func() (bool, string)
					}{
						name: "Group for " + ifaceCfg.Name,
						check: func() (bool, string) {
							group, err := cfg.GroupFor(ifaceCfg)
							if err != nil {
								return false, err.Error()
							}
							return true, fmt.Sprintf("%s:%d", group, cfg.PortFor(ifaceCfg))
						},
					})
				}
			}

			passed := 0
			for _, c := range checks {
				ok, msg := c.check()
				if ok {
					passed++
					fmt.Printf("  ✓ %s: %s\n", c.name, msg)
					continue
				}
				fmt.Printf("  ✗ %s: %s\n", c.name, msg)
			}

			fmt.Printf("\n%d/%d checks passed\n", passed, len(checks))
			if passed != len(checks) {
				return fmt.Errorf("some checks failed")
			}
			return nil
		},
	}
}

// Check the effective capability set: a non-root process can have NET_ADMIN,
// while root in a container or restricted service can lack it.
func checkRoutePrivileges(capget func(*unix.CapUserHeader, *unix.CapUserData) error) (bool, string) {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	// Version 3 writes two 32-bit capability words, even when only the first
	// word contains the capability being checked.
	var caps [2]unix.CapUserData
	if err := capget(&header, &caps[0]); err != nil {
		return false, fmt.Sprintf("cannot read effective capabilities: %v", err)
	}
	if caps[unix.CAP_NET_ADMIN/32].Effective&(1<<uint(unix.CAP_NET_ADMIN%32)) == 0 {
		return false, "programming routes needs effective CAP_NET_ADMIN (the packaged unit grants it)"
	}
	return true, "CAP_NET_ADMIN is effective"
}
