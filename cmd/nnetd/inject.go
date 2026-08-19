package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nishisan-dev/n-netman/internal/config"
	"github.com/nishisan-dev/n-netman/internal/controlplane"
	"github.com/nishisan-dev/n-netman/internal/inject"
	"github.com/nishisan-dev/n-netman/internal/observability"
)

// ribSource adapts the control plane route table to the publisher's view of it.
//
// The adaptation lives here, in the daemon, so internal/inject never imports
// the control plane and stays linkable inside a VM.
type ribSource struct {
	table *controlplane.RouteTable
}

func (r ribSource) InjectableRoutes() []inject.RIBRoute {
	all := r.table.All()
	out := make([]inject.RIBRoute, 0, len(all))
	for _, route := range all {
		out = append(out, inject.RIBRoute{
			Prefix: route.Prefix,
			Metric: route.Metric,
			Tags:   route.Tags,
			VNI:    route.VNI,
		})
	}
	return out
}

// startInjectPublishers starts one publisher per bridge the inject rules match.
//
// A misconfigured channel fails startup rather than degrading quietly: the
// operator asked for injection explicitly, so a bad key or a contradictory rule
// set is an error, not a warning.
func startInjectPublishers(
	ctx context.Context,
	cfg *config.Config,
	table *controlplane.RouteTable,
	metrics *observability.Metrics,
	logger *slog.Logger,
) ([]*inject.Publisher, error) {
	injectCfg := cfg.Routing.Inject
	if !injectCfg.Enabled {
		return nil, nil
	}

	psk, err := inject.LoadPSK(injectCfg.PSKRef)
	if err != nil {
		return nil, fmt.Errorf("inject channel: %w", err)
	}

	source := ribSource{table: table}

	var publishers []*inject.Publisher
	for _, overlay := range cfg.GetOverlays() {
		publisher, err := inject.NewPublisher(inject.PublisherConfig{
			ControllerID: cfg.Node.ID,
			VNI:          uint32(overlay.VNI),
			Segment:      overlay.Name,
			BridgeName:   overlay.Bridge.Name,
			BridgeIPv4:   overlay.Bridge.IPv4,
			Tags:         overlay.Bridge.Tags,
			Inject:       injectCfg,
			PSK:          psk,
			KeyID:        injectCfg.KeyID,
		},
			inject.WithLogger(logger),
			inject.WithMetrics(metrics),
			inject.WithRouteSource(source),
		)
		if err != nil {
			stopInjectPublishers(publishers, logger)
			return nil, fmt.Errorf("inject channel on overlay %q: %w", overlay.Name, err)
		}
		if publisher == nil {
			// The bridge carries no tag any rule selects on.
			continue
		}

		publishers = append(publishers, publisher)
		go func(p *inject.Publisher, name string) {
			if err := p.Run(ctx); err != nil && err != context.Canceled {
				logger.Error("inject publisher stopped", "overlay", name, "error", err)
			}
		}(publisher, overlay.Name)

		logger.Info("inject publisher started",
			"overlay", overlay.Name,
			"bridge", overlay.Bridge.Name,
			"vni", overlay.VNI,
			"tags", overlay.Bridge.Tags,
			"rules_matched", len(publisher.Rules()),
		)
	}

	if len(publishers) == 0 {
		logger.Warn("routing.inject is enabled but no bridge carries a tag any rule matches; nothing will be advertised",
			"rules", len(injectCfg.Rules))
	}

	return publishers, nil
}

// injectStatus reports the publishers' state on /status.
type injectStatus struct {
	publishers []*inject.Publisher
}

func (i injectStatus) GetInjectStatus() []observability.InjectSegmentStatus {
	out := make([]observability.InjectSegmentStatus, 0, len(i.publishers))
	for _, p := range i.publishers {
		s := p.Status()
		out = append(out, observability.InjectSegmentStatus{
			Segment:        s.Segment,
			VNI:            s.VNI,
			Bridge:         s.Bridge,
			Tags:           s.Tags,
			Group:          s.Group,
			Port:           s.Port,
			RulesMatched:   s.RulesMatched,
			Routes:         s.Routes,
			DefaultGateway: s.DefaultGateway,
			Sequence:       s.Sequence,
			LastPublished:  s.LastPublished,
			LastError:      s.LastError,
		})
	}
	return out
}

// NewInjectStatusProvider exposes the publishers to the /status endpoint.
func NewInjectStatusProvider(publishers []*inject.Publisher) observability.InjectStatusProvider {
	return injectStatus{publishers: publishers}
}

// stopInjectPublishers releases the publishers' sockets.
func stopInjectPublishers(publishers []*inject.Publisher, logger *slog.Logger) {
	for _, p := range publishers {
		if err := p.Close(); err != nil {
			logger.Warn("failed to close inject publisher", "error", err)
		}
	}
}
