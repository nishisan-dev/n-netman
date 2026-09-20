package observability

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nishisan-dev/n-netman/internal/config"
)

func TestNewMetrics_DoesNotPanicOnDuplicateRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()
	// Registering twice on the same registry must not panic.
	first := NewMetrics(reg)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second NewMetrics panicked: %v", r)
		}
	}()
	second := NewMetrics(reg)

	// The second call must reuse the already-registered collectors so updates
	// stay wired to the registry.
	if first.ReconciliationsTotal != second.ReconciliationsTotal {
		t.Fatal("expected duplicate NewMetrics to reuse the existing collector")
	}
}

func newTestServer() *Server {
	return NewServer(&config.Config{}, nil)
}

func healthCode(s *Server) int {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.handleHealth(rec, req)
	return rec.Code
}

func TestHandleHealth_ComposesFlagAndPredicate(t *testing.T) {
	s := newTestServer()
	s.SetHealthy(true)

	// No predicate, healthy flag -> 200.
	if got := healthCode(s); got != http.StatusOK {
		t.Fatalf("expected 200 when healthy with no predicate, got %d", got)
	}

	// Predicate false -> 503 even though flag is true.
	s.SetHealthFunc(func() bool { return false })
	if got := healthCode(s); got != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when predicate is false, got %d", got)
	}

	// Predicate true but manual flag false (e.g. shutdown) -> 503.
	s.SetHealthFunc(func() bool { return true })
	s.SetHealthy(false)
	if got := healthCode(s); got != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when manual flag is false, got %d", got)
	}
}

func TestNewAgentMetrics_DuplicateRegistrationReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()

	first := NewAgentMetrics(reg)
	second := NewAgentMetrics(reg)

	// The second construction must reuse the registered collectors rather than
	// panicking or returning ones wired to nothing.
	first.AdvertisementsReceived.WithLabelValues("ens3").Inc()
	second.AdvertisementsReceived.WithLabelValues("ens3").Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	for _, f := range families {
		if f.GetName() != "nnetman_agent_advertisements_received_total" {
			continue
		}
		if got := f.GetMetric()[0].GetCounter().GetValue(); got != 2 {
			t.Fatalf("expected both increments on one collector, got %v", got)
		}
		return
	}
	t.Fatal("agent metric was not registered")
}
