package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nishisan-dev/n-netman/internal/agent"
)

func TestNewLogger_Formats(t *testing.T) {
	// Only that construction succeeds for every accepted level/format pair; the
	// handler choice is what nnetd already exercises.
	for _, level := range []string{"debug", "info", "warn", "error", "nonsense"} {
		for _, format := range []string{"json", "text"} {
			if newLogger(level, format) == nil {
				t.Fatalf("newLogger(%q, %q) returned nil", level, format)
			}
		}
	}
}

// The status payload is what `nnet-agent status` parses back, so the two must
// agree on the shape.
func TestAgentStatus_RoundTrip(t *testing.T) {
	in := agentStatus{
		AgentID: "vm-app-01",
		Version: "test",
		Healthy: true,
		Interfaces: []agent.InterfaceStatus{{
			Interface: "ens3",
			Address:   "10.100.0.50/24",
			Group:     "239.8.0.100",
			Port:      4790,
			Table:     254,
			Controllers: []agent.ControllerStatus{
				{ID: "host-a", Segment: "vxlan-prod", VNI: 100, Routes: 2},
			},
			Routes: []agent.Route{
				{Prefix: "172.16.10.0/24", NextHop: "10.100.0.1", Metric: 100},
			},
		}},
	}

	encoded, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out agentStatus
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if out.AgentID != in.AgentID || !out.Healthy {
		t.Fatalf("identity did not survive: %+v", out)
	}
	if len(out.Interfaces) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(out.Interfaces))
	}
	iface := out.Interfaces[0]
	if iface.Group != "239.8.0.100" || iface.Table != 254 {
		t.Fatalf("interface fields did not survive: %+v", iface)
	}
	if len(iface.Controllers) != 1 || iface.Controllers[0].ID != "host-a" {
		t.Fatalf("controllers did not survive: %+v", iface.Controllers)
	}
	if len(iface.Routes) != 1 || iface.Routes[0].Prefix != "172.16.10.0/24" {
		t.Fatalf("routes did not survive: %+v", iface.Routes)
	}

	// Rendering must not panic on a populated status.
	printStatus(out)
}

func TestPrintStatus_HandlesAnEmptyAgent(t *testing.T) {
	// A freshly started agent that has heard nothing yet must still render.
	printStatus(agentStatus{AgentID: "vm-app-01", Version: "test", Healthy: false})
}

func TestStatusCmd_ReportsAnUnreachableAgent(t *testing.T) {
	// Point at a config that cannot be loaded; the command must say so rather
	// than panic.
	configPath = "/nonexistent/agent.yaml"
	err := statusCmd().RunE(nil, nil)
	if err == nil {
		t.Fatal("expected an error for a missing config")
	}
	if !strings.Contains(err.Error(), "failed to load agent config") {
		t.Fatalf("unexpected error: %v", err)
	}
}
