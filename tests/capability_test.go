package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-inference-mesh/oim/internal/capability"
	"github.com/open-inference-mesh/oim/internal/exoadapter"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// realClusterState is trimmed from an actual /state response captured against
// a live 3-device Exo cluster (32 GB + 32 GB + 16 GB = 80 GB), used to catch
// exactly the bug this test suite is guarding against: DetectClusterNode
// under-reporting cluster memory as just one device's RAM.
func realClusterState() map[string]any {
	return map[string]any{
		"topology": map[string]any{
			"nodes": []any{
				"6c1ed5f107db1f493a5867e033d525b2",
				"629ae8788ab947024cefc387256fa090",
				"38c002ca35fd8548b3f19880882dcc9d",
			},
		},
		// ramAvailable values are ALSO from a real live capture of the same
		// cluster (a different moment than ramTotal, but same 3 devices) — at
		// that moment the MacBook Pro (38c002ca...) had only ~2 GB free out of
		// 16 GB while both Mac Studios had 15-18 GB free out of 32 GB each.
		// This is the exact scenario the safe-contribution logic exists for:
		// the MacBook Pro should end up contributing ~nothing while the
		// Studios still contribute most of their headroom.
		"nodeMemory": map[string]any{
			"629ae8788ab947024cefc387256fa090": map[string]any{
				"ramTotal":     map[string]any{"inBytes": float64(34359738368)}, // 32 GB
				"ramAvailable": map[string]any{"inBytes": float64(16730521600)}, // ~15.58 GB free
			},
			"6c1ed5f107db1f493a5867e033d525b2": map[string]any{
				"ramTotal":     map[string]any{"inBytes": float64(34359738368)}, // 32 GB
				"ramAvailable": map[string]any{"inBytes": float64(18860294144)}, // ~17.57 GB free
			},
			"38c002ca35fd8548b3f19880882dcc9d": map[string]any{
				"ramTotal":     map[string]any{"inBytes": float64(17179869184)}, // 16 GB
				"ramAvailable": map[string]any{"inBytes": float64(2169126912)},  // ~2.02 GB free — nearly maxed out
			},
		},
		"nodeIdentities": map[string]any{
			"6c1ed5f107db1f493a5867e033d525b2": map[string]any{
				"modelId": "Mac Studio", "chipId": "Apple M1 Max", "friendlyName": "lab-02",
			},
			"629ae8788ab947024cefc387256fa090": map[string]any{
				"modelId": "Mac Studio", "chipId": "Apple M1 Max", "friendlyName": "lab-01",
			},
			"38c002ca35fd8548b3f19880882dcc9d": map[string]any{
				"modelId": "MacBook Pro", "chipId": "Apple M1 Pro", "friendlyName": "MBP",
			},
		},
	}
}

func stateServer(t *testing.T, state map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/state" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(state)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDetectClusterNodeSumsMemoryAcrossDevices(t *testing.T) {
	srv := stateServer(t, realClusterState())
	exo := exoadapter.New(srv.URL)

	info, err := capability.DetectClusterNode(context.Background(), exo)
	if err != nil {
		t.Fatalf("DetectClusterNode: %v", err)
	}
	if !info.IsCluster {
		t.Fatal("expected IsCluster=true for a 3-device topology")
	}
	if info.DeviceCount != 3 {
		t.Errorf("DeviceCount: want 3, got %d", info.DeviceCount)
	}
	const wantGB = 80.0
	if info.TotalMemGB < wantGB-0.01 || info.TotalMemGB > wantGB+0.01 {
		t.Errorf("TotalMemGB: want %.2f (32+32+16), got %.2f — this is the exact bug reported live: cluster memory under-reported as a single device's RAM", wantGB, info.TotalMemGB)
	}
}

func TestDetectClusterNodeChipFamiliesExcludeHostnames(t *testing.T) {
	srv := stateServer(t, realClusterState())
	exo := exoadapter.New(srv.URL)

	info, err := capability.DetectClusterNode(context.Background(), exo)
	if err != nil {
		t.Fatalf("DetectClusterNode: %v", err)
	}
	if len(info.ChipFamilies) != 3 {
		t.Fatalf("expected 3 chip family entries, got %d: %v", len(info.ChipFamilies), info.ChipFamilies)
	}
	for _, f := range info.ChipFamilies {
		if f != "Apple M1" {
			t.Errorf("chip family: want coarsened %q (variant stripped), got %q", "Apple M1", f)
		}
		// The user explicitly asked for hostnames/exact models to stay out —
		// verify none of the raw identity fields leak into the chip family list.
		for _, forbidden := range []string{"lab-01", "lab-02", "MBP", "Mac Studio", "MacBook Pro", "Max", "Pro"} {
			if f == forbidden {
				t.Errorf("chip family %q leaked an identifying/precise field that should have been excluded or coarsened", f)
			}
		}
	}
}

func TestDetectClusterNodeSingleDeviceIsNotACluster(t *testing.T) {
	state := map[string]any{
		"topology": map[string]any{
			"nodes": []any{"solo-node-id"},
		},
	}
	srv := stateServer(t, state)
	exo := exoadapter.New(srv.URL)

	info, err := capability.DetectClusterNode(context.Background(), exo)
	if err != nil {
		t.Fatalf("DetectClusterNode: %v", err)
	}
	if info.IsCluster {
		t.Error("a topology.nodes list containing only self should not be reported as a cluster")
	}
	if info.DeviceCount != 1 {
		t.Errorf("DeviceCount: want 1, got %d", info.DeviceCount)
	}
}

func TestDetectClusterNodePeersShapeExcludesSelfCorrectly(t *testing.T) {
	// Legacy/alternate shape: "peers" explicitly excludes self, so device
	// count must add 1 — unlike the "nodes" shape, which already includes self.
	state := map[string]any{
		"topology": map[string]any{
			"peers": []any{"peer-a", "peer-b"},
		},
	}
	srv := stateServer(t, state)
	exo := exoadapter.New(srv.URL)

	info, err := capability.DetectClusterNode(context.Background(), exo)
	if err != nil {
		t.Fatalf("DetectClusterNode: %v", err)
	}
	if !info.IsCluster {
		t.Fatal("2 peers + self should be a cluster")
	}
	if info.DeviceCount != 3 {
		t.Errorf("DeviceCount: want 3 (2 peers + self), got %d", info.DeviceCount)
	}
}

func TestDetectClusterNodeSafeContributableDefersToRoomierDevices(t *testing.T) {
	srv := stateServer(t, realClusterState())
	exo := exoadapter.New(srv.URL)

	info, err := capability.DetectClusterNode(context.Background(), exo)
	if err != nil {
		t.Fatalf("DetectClusterNode: %v", err)
	}

	// Reserve = max(2GB, 15% of device total). Studios: max(2, 4.8)=4.8GB
	// reserved each; MacBook Pro: max(2, 2.4)=2.4GB reserved — and it only had
	// ~2.02GB free to begin with, so it should contribute exactly 0, not a
	// negative number silently underflowing into the total.
	const wantSafeGB = (15.58 - 4.8) + (17.57 - 4.8) + 0 // ~23.55 GB
	if info.SafeContributableGB < wantSafeGB-0.5 || info.SafeContributableGB > wantSafeGB+0.5 {
		t.Errorf("SafeContributableGB: want ~%.2f, got %.2f", wantSafeGB, info.SafeContributableGB)
	}
	// Sanity: safe-contributable must always be well under the raw 80GB total —
	// otherwise the safety logic isn't doing anything.
	if info.SafeContributableGB >= info.TotalMemGB {
		t.Errorf("SafeContributableGB (%.2f) should be less than TotalMemGB (%.2f) when any device is under memory pressure", info.SafeContributableGB, info.TotalMemGB)
	}
}

func TestAssembleManifestClampsCapPctToSafeContributable(t *testing.T) {
	srv := stateServer(t, realClusterState())
	exo := exoadapter.New(srv.URL)
	_, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	opts := capability.DefaultOptions()
	opts.MemoryCapPct = 0.9 // operator asks for 90% — far more than is actually safe to give right now

	manifest, err := capability.AssembleManifest(context.Background(), exo, pub, opts)
	if err != nil {
		t.Fatalf("AssembleManifest: %v", err)
	}
	if !manifest.IsCluster {
		t.Fatal("expected a cluster manifest for the 3-device fixture")
	}
	// DeclaredMemoryGB should still report the TRUE total (transparency) —
	// only the cap percentage gets clamped.
	if manifest.DeclaredMemoryGB < 79 || manifest.DeclaredMemoryGB > 81 {
		t.Errorf("DeclaredMemoryGB: want ~80 (true cluster total, unclamped), got %.2f", manifest.DeclaredMemoryGB)
	}
	// Effective cap should be clamped well below the requested 90% — the real
	// bug this guards: previously a high cap_pct would be taken at face value
	// even when the cluster (specifically the MacBook Pro) had no real headroom.
	if manifest.DeclaredMemoryCapPct >= 0.9 {
		t.Errorf("DeclaredMemoryCapPct: want less than the requested 0.9 (safety clamp should have applied), got %.2f", manifest.DeclaredMemoryCapPct)
	}
	committedGB := manifest.DeclaredMemoryGB * manifest.DeclaredMemoryCapPct
	if committedGB > 25 {
		t.Errorf("committed GB (%.2f) should stay close to the ~23.5 GB safely available, not the ~72 GB the raw 90%% request would imply", committedGB)
	}
}

func TestAssembleManifestNeverClampsAboveRequestedCap(t *testing.T) {
	// The safety logic must only ever REDUCE the operator's chosen cap, never
	// increase it — a cautious 10% request must stay at (or below) 10% even
	// though the cluster could safely offer much more.
	srv := stateServer(t, realClusterState())
	exo := exoadapter.New(srv.URL)
	_, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	opts := capability.DefaultOptions()
	opts.MemoryCapPct = 0.10

	manifest, err := capability.AssembleManifest(context.Background(), exo, pub, opts)
	if err != nil {
		t.Fatalf("AssembleManifest: %v", err)
	}
	if manifest.DeclaredMemoryCapPct > 0.10 {
		t.Errorf("DeclaredMemoryCapPct: want <= 0.10 (never expand beyond the operator's request), got %.2f", manifest.DeclaredMemoryCapPct)
	}
}

// TestAssembleManifestSkipsClampForSimulatedNodes guards a real regression
// caught before it shipped: stub-exo (used by the Docker simulation) reports
// an empty topology, same as a genuine solo node, but simulated nodes ALSO
// set --declared-memory-gb to a fabricated capacity (e.g. 400 GB) that has no
// relationship to the container's real host memory. Without the
// opts.DeclaredMemoryGB guard, the safety clamp would compare that fake
// capacity against governor.AvailableMemoryGB()'s real (tiny, container-host)
// reading and clamp every simulated node's contribution to ~0.
func TestAssembleManifestSkipsClampForSimulatedNodes(t *testing.T) {
	// Mirrors stub-exo's actual /state response exactly: empty topology.
	srv := stateServer(t, map[string]any{
		"node_id": "stub-node",
		"status":  "healthy",
		"topology": map[string]any{
			"peers": []any{},
			"nodes": []any{},
		},
	})
	exo := exoadapter.New(srv.URL)
	_, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	opts := capability.DefaultOptions()
	opts.MemoryCapPct = 0.8
	opts.DeclaredMemoryGB = 400 // simulation override — a real node never has 400GB

	manifest, err := capability.AssembleManifest(context.Background(), exo, pub, opts)
	if err != nil {
		t.Fatalf("AssembleManifest: %v", err)
	}
	if manifest.DeclaredMemoryCapPct != 0.8 {
		t.Errorf("simulated node's DeclaredMemoryCapPct got clamped to %.2f — the safety logic must not apply to --declared-memory-gb overrides, or every simulated node in the Docker demo silently drops to ~0%% committed memory", manifest.DeclaredMemoryCapPct)
	}
}

func TestDetectClusterNodeNoTopologyIsSolo(t *testing.T) {
	srv := stateServer(t, map[string]any{"instances": map[string]any{}})
	exo := exoadapter.New(srv.URL)

	info, err := capability.DetectClusterNode(context.Background(), exo)
	if err != nil {
		t.Fatalf("DetectClusterNode: %v", err)
	}
	if info.IsCluster {
		t.Error("no topology key at all should mean not a cluster")
	}
}
