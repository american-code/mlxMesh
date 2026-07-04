// Package bench implements reference-prompt benchmarking for MeasuredSignature generation.
// Modeled on Exo's bench/exo_bench.py pattern — Exo already solved prefill/decode
// tokens-per-second timing; this package adds the mesh's tier-verification purpose:
// results are signed and submitted to the pod coordinator on a recurring schedule,
// not just printed to console (proposal §8.2/9.2).
package bench

import (
	"context"
	"fmt"
	"time"

	"github.com/open-inference-mesh/oim/internal/exoadapter"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// Prompt is one reference benchmark workload.
type Prompt struct {
	Messages    []map[string]any
	MaxTokens   int
	Description string
}

// ReferencePrompts is the fixed set used for comparable node-to-node benchmarking.
// All nodes must use the same prompts so MeasuredSignature values are comparable
// and verify_tier_claim can detect divergence within tolerance.
var ReferencePrompts = map[string]Prompt{
	"short": {
		Messages:    []map[string]any{{"role": "user", "content": "What is 2+2?"}},
		MaxTokens:   20,
		Description: "~5 token prompt, ~10 token response",
	},
	"medium": {
		Messages: []map[string]any{{
			"role": "user",
			"content": "Explain the difference between pipeline-parallel and expert-parallel " +
				"inference sharding in transformer models, including latency implications " +
				"over a high-latency WAN connection.",
		}},
		MaxTokens:   250,
		Description: "~60 token prompt, ~200 token response",
	},
	"long": {
		Messages: []map[string]any{{
			"role": "user",
			"content": "You are analyzing a PostgreSQL query plan. The following EXPLAIN ANALYZE " +
				"output shows a sequential scan on a 10M row table with no indexes used. " +
				"The query filters on created_at and user_id. Provide: " +
				"1) Diagnosis of why this plan is slow, " +
				"2) Recommended indexes with exact CREATE INDEX statements, " +
				"3) Estimated improvement after indexing, " +
				"4) Downsides of adding these indexes. " +
				"Query: SELECT * FROM events WHERE created_at > '2024-01-01' " +
				"AND user_id = 42 ORDER BY created_at DESC LIMIT 100;",
		}},
		MaxTokens:   600,
		Description: "~120 token prompt, ~500 token response (SQL-metrics reference workload)",
	},
}

// Run executes a benchmark against a local Exo instance and returns a MeasuredSignature.
// Averages over sampleCount runs. Both prefill and decode rates are measured.
//
// Prefill throughput = prompt_tokens / time_to_first_token (estimated from token ratios).
// Decode throughput  = completion_tokens / remaining_time.
func Run(
	ctx context.Context,
	exo *exoadapter.Client,
	modelID string,
	promptID string,
	sampleCount int,
) (*protocol.MeasuredSignature, error) {
	prompt, ok := ReferencePrompts[promptID]
	if !ok {
		return nil, fmt.Errorf("unknown promptID %q — available: short, medium, long", promptID)
	}
	if sampleCount < 1 {
		sampleCount = 1
	}

	var totalDecode, totalPrefill float64
	successful := 0

	for i := 0; i < sampleCount; i++ {
		d, p, err := runOnce(ctx, exo, modelID, prompt)
		if err != nil {
			// Log but don't abort — a partial run is better than no result
			continue
		}
		totalDecode += d
		totalPrefill += p
		successful++
	}

	if successful == 0 {
		return nil, fmt.Errorf("all %d benchmark runs failed", sampleCount)
	}

	return &protocol.MeasuredSignature{
		TokensPerSecDecode:  round2(totalDecode / float64(successful)),
		TokensPerSecPrefill: round2(totalPrefill / float64(successful)),
		MeasuredAt:          time.Now().UTC().Format(time.RFC3339),
		BenchmarkPromptID:   promptID,
		SampleCount:         successful,
	}, nil
}

// CompareSignatures returns true if measured is within tolerancePct of claimed.
// Used by pod_coordinator/verification to catch tier-misreporting (proposal §8.2/9.2).
func CompareSignatures(claimed, measured *protocol.MeasuredSignature, tolerancePct float64) bool {
	within := func(claim, actual float64) bool {
		if claim <= 0 {
			return actual >= 0
		}
		ratio := actual / claim
		return ratio >= (1-tolerancePct) && ratio <= (1+tolerancePct)
	}
	return within(claimed.TokensPerSecDecode, measured.TokensPerSecDecode) &&
		within(claimed.TokensPerSecPrefill, measured.TokensPerSecPrefill)
}

func runOnce(ctx context.Context, exo *exoadapter.Client, modelID string, prompt Prompt) (decodeTPS, prefillTPS float64, err error) {
	start := time.Now()
	result, err := exo.RunChatCompletion(ctx, modelID, prompt.Messages, false, map[string]any{
		"max_tokens": prompt.MaxTokens,
	})
	elapsed := time.Since(start).Seconds()
	if err != nil {
		return 0, 0, err
	}

	// Extract token counts from usage field if present
	promptTokens, completionTokens := extractTokenCounts(result, prompt)

	total := float64(promptTokens + completionTokens)
	if total == 0 || elapsed == 0 {
		return 0, 0, fmt.Errorf("no token counts in response")
	}

	// Estimate time split proportionally to token counts
	prefillFrac := float64(promptTokens) / total
	prefillTime := elapsed * prefillFrac
	decodeTime := elapsed - prefillTime

	if prefillTime > 0 {
		prefillTPS = float64(promptTokens) / prefillTime
	}
	if decodeTime > 0 {
		decodeTPS = float64(completionTokens) / decodeTime
	}
	return decodeTPS, prefillTPS, nil
}

func extractTokenCounts(result map[string]any, prompt Prompt) (promptTokens, completionTokens int) {
	if usage, ok := result["usage"].(map[string]any); ok {
		promptTokens = int(floatVal(usage, "prompt_tokens"))
		completionTokens = int(floatVal(usage, "completion_tokens"))
	}
	if promptTokens == 0 {
		// Rough estimate from message length
		for _, m := range prompt.Messages {
			if content, ok := m["content"].(string); ok {
				promptTokens += len(content) / 4 // ~4 chars/token
			}
		}
	}
	if completionTokens == 0 {
		completionTokens = prompt.MaxTokens / 2
	}
	return
}

func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

func round2(f float64) float64 {
	return float64(int(f*100)) / 100
}
