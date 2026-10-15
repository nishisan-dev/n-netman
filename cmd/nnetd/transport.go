package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/nishisan-dev/n-netman/internal/config"
	"github.com/nishisan-dev/n-netman/internal/controlplane"
	nl "github.com/nishisan-dev/n-netman/internal/netlink"
	"github.com/nishisan-dev/n-netman/internal/observability"
	"github.com/nishisan-dev/n-netman/internal/reconciler"
	"github.com/prometheus/client_golang/prometheus"
)

func runTransportDaemon(ctx context.Context, cfg *config.Config, obs *observability.Server, metrics *observability.Metrics, logger *slog.Logger) error {
	fib, e := nl.NewCandidateManager(nl.NewRouteManager(), cfg.StateFile)
	if e != nil {
		return e
	}
	unlock, e := fib.Lock()
	if e != nil {
		return e
	}
	defer unlock()
	manager := controlplane.NewSessionManager(cfg, fib, logger, prometheus.DefaultRegisterer)
	obs.SetStatusProvider(manager)
	rec := reconciler.New(cfg, reconciler.WithLogger(logger), reconciler.WithMetrics(metrics), reconciler.WithInterval(5*time.Second))
	// Reconciliation errors (e.g. an absent PW) cannot prevent the backup session.
	if e := rec.RunOnce(ctx); e != nil {
		logger.Warn("initial transport reconciliation", "error", e)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _ = rec.Run(runCtx) }()
	publishers, e := startInjectPublishers(runCtx, cfg, controlplane.NewRouteTable(), metrics, logger)
	if e != nil {
		cancel()
		<-done
		return e
	}
	defer stopInjectPublishers(publishers, logger)
	if len(publishers) > 0 {
		obs.SetInjectStatusProvider(NewInjectStatusProvider(publishers))
	}
	obs.SetHealthFunc(func() bool { return rec.Status().LastErr == nil })
	obs.SetReady(true)
	e = manager.Run(runCtx)
	obs.SetReady(false)
	cancel()
	<-done
	return e
}
