package tests

import (
	"context"
	"testing"

	"github.com/open-inference-mesh/oim/internal/capability"
	"github.com/open-inference-mesh/oim/internal/exoadapter"
)

// realTopologyState augments realClusterState with nodeSystem (temp/power/GPU)
// and topology.connections — captured live against the same 3-device cluster,
// exactly the fields DeviceStat needs that ClusterInfo/aggregateClusterStats
// never touch.
func realTopologyState() map[string]any {
	state := realClusterState()
	topology := state["topology"].(map[string]any)
	topology["connections"] = map[string]any{
		"6c1ed5f107db1f493a5867e033d525b2": map[string]any{
			"629ae8788ab947024cefc387256fa090": []any{map[string]any{}},
			"38c002ca35fd8548b3f19880882dcc9d": []any{map[string]any{}},
		},
		"629ae8788ab947024cefc387256fa090": map[string]any{
			"6c1ed5f107db1f493a5867e033d525b2": []any{map[string]any{}},
		},
	}
	state["nodeSystem"] = map[string]any{
		"629ae8788ab947024cefc387256fa090": map[string]any{
			"gpuUsage": 0.0, "temp": 7.8, "sysPower": 11.3, "pcpuUsage": 0.002, "ecpuUsage": 0.31,
		},
		"6c1ed5f107db1f493a5867e033d525b2": map[string]any{
			"gpuUsage": 0.0, "temp": 31.7, "sysPower": 7.8, "pcpuUsage": 0.003, "ecpuUsage": 0.16,
		},
		"38c002ca35fd8548b3f19880882dcc9d": map[string]any{
			"gpuUsage": 0.217, "temp": 66.7, "sysPower": 31.2, "pcpuUsage": 0.6, "ecpuUsage": 0.79,
		},
	}
	return state
}

func TestGetDeviceTopologyReturnsPerDeviceStats(t *testing.T) {
	srv := stateServer(t, realTopologyState())
	exo := exoadapter.New(srv.URL)

	topo, err := capability.GetDeviceTopology(context.Background(), exo)
	if err != nil {
		t.Fatalf("GetDeviceTopology: %v", err)
	}
	if topo == nil {
		t.Fatal("expected a non-nil topology for a 3-device cluster")
	}
	if len(topo.Devices) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(topo.Devices))
	}

	byID := make(map[string]capability.DeviceStat, len(topo.Devices))
	for _, d := range topo.Devices {
		byID[d.DeviceID] = d
	}

	mbp := byID["38c002ca35fd8548b3f19880882dcc9d"]
	if mbp.FriendlyName != "MBP" {
		t.Errorf("expected friendly name MBP, got %q", mbp.FriendlyName)
	}
	if mbp.RAMTotalGB < 15.9 || mbp.RAMTotalGB > 16.1 {
		t.Errorf("expected ~16 GB total, got %v", mbp.RAMTotalGB)
	}
	if mbp.TempC != 66.7 {
		t.Errorf("expected temp 66.7, got %v", mbp.TempC)
	}
	if mbp.PowerW != 31.2 {
		t.Errorf("expected power 31.2, got %v", mbp.PowerW)
	}
	if mbp.RAMUsedPct < 80 {
		t.Errorf("expected the near-maxed MacBook Pro to show high used pct, got %v", mbp.RAMUsedPct)
	}

	lab02 := byID["6c1ed5f107db1f493a5867e033d525b2"]
	if len(lab02.ConnectedTo) != 2 {
		t.Errorf("expected lab-02 to connect to 2 peers, got %v", lab02.ConnectedTo)
	}
}

func TestGetDeviceTopologyReturnsNilWithoutTopology(t *testing.T) {
	srv := stateServer(t, map[string]any{})
	exo := exoadapter.New(srv.URL)

	topo, err := capability.GetDeviceTopology(context.Background(), exo)
	if err != nil {
		t.Fatalf("GetDeviceTopology: %v", err)
	}
	if topo != nil {
		t.Errorf("expected nil topology when Exo reports no topology, got %v", topo)
	}
}
