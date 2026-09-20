package observability

import "github.com/prometheus/client_golang/prometheus"

// AgentMetrics holds the Prometheus metrics for nnet-agent.
//
// They are separate from Metrics so a controller does not export a wall of
// always-zero agent series, and vice versa.
type AgentMetrics struct {
	// AdvertisementsReceived counts advertisements accepted on an interface.
	AdvertisementsReceived *prometheus.CounterVec
	// AdvertisementsRejected counts drops, labelled with why. A quiet channel
	// and an actively rejected one look identical without this.
	AdvertisementsRejected *prometheus.CounterVec
	// RoutesInstalled is the size of the route set currently programmed.
	RoutesInstalled *prometheus.GaugeVec
	// ControllersActive is how many controllers hold a live lease.
	ControllersActive *prometheus.GaugeVec
	// LastAdvertisement is when an advertisement was last accepted.
	LastAdvertisement *prometheus.GaugeVec
	// RouteSyncErrors counts failures to program the kernel.
	RouteSyncErrors *prometheus.CounterVec
}

// NewAgentMetrics creates and registers the agent metrics.
func NewAgentMetrics(reg prometheus.Registerer) *AgentMetrics {
	m := &AgentMetrics{
		AdvertisementsReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nnetman",
			Subsystem: "agent",
			Name:      "advertisements_received_total",
			Help:      "Total number of accepted inject advertisements",
		}, []string{"interface"}),
		AdvertisementsRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nnetman",
			Subsystem: "agent",
			Name:      "advertisements_rejected_total",
			Help:      "Total number of rejected inject advertisements, by reason",
		}, []string{"interface", "reason"}),
		RoutesInstalled: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Subsystem: "agent",
			Name:      "routes_installed",
			Help:      "Number of routes currently programmed from advertisements",
		}, []string{"interface"}),
		ControllersActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Subsystem: "agent",
			Name:      "controllers_active",
			Help:      "Number of controllers holding a live lease",
		}, []string{"interface"}),
		LastAdvertisement: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "nnetman",
			Subsystem: "agent",
			Name:      "last_advertisement_timestamp_seconds",
			Help:      "Timestamp of the last accepted advertisement",
		}, []string{"interface"}),
		RouteSyncErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nnetman",
			Subsystem: "agent",
			Name:      "route_sync_errors_total",
			Help:      "Total number of failures programming routes into the kernel",
		}, []string{"interface"}),
	}

	m.AdvertisementsReceived = registerOrExisting(reg, m.AdvertisementsReceived)
	m.AdvertisementsRejected = registerOrExisting(reg, m.AdvertisementsRejected)
	m.RoutesInstalled = registerOrExisting(reg, m.RoutesInstalled)
	m.ControllersActive = registerOrExisting(reg, m.ControllersActive)
	m.LastAdvertisement = registerOrExisting(reg, m.LastAdvertisement)
	m.RouteSyncErrors = registerOrExisting(reg, m.RouteSyncErrors)

	return m
}
