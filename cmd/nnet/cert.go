package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/nishisan-dev/n-netman/internal/pki"
)

func certCmd() *cobra.Command {
	baseCmd := &cobra.Command{
		Use:   "cert",
		Short: "Manage PKI and certificates",
		Long:  `Utilities for generating and managing internal CA and host certificates for mTLS.`,
	}

	baseCmd.AddCommand(initCACmd())
	baseCmd.AddCommand(genHostCmd())

	return baseCmd
}

func initCACmd() *cobra.Command {
	var outputDir string
	var days int

	cmd := &cobra.Command{
		Use:   "init-ca",
		Short: "Generate a new self-signed Root CA",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("🔐 Generating Root CA in '%s'...\n", outputDir)
			if err := pki.GenerateCA(outputDir, days); err != nil {
				return err
			}
			fmt.Println("✅ CA generated successfully!")
			fmt.Printf("   Files: %s/ca.crt, %s/ca.key\n", outputDir, outputDir)
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputDir, "output-dir", "o", ".", "Directory to save the certificates")
	cmd.Flags().IntVar(&days, "days", 365, "Validity duration in days")

	return cmd
}

func genHostCmd() *cobra.Command {
	var outputDir, caCert, caKey, host string
	var ips []string
	var days int

	cmd := &cobra.Command{
		Use:   "gen-host",
		Short: "Generate a host certificate signed by the CA",
		RunE: func(cmd *cobra.Command, args []string) error {
			if host == "" {
				// try to get hostname
				h, err := os.Hostname()
				if err != nil {
					return fmt.Errorf("hostname is required")
				}
				host = h
			}

			// Validate and parse IPs
			var parsedIPs []net.IP
			for _, ipsum := range ips {
				parsed := net.ParseIP(ipsum)
				if parsed == nil {
					return fmt.Errorf("invalid IP address: %s", ipsum)
				}
				parsedIPs = append(parsedIPs, parsed)
			}

			// verify CA files exist
			if _, err := os.Stat(caCert); os.IsNotExist(err) {
				return fmt.Errorf("CA certificate not found at %s", caCert)
			}
			if _, err := os.Stat(caKey); os.IsNotExist(err) {
				return fmt.Errorf("CA private key not found at %s", caKey)
			}

			// Resolve absolute paths for CA files to handle relative paths correctly
			absCaCert, _ := filepath.Abs(caCert)
			absCaKey, _ := filepath.Abs(caKey)

			fmt.Printf("📜 Generating certificate for host '%s'...\n", host)
			if err := pki.GenerateHostCert(outputDir, absCaCert, absCaKey, host, parsedIPs, days); err != nil {
				return err
			}

			fmt.Println("✅ Host certificate generated successfully!")
			fmt.Printf("   Files: %s/%s.crt, %s/%s.key\n", outputDir, host, outputDir, host)
			return nil
		},
	}

	cmd.Flags().StringVarP(&outputDir, "output-dir", "o", ".", "Directory to save the certificates")
	cmd.Flags().StringVar(&caCert, "ca-cert", "ca.crt", "Path to CA certificate")
	cmd.Flags().StringVar(&caKey, "ca-key", "ca.key", "Path to CA private key")
	cmd.Flags().StringVar(&host, "host", "", "Hostname for the certificate (defaults to system hostname)")
	cmd.Flags().StringSliceVar(&ips, "ip", nil, "List of IP addresses for SANs (comma separated)")
	cmd.Flags().IntVar(&days, "days", 365, "Validity duration in days")

	return cmd
}
