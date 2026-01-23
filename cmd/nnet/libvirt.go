package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nishisan-dev/n-netman/internal/libvirt"
	nlink "github.com/nishisan-dev/n-netman/internal/netlink"
)

func libvirtCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "libvirt",
		Short: "Manage libvirt/KVM integration",
		Long: `Commands for integrating n-netman bridges with libvirt VMs.
Allows attaching VM interfaces to overlay bridges and managing systemd dependencies.`,
	}

	cmd.AddCommand(libvirtEnableCmd())
	cmd.AddCommand(libvirtDisableCmd())
	cmd.AddCommand(libvirtStatusCmd())
	cmd.AddCommand(libvirtListVMsCmd())
	cmd.AddCommand(libvirtAttachCmd())
	cmd.AddCommand(libvirtDetachCmd())

	return cmd
}

func libvirtEnableCmd() *cobra.Command {
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Configure systemd dependency for libvirt",
		Long: `Creates a systemd drop-in to make libvirt.service depend on n-netman.service.
This ensures bridges exist before VMs start at boot.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dryRun {
				fmt.Println("🔍 Dry-run mode - would create:")
				fmt.Printf("   %s\n", libvirt.GetDropInPath())
				fmt.Println("   And run 'systemctl daemon-reload'")
				return nil
			}

			if err := libvirt.EnableDependency(); err != nil {
				return fmt.Errorf("failed to enable dependency: %w", err)
			}

			fmt.Printf("✓ Created %s\n", libvirt.GetDropInPath())
			fmt.Println("✓ Ran 'systemctl daemon-reload'")
			fmt.Println()
			fmt.Println("libvirt.service now depends on n-netman.service.")
			fmt.Println("VMs will only start after n-netman bridges are ready.")

			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be done without making changes")

	return cmd
}

func libvirtDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Remove systemd dependency for libvirt",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !libvirt.IsDependencyEnabled() {
				fmt.Println("ℹ️  Dependency is not enabled, nothing to do.")
				return nil
			}

			if err := libvirt.DisableDependency(); err != nil {
				return fmt.Errorf("failed to disable dependency: %w", err)
			}

			fmt.Printf("✓ Removed %s\n", libvirt.GetDropInPath())
			fmt.Println("✓ Ran 'systemctl daemon-reload'")

			return nil
		},
	}
}

func libvirtStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show libvirt integration status",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			fmt.Println("🔗 Libvirt Integration Status")
			fmt.Println("─────────────────────────────────────────")

			// Check systemd dependency
			if libvirt.IsDependencyEnabled() {
				fmt.Println("  ✓ Systemd dependency configured (libvirt → n-netman)")
			} else {
				fmt.Println("  ⚠ Systemd dependency NOT configured")
				fmt.Println("    Run 'nnet libvirt enable' to configure")
			}

			// Check n-netman service
			nnetStatus, _ := libvirt.GetServiceStatus("n-netman")
			fmt.Printf("  • n-netman.service: %s\n", strings.TrimSpace(nnetStatus))

			// Check libvirt service
			libvirtStatus, _ := libvirt.GetServiceStatus("libvirtd")
			fmt.Printf("  • libvirtd.service: %s\n", strings.TrimSpace(libvirtStatus))

			// Show managed bridges
			fmt.Println()
			fmt.Println("🌉 Managed Bridges:")
			fmt.Println("─────────────────────────────────────────")

			bridgeMgr := nlink.NewBridgeManager()
			overlays := cfg.GetOverlays()

			for _, o := range overlays {
				bridgeInfo, err := bridgeMgr.Get(o.Bridge.Name)
				if err != nil {
					fmt.Printf("  • %s (VNI %d) - ❌ NOT FOUND\n", o.Bridge.Name, o.VNI)
				} else {
					status := "DOWN"
					if bridgeInfo.Up {
						status = "UP"
					}
					fmt.Printf("  • %s (VNI %d) - %s\n", o.Bridge.Name, o.VNI, status)
				}
			}

			// Show VMs using n-netman bridges
			fmt.Println()
			fmt.Println("🖥️  VMs using n-netman bridges:")
			fmt.Println("─────────────────────────────────────────")

			client := libvirt.NewClient()
			domains, err := client.ListDomains(true)
			if err != nil {
				fmt.Printf("  ⚠ Could not list VMs: %s\n", err)
				return nil
			}

			// Build set of managed bridges
			managedBridges := make(map[string]bool)
			for _, o := range overlays {
				managedBridges[o.Bridge.Name] = true
			}

			found := false
			for _, domain := range domains {
				interfaces, err := client.GetDomainInterfaces(domain.Name)
				if err != nil {
					continue
				}
				for _, iface := range interfaces {
					if managedBridges[iface.Bridge] {
						found = true
						fmt.Printf("  • %s → %s (MAC: %s)\n", domain.Name, iface.Bridge, iface.MAC)
					}
				}
			}

			if !found {
				fmt.Println("  (no VMs attached to n-netman bridges)")
			}

			return nil
		},
	}
}

func libvirtListVMsCmd() *cobra.Command {
	var showAll bool
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list-vms",
		Short: "List libvirt VMs and their interfaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			client := libvirt.NewClient()
			domains, err := client.ListDomains(showAll)
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			if len(domains) == 0 {
				fmt.Println("No VMs found.")
				return nil
			}

			// Build set of managed bridges for marking
			managedBridges := make(map[string]bool)
			for _, o := range cfg.GetOverlays() {
				managedBridges[o.Bridge.Name] = true
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "VM NAME\tSTATE\tMAC\tBRIDGE\t")
			fmt.Fprintln(w, "───────\t─────\t───\t──────\t")

			for _, domain := range domains {
				interfaces, err := client.GetDomainInterfaces(domain.Name)
				if err != nil || len(interfaces) == 0 {
					fmt.Fprintf(w, "%s\t%s\t-\t-\t\n", domain.Name, domain.State)
					continue
				}

				for i, iface := range interfaces {
					vmName := domain.Name
					state := domain.State
					if i > 0 {
						vmName = ""
						state = ""
					}

					mark := ""
					if managedBridges[iface.Bridge] {
						mark = " ✓"
					}

					fmt.Fprintf(w, "%s\t%s\t%s\t%s%s\t\n", vmName, state, iface.MAC, iface.Bridge, mark)
				}
			}
			w.Flush()

			_ = jsonOutput // TODO: implement JSON output

			return nil
		},
	}

	cmd.Flags().BoolVar(&showAll, "all", false, "Include shut off VMs")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func libvirtAttachCmd() *cobra.Command {
	var bridge string
	var mac string

	cmd := &cobra.Command{
		Use:   "attach <vm-name>",
		Short: "Attach a VM to an n-netman bridge",
		Long: `Adds a new network interface to the specified VM, connected to the given bridge.
The interface is persisted in the domain XML and applied live if the VM is running.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]

			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			// Validate bridge exists and is managed by n-netman
			bridgeMgr := nlink.NewBridgeManager()
			_, err = bridgeMgr.Get(bridge)
			if err != nil {
				// Check if it's a known n-netman bridge
				overlays := cfg.GetOverlays()
				var availableBridges []string
				for _, o := range overlays {
					availableBridges = append(availableBridges, o.Bridge.Name)
				}
				return fmt.Errorf("bridge '%s' does not exist.\n  Did you run 'nnet apply' first?\n  Available n-netman bridges: %s",
					bridge, strings.Join(availableBridges, ", "))
			}

			// Validate VM exists
			client := libvirt.NewClient()
			if !client.DomainExists(vmName) {
				return fmt.Errorf("VM '%s' does not exist", vmName)
			}

			// Attach interface
			assignedMAC, err := client.AttachInterface(vmName, bridge, mac)
			if err != nil {
				return fmt.Errorf("failed to attach interface: %w", err)
			}

			fmt.Printf("✓ Added interface to '%s' on bridge '%s'\n", vmName, bridge)
			fmt.Printf("  MAC: %s\n", assignedMAC)

			return nil
		},
	}

	cmd.Flags().StringVar(&bridge, "bridge", "", "Bridge to attach the VM to (required)")
	cmd.Flags().StringVar(&mac, "mac", "", "MAC address for the interface (optional, auto-generated if not specified)")
	_ = cmd.MarkFlagRequired("bridge")

	return cmd
}

func libvirtDetachCmd() *cobra.Command {
	var mac string

	cmd := &cobra.Command{
		Use:   "detach <vm-name>",
		Short: "Detach a VM interface by MAC address",
		Long: `Removes a network interface from the specified VM by its MAC address.
The interface is removed from both the domain XML and the running VM.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]

			client := libvirt.NewClient()

			// Validate VM exists
			if !client.DomainExists(vmName) {
				return fmt.Errorf("VM '%s' does not exist", vmName)
			}

			// Detach interface
			if err := client.DetachInterface(vmName, mac); err != nil {
				return fmt.Errorf("failed to detach interface: %w", err)
			}

			fmt.Printf("✓ Removed interface %s from '%s'\n", mac, vmName)

			return nil
		},
	}

	cmd.Flags().StringVar(&mac, "mac", "", "MAC address of the interface to remove (required)")
	_ = cmd.MarkFlagRequired("mac")

	return cmd
}
