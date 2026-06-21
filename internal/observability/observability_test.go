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
	_ = NewMetrics(reg)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second NewMetrics panicked: %v", r)
		}
	}()
	_ = NewMetrics(reg)
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
