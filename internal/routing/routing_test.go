package routing

import (
	"testing"

	"github.com/nishisan-dev/n-netman/internal/config"
	"github.com/nishisan-dev/n-netman/internal/controlplane"
)

func overlayWith(imp config.ImportConfig) config.OverlayDef {
	return config.OverlayDef{
		VNI:     100,
		Name:    "test",
		Bridge:  config.BridgeConfig{Name: "br-test"},
		Routing: config.OverlayRouting{Import: imp},
	}
}

func TestShouldImportForOverlay(t *testing.T) {
	mgr := NewManager(&config.Config{})

	cases := []struct {
		name   string
		prefix string
		imp    config.ImportConfig
		want   bool
	}{
		{
			name:   "deny supernet announcement is rejected (overlap)",
			prefix: "10.0.0.0/8",
			imp:    config.ImportConfig{AcceptAll: true, Deny: []string{"10.1.0.0/16"}},
			want:   false,
		},
		{
			name:   "deny exact subnet is rejected",
			prefix: "10.1.5.0/24",
			imp:    config.ImportConfig{AcceptAll: true, Deny: []string{"10.1.0.0/16"}},
			want:   false,
		},
		{
			name:   "allow contains route is admitted",
			prefix: "172.16.10.0/24",
			imp:    config.ImportConfig{Allow: []string{"172.16.0.0/16"}},
			want:   true,
		},
		{
			name:   "route broader than allow is not admitted",
			prefix: "172.0.0.0/8",
			imp:    config.ImportConfig{Allow: []string{"172.16.0.0/16"}},
			want:   false,
		},
		{
			name:   "accept_all admits anything not denied",
			prefix: "192.168.5.0/24",
			imp:    config.ImportConfig{AcceptAll: true},
			want:   true,
		},
		{
			name:   "empty policy denies by default",
			prefix: "192.168.5.0/24",
			imp:    config.ImportConfig{},
			want:   false,
		},
		{
			name:   "deny default route beats accept_all",
			prefix: "10.0.0.0/24",
			imp:    config.ImportConfig{AcceptAll: true, Deny: []string{"0.0.0.0/0"}},
			want:   false,
		},
		{
			name:   "ipv6 within allow is admitted",
			prefix: "2001:db8:10::/64",
			imp:    config.ImportConfig{Allow: []string{"2001:db8::/32"}},
			want:   true,
		},
		{
			name:   "invalid prefix is rejected",
			prefix: "not-a-cidr",
			imp:    config.ImportConfig{AcceptAll: true},
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mgr.ShouldImportForOverlay(controlplane.Route{Prefix: tc.prefix}, overlayWith(tc.imp))
			if got != tc.want {
				t.Fatalf("ShouldImportForOverlay(%s) = %v, want %v", tc.prefix, got, tc.want)
			}
		})
	}
}

func TestGetExportRoutesForOverlay(t *testing.T) {
	mgr := NewManager(&config.Config{})
	overlay := config.OverlayDef{
		VNI: 100,
		Routing: config.OverlayRouting{
			Export: config.ExportConfig{Networks: []string{"172.16.10.0/24", "172.16.20.0/24"}, Metric: 0},
		},
	}

	routes := mgr.GetExportRoutesForOverlay(overlay)
	if len(routes) != 2 {
		t.Fatalf("expected 2 export routes, got %d", len(routes))
	}
	for _, r := range routes {
		if r.Metric != 100 { // default applied when Metric==0
			t.Errorf("expected default metric 100, got %d", r.Metric)
		}
		if r.VNI != 100 {
			t.Errorf("expected VNI 100, got %d", r.VNI)
		}
	}
}
