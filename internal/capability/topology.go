package capability

import (
	"context"
	"sort"

	"github.com/open-inference-mesh/oim/internal/exoadapter"
)

// DeviceStat is one physical device's live stats within an Exo cluster —
// everything the local Node Setup dashboard needs to draw a per-device
// topology view (RAM bar, temp, power, GPU load, and links to peers).
//
// This is LOCAL-ONLY data, surfaced through the agent's own /detect endpoint
// for the operator's own dashboard. It deliberately carries more detail
// (hostnames, exact chip variant) than ClusterInfo/CapabilityManifest ever
// broadcasts to the coordinator/mesh — the operator looking at their own
// hardware is a different trust boundary than what gets announced publicly.
type DeviceStat struct {
	DeviceID       string   `json:"device_id"`
	FriendlyName   string   `json:"friendly_name"`
	ModelID        string   `json:"model_id"` // e.g. "Mac Studio", "MacBook Pro"
	ChipID         string   `json:"chip_id"`  // e.g. "Apple M1 Max"
	RAMTotalGB     float64  `json:"ram_total_gb"`
	RAMAvailableGB float64  `json:"ram_available_gb"`
	RAMUsedPct     float64  `json:"ram_used_pct"`
	GPUUsagePct    float64  `json:"gpu_usage_pct"`
	TempC          float64  `json:"temp_c"`
	PowerW         float64  `json:"power_w"`
	ConnectedTo    []string `json:"connected_to"` // device IDs this device has a direct link to
}

// DeviceTopology is the full per-device breakdown for one Exo instance.
type DeviceTopology struct {
	Devices []DeviceStat `json:"devices"`
}

// GetDeviceTopology inspects Exo's /state and returns per-device live stats
// for every device in the topology (solo node included — a 1-device
// "cluster" is still a valid topology to draw, just with no connections).
// Returns (nil, nil) when Exo reports no topology at all (not yet connected
// to any device, including itself) rather than erroring — callers should
// treat that as "nothing to draw yet," not a failure.
func GetDeviceTopology(ctx context.Context, exo *exoadapter.Client) (*DeviceTopology, error) {
	state, err := exo.GetState(ctx)
	if err != nil {
		return nil, err
	}
	topology, _ := state["topology"].(map[string]any)
	if topology == nil {
		return nil, nil
	}

	var deviceIDs []string
	if nodes, _ := topology["nodes"].([]any); len(nodes) > 0 {
		for _, n := range nodes {
			if id, ok := n.(string); ok {
				deviceIDs = append(deviceIDs, id)
			}
		}
	}
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	sort.Strings(deviceIDs) // stable ordering across polls, independent of map iteration

	nodeMemory, _ := state["nodeMemory"].(map[string]any)
	nodeIdentities, _ := state["nodeIdentities"].(map[string]any)
	nodeSystem, _ := state["nodeSystem"].(map[string]any)
	connections, _ := topology["connections"].(map[string]any)

	devices := make([]DeviceStat, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		d := DeviceStat{DeviceID: id}

		if ident, ok := nodeIdentities[id].(map[string]any); ok {
			d.FriendlyName, _ = ident["friendlyName"].(string)
			d.ModelID, _ = ident["modelId"].(string)
			d.ChipID, _ = ident["chipId"].(string)
		}
		if d.FriendlyName == "" {
			d.FriendlyName = id
		}

		if mem, ok := nodeMemory[id].(map[string]any); ok {
			totalBytes := extractBytes(mem, "ramTotal")
			availBytes := extractBytes(mem, "ramAvailable")
			d.RAMTotalGB = round2(totalBytes / (1 << 30))
			d.RAMAvailableGB = round2(availBytes / (1 << 30))
			if totalBytes > 0 {
				d.RAMUsedPct = round2(100 * (1 - availBytes/totalBytes))
			}
		}

		if sys, ok := nodeSystem[id].(map[string]any); ok {
			if v, ok := sys["gpuUsage"].(float64); ok {
				d.GPUUsagePct = round2(v * 100)
			}
			if v, ok := sys["temp"].(float64); ok {
				d.TempC = round2(v)
			}
			if v, ok := sys["sysPower"].(float64); ok {
				d.PowerW = round2(v)
			}
		}

		if peers, ok := connections[id].(map[string]any); ok {
			peerIDs := make([]string, 0, len(peers))
			for peerID := range peers {
				peerIDs = append(peerIDs, peerID)
			}
			sort.Strings(peerIDs)
			d.ConnectedTo = peerIDs
		}

		devices = append(devices, d)
	}

	return &DeviceTopology{Devices: devices}, nil
}

// extractBytes reads a nested {"<key>": {"inBytes": N}} field, as Exo's
// nodeMemory entries are shaped.
func extractBytes(m map[string]any, key string) float64 {
	obj, _ := m[key].(map[string]any)
	n, _ := obj["inBytes"].(float64)
	return n
}
