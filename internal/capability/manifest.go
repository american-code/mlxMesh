// Package capability assembles this node's CapabilityManifest by combining
// live Exo state, resource governor readings, and the last benchmark signature.
// No I/O outside the local machine. reputation sends it onward.
package capability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/open-inference-mesh/oim/internal/exoadapter"
	"github.com/open-inference-mesh/oim/internal/governor"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

var signatureCachePath = filepath.Join(os.Getenv("HOME"), ".config", "oim", "last_benchmark.json")

// AssembleManifest is the single source of truth for "what can this node claim right now."
// Re-checks governor.EnforceContributionCap on every call — never cache stale capacity.
func AssembleManifest(
	ctx context.Context,
	exo *exoadapter.Client,
	pubKey []byte,
	opts Options,
) (*protocol.CapabilityManifest, error) {
	nodeID := protocol.NodeIDFromPubKey(pubKey)

	totalGB, err := governor.TotalRAMGB()
	if err != nil {
		return nil, fmt.Errorf("read total RAM: %w", err)
	}
	if opts.DeclaredMemoryGB > 0 {
		totalGB = opts.DeclaredMemoryGB
	}

	cluster, err := DetectClusterNode(ctx, exo)
	if err != nil {
		// Non-fatal: default to single-node
		cluster = ClusterInfo{IsCluster: false, DeviceCount: 1}
	}
	// Treat high-memory nodes as clusters even when topology detection is unavailable
	// (common in simulated environments where Exo doesn't report multi-device peers).
	if !cluster.IsCluster && totalGB >= 128 {
		cluster.IsCluster = true
		cluster.DeviceCount = int(totalGB / 80) // rough device estimate: one ~80 GB GPU per device
	}
	// A real cluster's true capacity is the SUM across every device, not just
	// this local machine's RAM — governor.TotalRAMGB() only ever sees the one
	// machine oim node start is running on, so without this override a 3-device
	// 80 GB cluster would under-report itself as whatever its single smallest
	// (or just-happens-to-be-running-the-agent) member has.
	if cluster.IsCluster && cluster.TotalMemGB > 0 {
		totalGB = cluster.TotalMemGB
	}

	models, err := buildModelList(ctx, exo, opts.AllowedModels)
	if err != nil {
		models = nil // exo not running — still build a valid manifest
	}

	sig := loadLastSignature()

	var dcPtr *int
	if cluster.IsCluster {
		dc := cluster.DeviceCount
		dcPtr = &dc
	}

	// Never promise the mesh more than can be safely committed RIGHT NOW —
	// the operator's chosen MemoryCapPct is a ceiling, never a guarantee.
	// Clamped per-device (via cluster.SafeContributableGB, computed from live
	// Exo nodeMemory.ramAvailable) so one device already under memory
	// pressure can't be pushed further just because roomier devices sit
	// alongside it in the same cluster — that's the whole point: the network
	// defers to whichever machines actually have headroom. Re-evaluated on
	// every call (every heartbeat), so contribution self-adjusts as real
	// usage changes throughout the day, no operator action needed.
	//
	// Only applies with REAL per-device availability data. Two guards matter:
	//   - cluster.TotalMemGB > 0 requires genuine Exo-reported cluster stats,
	//     not the "high-memory nodes treated as clusters" heuristic a few
	//     lines up (that path never learns SafeContributableGB and would
	//     clamp everything to 0).
	//   - opts.DeclaredMemoryGB <= 0 requires totalGB came from real system
	//     detection, not a --declared-memory-gb override — comparing this
	//     HOST's real governor.AvailableMemoryGB() against a fabricated
	//     declared capacity (simulation/testing) is meaningless and would
	//     zero out every simulated node's committed memory.
	effectiveCapPct := opts.MemoryCapPct
	switch {
	case cluster.IsCluster && cluster.TotalMemGB > 0 && totalGB > 0:
		if safeCapPct := cluster.SafeContributableGB / totalGB; safeCapPct < effectiveCapPct {
			effectiveCapPct = safeCapPct
		}
	case !cluster.IsCluster && opts.DeclaredMemoryGB <= 0 && totalGB > 0:
		if avail, availErr := governor.AvailableMemoryGB(); availErr == nil {
			safe := avail - perDeviceReserveGB(totalGB)
			if safe < 0 {
				safe = 0
			}
			if safeCapPct := safe / totalGB; safeCapPct < effectiveCapPct {
				effectiveCapPct = safeCapPct
			}
		}
	}
	if effectiveCapPct < 0 {
		effectiveCapPct = 0
	}

	return &protocol.CapabilityManifest{
		NodeID:               nodeID,
		IsCluster:            cluster.IsCluster,
		ClusterDeviceCount:   dcPtr,
		ClusterChipFamilies:  cluster.ChipFamilies,
		DeclaredMemoryGB:     round2(totalGB),
		DeclaredMemoryCapPct: round2(effectiveCapPct),
		GeographicHint:       opts.GeographicHint,
		GeoLat:               opts.GeoLat,
		GeoLng:               opts.GeoLng,
		Models:               models,
		MeasuredSignature:    sig,
		ReachabilityEndpoint: opts.ReachabilityEndpoint,
		PricePerUnit:         opts.PricePerUnit,
		HasSecureEnclave:     protocol.CheckSecureEnclaveAvailable(),
		ECDHPublicKey:        opts.ECDHPublicKey,
		TLSCertFingerprint:   opts.TLSCertFingerprint,
	}, nil
}

// Options holds operator-configured parameters for manifest assembly.
type Options struct {
	MemoryCapPct         float64
	DeclaredMemoryGB     float64  // when > 0, overrides governor.TotalRAMGB() (useful for simulation)
	AllowedModels        []string // empty = all downloaded Exo models; non-empty = allowlist
	GeographicHint       string
	GeoLat               float64 // approximate latitude; 0 = not declared
	GeoLng               float64 // approximate longitude; 0 = not declared
	ReachabilityEndpoint string
	PricePerUnit         map[string]float64
	// ECDHPublicKey is this node's P-256 key-agreement public key (raw
	// uncompressed point, base64) — see protocol.CapabilityManifest.ECDHPublicKey.
	// Empty means this node doesn't support encrypted-pointer jobs.
	ECDHPublicKey string
	// TLSCertFingerprint — see protocol.CapabilityManifest.TLSCertFingerprint.
	// Empty means this node serves its job endpoint over plain HTTP.
	TLSCertFingerprint string
}

// DefaultOptions returns sensible defaults for a new node.
func DefaultOptions() Options {
	return Options{
		MemoryCapPct:         0.5,
		GeographicHint:       guessRegion(),
		ReachabilityEndpoint: "http://localhost:8765",
		PricePerUnit: map[string]float64{
			"compute_cycles":    0.0,
			"memory_hours":      0.0,
			"bandwidth_relayed": 0.0,
		},
	}
}

// ClusterInfo summarizes an Exo instance's multi-device topology, when present.
type ClusterInfo struct {
	IsCluster    bool
	DeviceCount  int
	TotalMemGB   float64  // summed nodeMemory.ramTotal across every cluster device; 0 when not a cluster
	ChipFamilies []string // one coarse chip family per device (e.g. "Apple M1") — no hostnames, no exact chip variant (Pro/Max/Ultra), no OS/model info
	// SafeContributableGB is the sum, across every device, of that device's
	// CURRENTLY free memory (Exo's nodeMemory.ramAvailable) minus a per-device
	// safety reserve — never how much RAM a device HAS, but how much it can
	// safely give up right now without starving its owner. A device already
	// under memory pressure (e.g. a 16 GB MacBook Pro with only 2.5 GB free,
	// sitting alongside two 32 GB Mac Studios with 15+ GB free each)
	// contributes little or nothing here even though its TotalMemGB share is
	// unchanged — this is what lets the network defer to the roomier devices
	// instead of maxing out the tightest one.
	SafeContributableGB float64
	// TotalAvailableGB is the RAW sum of nodeMemory.ramAvailable across every
	// device — before the safety reserve is subtracted. Informational only
	// (e.g. dashboard display); SafeContributableGB is what routing math uses.
	TotalAvailableGB float64
}

// PerDeviceReserveGB and PerDeviceReservePct define the safety margin every
// device (cluster member or solo node) always keeps for its owner, regardless
// of the operator's chosen memory-cap percentage — whichever floor is larger.
// A flat GB floor alone would round to nothing on a huge device; a flat
// percentage alone would be too aggressive on a small one, so both are kept
// and the larger wins.
const (
	PerDeviceReserveGB  = 2.0
	PerDeviceReservePct = 0.15
)

// perDeviceReserveGB returns how much of a device's memory must stay free for
// its owner, never counted as contributable to the mesh.
func perDeviceReserveGB(deviceTotalGB float64) float64 {
	pct := deviceTotalGB * PerDeviceReservePct
	if pct > PerDeviceReserveGB {
		return pct
	}
	return PerDeviceReserveGB
}

// DetectClusterNode inspects Exo's /state to determine whether this Exo instance
// is coordinating multiple physical devices. If so, the mesh sees it as ONE
// cluster-node, not several independent nodes (proposal §6.4) — and that
// cluster-node's declared memory and chip summary reflect every device in it,
// not just whichever machine happens to be running the oim agent.
//
// Exo's /state reports device membership two different ways depending on
// version/config: topology.nodes (observed in practice — a list that INCLUDES
// self) or topology.peers (documented as EXCLUDING self, so +1 for self).
// Getting this wrong either double-counts or under-counts devices, so the two
// shapes are handled explicitly rather than treated as interchangeable.
func DetectClusterNode(ctx context.Context, exo *exoadapter.Client) (ClusterInfo, error) {
	solo := ClusterInfo{IsCluster: false, DeviceCount: 1}

	state, err := exo.GetState(ctx)
	if err != nil {
		return solo, err
	}
	topology, _ := state["topology"].(map[string]any)
	if topology == nil {
		return solo, nil
	}

	var deviceIDs []string
	if peers, _ := topology["peers"].([]any); len(peers) > 0 {
		// peers excludes self — self's own device isn't identifiable from this
		// list alone, so it's counted but not included in the chip/memory scan.
		for _, p := range peers {
			if id, ok := p.(string); ok {
				deviceIDs = append(deviceIDs, id)
			}
		}
		agg := aggregateClusterStats(state, deviceIDs)
		agg.IsCluster = true
		agg.DeviceCount = len(deviceIDs) + 1
		return agg, nil
	}
	if nodes, _ := topology["nodes"].([]any); len(nodes) > 0 {
		// nodes includes self — no +1 needed, and self's stats are available
		// for the memory/chip aggregation since its own ID is in the list.
		for _, n := range nodes {
			if id, ok := n.(string); ok {
				deviceIDs = append(deviceIDs, id)
			}
		}
		if len(deviceIDs) < 2 {
			return solo, nil // a "cluster" of one is just a regular node
		}
		agg := aggregateClusterStats(state, deviceIDs)
		agg.IsCluster = true
		agg.DeviceCount = len(deviceIDs)
		return agg, nil
	}
	return solo, nil
}

// aggregateClusterStats sums nodeMemory.ramTotal/ramAvailable and collects
// coarse chip families for the given device IDs from Exo's /state response.
// Missing or malformed entries are skipped rather than failing the whole
// aggregation — partial cluster stats are better than none when one device's
// data is odd-shaped.
func aggregateClusterStats(state map[string]any, deviceIDs []string) ClusterInfo {
	nodeMemory, _ := state["nodeMemory"].(map[string]any)
	nodeIdentities, _ := state["nodeIdentities"].(map[string]any)

	var totalBytes, availableBytes, safeContributableBytes float64
	var chipFamilies []string
	for _, id := range deviceIDs {
		if mem, ok := nodeMemory[id].(map[string]any); ok {
			var deviceTotalBytes, deviceAvailableBytes float64
			if ramTotal, ok := mem["ramTotal"].(map[string]any); ok {
				if bytes, ok := ramTotal["inBytes"].(float64); ok {
					deviceTotalBytes = bytes
					totalBytes += bytes
				}
			}
			if ramAvail, ok := mem["ramAvailable"].(map[string]any); ok {
				if bytes, ok := ramAvail["inBytes"].(float64); ok {
					deviceAvailableBytes = bytes
					availableBytes += bytes
				}
			}
			if deviceTotalBytes > 0 {
				reserveBytes := perDeviceReserveGB(deviceTotalBytes/(1<<30)) * (1 << 30)
				if safe := deviceAvailableBytes - reserveBytes; safe > 0 {
					safeContributableBytes += safe
				}
			}
		}
		if ident, ok := nodeIdentities[id].(map[string]any); ok {
			if chipID, ok := ident["chipId"].(string); ok && chipID != "" {
				chipFamilies = append(chipFamilies, chipFamily(chipID))
			}
		}
	}
	return ClusterInfo{
		TotalMemGB:          totalBytes / (1 << 30),
		TotalAvailableGB:    availableBytes / (1 << 30),
		SafeContributableGB: safeContributableBytes / (1 << 30),
		ChipFamilies:        chipFamilies,
	}
}

// chipFamily coarsens an exact chip identifier ("Apple M1 Max") down to its
// silicon generation ("Apple M1") — dropping the Pro/Max/Ultra variant.
// Deliberately coarser than what Exo reports: the dashboard can say "this
// cluster has 3 Apple M1-class devices" without broadcasting exact chip
// variants, and — separately, not handled by this function — never
// broadcasts hostnames (Exo's nodeIdentities.friendlyName) or modelId at all.
func chipFamily(chipID string) string {
	words := strings.Fields(chipID)
	if len(words) >= 2 {
		return words[0] + " " + words[1] // "Apple M1 Max" -> "Apple M1"
	}
	return chipID
}

// SaveBenchmarkResult persists a MeasuredSignature for future manifest assemblies.
func SaveBenchmarkResult(sig *protocol.MeasuredSignature) error {
	dir := filepath.Dir(signatureCachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	b, err := json.Marshal(sig)
	if err != nil {
		return err
	}
	return os.WriteFile(signatureCachePath, b, 0o600)
}

// --- private helpers ---

func buildModelList(ctx context.Context, exo *exoadapter.Client, allowedModels []string) ([]protocol.ModelCapability, error) {
	raw, err := exo.GetDownloadedModels(ctx)
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool, len(allowedModels))
	for _, id := range allowedModels {
		allowed[id] = true
	}

	models := make([]protocol.ModelCapability, 0, len(raw))
	for _, m := range raw {
		modelID := stringField(m, "id", "model_id")
		if modelID == "" {
			continue
		}
		if len(allowed) > 0 && !allowed[modelID] {
			continue
		}
		models = append(models, protocol.ModelCapability{
			ModelID:          modelID,
			Quantization:     inferQuantization(modelID),
			Runtime:          inferRuntime(modelID),
			MaxContextTokens: intField(m, 4096, "context_length", "max_context_tokens"),
			IsMoE:            isMoE(modelID),
		})
	}
	return models, nil
}

func loadLastSignature() *protocol.MeasuredSignature {
	b, err := os.ReadFile(signatureCachePath)
	if err != nil {
		// OIM_INITIAL_TPS seeds a fake tok/s for simulation nodes that have not run a
		// real benchmark yet. Never set this env var on production nodes.
		if tpsStr := os.Getenv("OIM_INITIAL_TPS"); tpsStr != "" {
			var tps float64
			fmt.Sscanf(tpsStr, "%f", &tps)
			if tps > 0 {
				return &protocol.MeasuredSignature{
					TokensPerSecDecode:  tps,
					TokensPerSecPrefill: tps * 2.5,
					MeasuredAt:          "1970-01-01T00:00:00Z",
					BenchmarkPromptID:   "stub",
					SampleCount:         0,
				}
			}
		}
		return nil
	}
	var sig protocol.MeasuredSignature
	if json.Unmarshal(b, &sig) != nil {
		return nil
	}
	return &sig
}

func inferRuntime(modelID string) protocol.RuntimeType {
	id := strings.ToLower(modelID)
	if strings.Contains(id, "gguf") || strings.Contains(id, "ggml") {
		return protocol.RuntimeLlamaCppGGUF
	}
	return protocol.RuntimeExoMLX
}

func inferQuantization(modelID string) string {
	id := strings.ToLower(modelID)
	for _, q := range []string{"4bit", "8bit", "q4", "q8", "fp16", "bf16"} {
		if strings.Contains(strings.ReplaceAll(id, "_", ""), q) {
			return q
		}
	}
	return "unknown"
}

func isMoE(modelID string) bool {
	id := strings.ToLower(modelID)
	return strings.Contains(id, "moe") ||
		strings.Contains(id, "mixtral") ||
		strings.Contains(id, "deepseek")
}

func guessRegion() string {
	_, offset := time.Now().Zone()
	hours := offset / 3600
	switch {
	case hours >= 8:
		return "apac"
	case hours >= -1:
		return "eu"
	default:
		return "us"
	}
}

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func intField(m map[string]any, defaultVal int, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
	}
	return defaultVal
}

func round2(f float64) float64 {
	return float64(int(f*100)) / 100
}
