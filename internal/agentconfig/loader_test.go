package agentconfig

import (
	"strings"
	"testing"
)

const validAgentConfig = `
version: 1
agent:
  id: "vm-app-01"
interfaces:
  - name: "ens3"
    vni: 100
    address: "10.100.0.50/24"
    expect_tags: ["it"]
    psk_ref: "file:/etc/n-netman/psk/inject.key"
`

func TestLoader_Load_Valid(t *testing.T) {
	cfg, err := NewLoader().Load([]byte(validAgentConfig))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if cfg.Agent.ID != "vm-app-01" {
		t.Errorf("expected agent.id vm-app-01, got %q", cfg.Agent.ID)
	}
	if len(cfg.Interfaces) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(cfg.Interfaces))
	}

	iface := cfg.Interfaces[0]
	group, err := cfg.GroupFor(iface)
	if err != nil {
		t.Fatalf("GroupFor: %v", err)
	}
	// Must match the group the controller derives for the same VNI.
	if group.String() != "239.8.0.100" {
		t.Errorf("expected group 239.8.0.100, got %s", group)
	}
	if port := cfg.PortFor(iface); port != 4790 {
		t.Errorf("expected default port 4790, got %d", port)
	}
	// The main table is the default: ordinary VM traffic uses it with no rules.
	if iface.Install.Table != 0 {
		t.Errorf("expected the main table by default, got %d", iface.Install.Table)
	}
	if iface.Install.AcceptDefaultGateway {
		t.Error("accepting an advertised default gateway must be opt-in")
	}
	if got := iface.Install.GetMetric(); got != DefaultRouteMetric {
		t.Errorf("expected metric %d, got %d", DefaultRouteMetric, got)
	}
}

func TestLoader_Load_Defaults(t *testing.T) {
	cfg, err := NewLoader().Load([]byte(validAgentConfig))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !cfg.Observability.Metrics.Enabled || cfg.Observability.Metrics.Listen.Port != 9111 {
		t.Errorf("unexpected metrics defaults: %+v", cfg.Observability.Metrics)
	}
	if cfg.Observability.Healthcheck.Listen.Port != 9112 {
		t.Errorf("unexpected healthcheck port: %d", cfg.Observability.Healthcheck.Listen.Port)
	}
	if cfg.Inject.GetGroupBase() != "239.8.0.0" {
		t.Errorf("unexpected group base: %s", cfg.Inject.GetGroupBase())
	}
}

func TestLoader_Load_Rejections(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "missing version",
			yaml: strings.Replace(validAgentConfig, "version: 1", "", 1),
		},
		{
			name: "controller config version",
			yaml: strings.Replace(validAgentConfig, "version: 1", "version: 2", 1),
		},
		{
			name: "missing agent id",
			yaml: `
version: 1
interfaces:
  - name: "ens3"
    vni: 100
    address: "10.100.0.50/24"
    psk_ref: "file:/k"
`,
		},
		{
			name: "no interfaces",
			yaml: `
version: 1
agent:
  id: "vm-app-01"
`,
		},
		{
			name: "neither vni nor group",
			yaml: `
version: 1
agent:
  id: "vm-app-01"
interfaces:
  - name: "ens3"
    address: "10.100.0.50/24"
    psk_ref: "file:/k"
`,
		},
		{
			name: "address is not a CIDR",
			yaml: strings.Replace(validAgentConfig, `address: "10.100.0.50/24"`, `address: "10.100.0.50"`, 1),
		},
		{
			name: "missing psk_ref",
			yaml: strings.Replace(validAgentConfig, `psk_ref: "file:/etc/n-netman/psk/inject.key"`, "", 1),
		},
		{
			name: "duplicate interface",
			yaml: validAgentConfig + `
  - name: "ens3"
    vni: 200
    address: "10.200.0.50/24"
    psk_ref: "file:/k"
`,
		},
		{
			name: "non-multicast explicit group",
			yaml: strings.Replace(validAgentConfig, `vni: 100`, `group: "10.0.0.1"`, 1),
		},
		{
			name: "empty expect tag",
			yaml: strings.Replace(validAgentConfig, `expect_tags: ["it"]`, `expect_tags: ["it", ""]`, 1),
		},
		{
			name: "reserved table out of range",
			yaml: validAgentConfig + `
    install:
      table: 300
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewLoader().Load([]byte(tc.yaml)); err == nil {
				t.Fatal("expected an error, got none")
			}
		})
	}
}

func TestLoader_Load_ExplicitGroupOverridesVNI(t *testing.T) {
	yaml := strings.Replace(validAgentConfig, `vni: 100`, "vni: 100\n    group: \"239.9.9.9\"", 1)
	cfg, err := NewLoader().Load([]byte(yaml))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	group, err := cfg.GroupFor(cfg.Interfaces[0])
	if err != nil {
		t.Fatalf("GroupFor: %v", err)
	}
	if group.String() != "239.9.9.9" {
		t.Fatalf("expected the explicit group to win, got %s", group)
	}
}

func TestConfig_Warnings(t *testing.T) {
	yaml := `
version: 1
agent:
  id: "vm-app-01"
interfaces:
  - name: "ens3"
    vni: 100
    address: "10.100.0.50/24"
    psk_ref: "file:/k"
    install:
      accept_default_gateway: true
`
	cfg, err := NewLoader().Load([]byte(yaml))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	warnings := cfg.Warnings()
	if len(warnings) != 2 {
		t.Fatalf("expected warnings for the missing expect_tags and the accepted default gateway, got %v", warnings)
	}
}

func TestImportPolicy_Policy(t *testing.T) {
	p := ImportPolicy{AcceptAll: true, Allow: []string{"172.16.0.0/16"}, Deny: []string{"0.0.0.0/0"}}
	got := p.Policy()
	if !got.AcceptAll || len(got.Allow) != 1 || len(got.Deny) != 1 {
		t.Fatalf("policy did not carry over: %+v", got)
	}
	// The shared evaluator must behave the same here as on the controller.
	if got.Admits("10.0.0.0/8") {
		t.Fatal("deny must beat accept_all")
	}
}

// The example shipped in the repo must always load and validate, mirroring the
// controller's TestLoader_ShippedExamplesAreValid.
func TestLoader_ShippedExampleIsValid(t *testing.T) {
	cfg, err := NewLoader().LoadFile("../../examples/agent.yaml")
	if err != nil {
		t.Fatalf("shipped agent example failed to load: %v", err)
	}
	group, err := cfg.GroupFor(cfg.Interfaces[0])
	if err != nil {
		t.Fatalf("GroupFor: %v", err)
	}
	if group.String() != "239.8.0.100" {
		t.Fatalf("expected the example to resolve to 239.8.0.100, got %s", group)
	}
}
