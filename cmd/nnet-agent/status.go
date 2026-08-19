package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what the running agent has learned",
		Long: `Queries the running agent's health endpoint and reports, per interface, the
controllers currently holding a lease and the routes installed from them.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			addr := fmt.Sprintf("http://%s:%d/status",
				cfg.Observability.Healthcheck.Listen.Address,
				cfg.Observability.Healthcheck.Listen.Port)

			body, err := fetch(addr)
			if err != nil {
				return fmt.Errorf("could not reach the agent at %s (is nnet-agent running?): %w", addr, err)
			}

			if asJSON {
				fmt.Println(string(body))
				return nil
			}

			var status agentStatus
			if err := json.Unmarshal(body, &status); err != nil {
				return fmt.Errorf("failed to parse the agent status: %w", err)
			}
			printStatus(status)
			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the raw JSON status")
	return cmd
}

func fetch(url string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return body, nil
}

func printStatus(status agentStatus) {
	health := "healthy"
	if !status.Healthy {
		health = "no live controller"
	}
	fmt.Printf("Agent:  %s (%s)\n", status.AgentID, status.Version)
	fmt.Printf("Health: %s\n", health)

	for _, iface := range status.Interfaces {
		fmt.Printf("\nInterface %s\n", iface.Interface)
		fmt.Printf("  address: %s   group: %s:%d   table: %d\n",
			iface.Address, iface.Group, iface.Port, iface.Table)
		if len(iface.ExpectTags) > 0 {
			fmt.Printf("  expects tags: %v\n", iface.ExpectTags)
		}

		if len(iface.Controllers) == 0 {
			fmt.Println("  controllers: none (no advertisement received, or every lease expired)")
		} else {
			fmt.Println("  controllers:")
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "    ID\tSEGMENT\tVNI\tROUTES\tEXPIRES IN")
			for _, c := range iface.Controllers {
				fmt.Fprintf(w, "    %s\t%s\t%d\t%d\t%s\n",
					c.ID, c.Segment, c.VNI, c.Routes, time.Until(c.ExpiresAt).Round(time.Second))
			}
			w.Flush()
		}

		if len(iface.Routes) == 0 {
			fmt.Println("  routes: none")
			continue
		}
		fmt.Println("  routes:")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "    PREFIX\tVIA\tMETRIC")
		for _, r := range iface.Routes {
			fmt.Fprintf(w, "    %s\t%s\t%d\n", r.Prefix, r.NextHop, r.Metric)
		}
		w.Flush()
	}
}
