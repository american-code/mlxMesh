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

	isCluster, deviceCount, err := DetectClusterNode(ctx, exo)
	if err != nil {
		// Non-fatal: default to single-node
		isCluster = false
	}

	models, err := buildModelList(ctx, exo)
	if err != nil {
		models = nil // exo not running — still build a valid manifest
	}

	sig := loadLastSignature()

	var dcPtr *int
	if isCluster {
		dcPtr = &deviceCount
	}

	return &protocol.CapabilityManifest{
		NodeID:               nodeID,
		IsCluster:            isCluster,
		ClusterDeviceCount:   dcPtr,
		DeclaredMemoryGB:     round2(totalGB),
		DeclaredMemoryCapPct: opts.MemoryCapPct,
		GeographicHint:       opts.GeographicHint,
		Models:               models,
		MeasuredSignature:    sig,
		ReachabilityEndpoint: opts.ReachabilityEndpoint,
		PricePerUnit:         opts.PricePerUnit,
	}, nil
}

// Options holds operator-configured parameters for manifest assembly.
type Options struct {
	MemoryCapPct         float64
	GeographicHint       string
	ReachabilityEndpoint string
	PricePerUnit         map[string]float64
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

// DetectClusterNode inspects Exo's /state to determine whether this Exo instance
// is coordinating multiple physical devices. If so, the mesh sees it as ONE
// cluster-node, not several independent nodes (proposal §6.4).
func DetectClusterNode(ctx context.Context, exo *exoadapter.Client) (bool, int, error) {
	state, err := exo.GetState(ctx)
	if err != nil {
		return false, 1, err
	}
	topology, _ := state["topology"].(map[string]any)
	if topology == nil {
		return false, 1, nil
	}
	// Exo reports peer nodes in the topology; peers excludes self.
	peers, _ := topology["peers"].([]any)
	if len(peers) == 0 {
		peers, _ = topology["nodes"].([]any)
	}
	if len(peers) > 0 {
		return true, len(peers) + 1, nil
	}
	return false, 1, nil
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

func buildModelList(ctx context.Context, exo *exoadapter.Client) ([]protocol.ModelCapability, error) {
	raw, err := exo.GetDownloadedModels(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]protocol.ModelCapability, 0, len(raw))
	for _, m := range raw {
		modelID := stringField(m, "id", "model_id")
		if modelID == "" {
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
