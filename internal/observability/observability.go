// Package observability provides logging, metrics, and health check functionality.
package observability

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/lucas/n-netman/internal/config"
)

// Metrics holds all Prometheus metrics for n-netman.
type Metrics struct {
	// Reconciler metrics
	ReconciliationsTotal   prometheus.Counter
	ReconciliationErrors   prometheus.Counter
	ReconciliationDuration prometheus.Histogram
	LastReconcileTime      prometheus.Gauge

	// Network metrics
	VXLANsActive    prometheus.Gauge
	BridgesActive   prometheus.Gauge
	FDBEntriesTotal prometheus.Gauge

	// Peer metrics
	PeersConfigured prometheus.Gauge
	PeersConnected  prometheus.Gauge
	PeersHealthy    prometheus.Gauge

	// Route metrics
	RoutesExported prometheus.Gauge
	RoutesImported prometheus.Gauge

	// Control plane metrics
	GRPCRequestsTotal   *prometheus.CounterVec
	GRPCRequestDuration *prometheus.HistogramVec
}

// NewMetrics creates and registers all Prometheus metrics.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		ReconciliationsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nnetman",
			Name:      "reconciliations_total",
			Help:      "Total number of reconciliation cycles",
		}),
		ReconciliationErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "nnetman",
			Name:      "reconciliation_errors_total",
			Help:      "Total number of reconciliation errors",
		}),
		ReconciliationDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "nnetman",
			Name:      "reconciliation_duration_seconds",
			Help:      "Duration of reconciliation cycles",
			Buckets:   prometheus.DefBuckets,
		}),
		LastReconcileTime: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Name:      "last_reconcile_timestamp_seconds",
			Help:      "Timestamp of last successful reconciliation",
		}),
		VXLANsActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Name:      "vxlans_active",
			Help:      "Number of active VXLAN interfaces",
		}),
		BridgesActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Name:      "bridges_active",
			Help:      "Number of active bridges",
		}),
		FDBEntriesTotal: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Name:      "fdb_entries_total",
			Help:      "Total number of FDB entries",
		}),
		PeersConfigured: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Name:      "peers_configured",
			Help:      "Number of configured peers",
		}),
		PeersConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Name:      "peers_connected",
			Help:      "Number of connected peers",
		}),
		PeersHealthy: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Name:      "peers_healthy",
			Help:      "Number of healthy peers",
		}),
		RoutesExported: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Name:      "routes_exported",
			Help:      "Number of routes being exported",
		}),
		RoutesImported: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Name:      "routes_imported",
			Help:      "Number of routes imported from peers",
		}),
		GRPCRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nnetman",
			Name:      "grpc_requests_total",
			Help:      "Total number of gRPC requests",
		}, []string{"method", "status"}),
		GRPCRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "nnetman",
			Name:      "grpc_request_duration_seconds",
			Help:      "Duration of gRPC requests",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method"}),
	}

	// Register all metrics
	reg.MustRegister(
		m.ReconciliationsTotal,
		m.ReconciliationErrors,
		m.ReconciliationDuration,
		m.LastReconcileTime,
		m.VXLANsActive,
		m.BridgesActive,
		m.FDBEntriesTotal,
		m.PeersConfigured,
		m.PeersConnected,
		m.PeersHealthy,
		m.RoutesExported,
		m.RoutesImported,
		m.GRPCRequestsTotal,
		m.GRPCRequestDuration,
	)

	return m
}

// Server provides HTTP endpoints for metrics and health checks.
type Server struct {
	cfg           *config.Config
	logger        *slog.Logger
	metricsServer *http.Server
	healthServer  *http.Server

	mu        sync.RWMutex
	healthy   bool
	ready     bool
	startTime time.Time
}

// NewServer creates a new observability server.
func NewServer(cfg *config.Config, logger *slog.Logger) *Server {
	return &Server{
		cfg:       cfg,
		logger:    logger,
		healthy:   true,
		ready:     false,
		startTime: time.Now(),
	}
}

// Start starts the metrics and health check servers.
func (s *Server) Start(ctx context.Context) error {
	// Start metrics server
	if s.cfg.Observability.Metrics.Enabled {
		if err := s.startMetricsServer(); err != nil {
			return err
		}
	}

	// Start health check server
	if s.cfg.Observability.Healthcheck.Enabled {
		if err := s.startHealthServer(); err != nil {
			return err
		}
	}

	return nil
}

func (s *Server) startMetricsServer() error {
	addr := fmt.Sprintf("%s:%d",
		s.cfg.Observability.Metrics.Listen.Address,
		s.cfg.Observability.Metrics.Listen.Port,
	)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())

	s.metricsServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		s.logger.Info("metrics server started", "address", addr)
		if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("metrics server error", "error", err)
		}
	}()

	return nil
}

func (s *Server) startHealthServer() error {
	addr := fmt.Sprintf("%s:%d",
		s.cfg.Observability.Healthcheck.Listen.Address,
		s.cfg.Observability.Healthcheck.Listen.Port,
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/livez", s.handleLive)

	s.healthServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		s.logger.Info("health server started", "address", addr)
		if err := s.healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.logger.Error("health server error", "error", err)
		}
	}()

	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	healthy := s.healthy
	s.mu.RUnlock()

	if healthy {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status": "healthy"}`)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, `{"status": "unhealthy"}`)
	}
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	ready := s.ready
	s.mu.RUnlock()

	if ready {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status": "ready"}`)
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintln(w, `{"status": "not ready"}`)
	}
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.startTime).Seconds()
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"status": "alive", "uptime_seconds": %.0f}`+"\n", uptime)
}

// SetHealthy sets the health status.
func (s *Server) SetHealthy(healthy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthy = healthy
}

// SetReady sets the readiness status.
func (s *Server) SetReady(ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ready = ready
}

// Stop gracefully stops the servers.
func (s *Server) Stop(ctx context.Context) error {
	var errs []error

	if s.metricsServer != nil {
		if err := s.metricsServer.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if s.healthServer != nil {
		if err := s.healthServer.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("shutdown errors: %v", errs)
	}

	return nil
}
