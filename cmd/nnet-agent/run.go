package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"

	"github.com/nishisan-dev/n-netman/internal/agent"
	"github.com/nishisan-dev/n-netman/internal/config"
	"github.com/nishisan-dev/n-netman/internal/observability"
)

func runCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the agent in the foreground",
		Long: `Applies the configured static addresses, joins each segment's multicast group
and keeps the routing table in sync with what the controller advertises.

Routes are installed with a dedicated protocol id, so stopping the agent
withdraws exactly what it installed and nothing else.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run()
		},
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Observability.Logging.Level, cfg.Observability.Logging.Format)
	logger.Info("starting nnet-agent",
		"version", version, "agent_id", cfg.Agent.ID, "interfaces", len(cfg.Interfaces))

	for _, warning := range cfg.Warnings() {
		logger.Warn(warning)
	}

	metrics := observability.NewAgentMetrics(prometheus.DefaultRegisterer)

	a, err := agent.New(cfg, logger, metrics)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Info("received shutdown signal", "signal", sig)
		cancel()
	}()

	servers := startEndpoints(cfg.Observability, cfg.Agent.ID, a, logger)

	if err := a.Run(ctx); err != nil {
		logger.Error("agent stopped", "error", err)
	}

	// Withdraw before the endpoints go away, so a scrape during shutdown still
	// reflects reality.
	if err := a.Withdraw(); err != nil {
		logger.Warn("failed to withdraw some routes", "error", err)
	}
	for _, srv := range servers {
		_ = srv.Close()
	}

	logger.Info("nnet-agent stopped")
	return nil
}

// startEndpoints exposes /metrics and the health endpoints, mirroring nnetd.
func startEndpoints(obs config.ObsConfig, agentID string, a *agent.Agent, logger *slog.Logger) []*http.Server {
	var servers []*http.Server

	if obs.Metrics.Enabled {
		mux := http.NewServeMux()
		mux.Handle("/metrics", observability.MetricsHandler())
		addr := fmt.Sprintf("%s:%d", obs.Metrics.Listen.Address, obs.Metrics.Listen.Port)
		servers = append(servers, observability.StartHTTP("metrics", addr, mux, logger))
	}

	if obs.Healthcheck.Enabled {
		mux := http.NewServeMux()
		// Healthy means every interface still hears from a controller: the
		// agent process being up says nothing about whether routes are fresh.
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			if !a.Healthy() {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unhealthy"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
		})
		mux.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
		})
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
			if !a.Healthy() {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
		})
		mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, agentStatus{
				AgentID:    agentID,
				Version:    version,
				Healthy:    a.Healthy(),
				Interfaces: a.Status(),
			})
		})
		addr := fmt.Sprintf("%s:%d", obs.Healthcheck.Listen.Address, obs.Healthcheck.Listen.Port)
		servers = append(servers, observability.StartHTTP("health", addr, mux, logger))
	}

	return servers
}

// agentStatus is the /status payload, consumed by `nnet-agent status`.
type agentStatus struct {
	AgentID    string                  `json:"agent_id"`
	Version    string                  `json:"version"`
	Healthy    bool                    `json:"healthy"`
	Interfaces []agent.InterfaceStatus `json:"interfaces"`
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(payload)
}
