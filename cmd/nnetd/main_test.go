package main

import (
	"sort"
	"testing"

	"github.com/nishisan-dev/n-netman/internal/config"
)

func v2TwoOverlays() *config.Config {
	mk := func(vni, table int, name, bridge string) config.OverlayDef {
		o := config.OverlayDef{VNI: vni, Name: name, Bridge: config.BridgeConfig{Name: bridge}}
		o.Routing.Import.Install.Table = table
		return o
	}
	return &config.Config{
		Version:  2,
		Overlays: []config.OverlayDef{mk(100, 100, "a", "br-a"), mk(200, 200, "b", "br-b")},
	}
}

func TestTableForVNI(t *testing.T) {
	cfg := v2TwoOverlays()

	if got := tableForVNI(cfg, 100); got != 100 {
		t.Errorf("tableForVNI(100) = %d, want 100", got)
	}
	if got := tableForVNI(cfg, 200); got != 200 {
		t.Errorf("tableForVNI(200) = %d, want 200", got)
	}
	// Unknown VNI falls back to the global/default table (100).
	if got := tableForVNI(cfg, 999); got != 100 {
		t.Errorf("tableForVNI(999) = %d, want 100 (default)", got)
	}
}

func TestTableForVNI_OverlayWithZeroTableDefaultsTo100(t *testing.T) {
	cfg := &config.Config{
		Version:  2,
		Overlays: []config.OverlayDef{{VNI: 100, Name: "a", Bridge: config.BridgeConfig{Name: "br-a"}}},
	}
	if got := tableForVNI(cfg, 100); got != 100 {
		t.Errorf("tableForVNI(100) with table 0 = %d, want 100", got)
	}
}

func TestDistinctImportTables(t *testing.T) {
	cfg := v2TwoOverlays()
	tables := distinctImportTables(cfg)
	sort.Ints(tables)
	// Overlays use 100 and 200; the global default (0 -> 100) dedups with 100.
	if len(tables) != 2 || tables[0] != 100 || tables[1] != 200 {
		t.Fatalf("distinctImportTables = %v, want [100 200]", tables)
	}
}
