// Package coordinator — document splitting for long-context background-lane jobs.
//
// Splits an input payload at semantically meaningful boundaries so that each half
// can be processed in parallel on separate nodes within the same pod. The splits
// are then recombined by the result_merger after parallel execution.
//
// IMPORTANT: This module must never be called for fast-lane jobs. Autoregressive
// generation cannot be parallelised mid-sequence — token N depends on tokens 1…N-1.
// The caller (RouteWithDecomposition) enforces this. Any path that reaches
// SplitDocument with lane="fast" is a bug.
package coordinator

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrNotImplemented is returned by stub functions that require capabilities not yet
// available in this build (ML model inference, external content-store integration).
// Callers that receive ErrNotImplemented must fall back to single-node routing —
// never propagate it to the end requester as an inference failure.
var ErrNotImplemented = errors.New("stub: not yet implemented — see function documentation for requirements")

// SplitStrategy controls where document splitting occurs.
// Ordered from most-preferred (semantically cleanest) to least-preferred.
// SplitFixedTokenCount is a last resort — it produces lower-quality splits
// because it does not respect semantic boundaries.
type SplitStrategy string

const (
	SplitParagraphBoundary SplitStrategy = "paragraph_boundary"
	SplitSQLStatement      SplitStrategy = "sql_statement"
	SplitLogEntry          SplitStrategy = "log_entry"
	SplitSemanticSentence  SplitStrategy = "semantic_sentence" // fallback for unstructured prose
	SplitFixedTokenCount   SplitStrategy = "fixed_token_count" // last resort; avoid
)

// DocumentSplit is one output segment of a split long-context payload.
// Position preserves ordering for the merger — reconstruction is always
// left-then-right, never in arrival order.
type DocumentSplit struct {
	SplitID           string        `json:"split_id"`
	ParentJobID       string        `json:"parent_job_id"`
	Content           string        `json:"content"`     // actual content slice for in-process routing
	ContentRef        string        `json:"content_ref"` // external-storage reference; populated when content store is integrated
	Position          string        `json:"position"`    // "left" | "right" | "middle"
	TokenEstimate     int           `json:"token_estimate"`
	SplitStrategyUsed SplitStrategy `json:"split_strategy_used"`
	Checksum          string        `json:"checksum"` // SHA-256(content) — used by ParallelVerifier
}

// DetectSplitStrategy infers the appropriate split strategy from content structure.
// modelID is reserved for future tokenizer-aware boundary detection; the current
// implementation uses lexical heuristics only.
//
// Heuristic order (first match wins):
//  1. SQL keywords at statement-start positions → SplitSQLStatement
//  2. Structured log level markers (INFO/WARN/ERROR) → SplitLogEntry
//  3. Double newlines (paragraph breaks) → SplitParagraphBoundary
//  4. Everything else → SplitSemanticSentence
func DetectSplitStrategy(content, _ string) SplitStrategy {
	// Inspect only the first 512 bytes to keep detection O(1).
	prefix := content
	if len(prefix) > 512 {
		prefix = content[:512]
	}
	upper := strings.ToUpper(prefix)
	if strings.Contains(upper, "SELECT ") || strings.Contains(upper, "INSERT ") ||
		strings.Contains(upper, "UPDATE ") || strings.Contains(upper, "CREATE TABLE") ||
		strings.Contains(upper, "ALTER TABLE") || strings.Contains(upper, "DROP ") {
		return SplitSQLStatement
	}
	if strings.Contains(prefix, " ERROR ") || strings.Contains(prefix, " WARN ") ||
		strings.Contains(prefix, " INFO ") || strings.Contains(prefix, " DEBUG ") {
		return SplitLogEntry
	}
	if strings.Contains(content, "\n\n") {
		return SplitParagraphBoundary
	}
	return SplitSemanticSentence
}

// FindMidpoint returns the character offset of the best split point closest to
// the exact center of content, respecting the given strategy's boundary rules.
// "Closest to center" is critical — a semantically perfect boundary at 10% from
// the end produces a heavily imbalanced split that defeats the parallelism gain.
func FindMidpoint(content string, strategy SplitStrategy) int {
	mid := len(content) / 2
	if mid == 0 {
		return 0
	}
	switch strategy {
	case SplitSQLStatement:
		return nearestSQLBoundary(content, mid)
	case SplitLogEntry:
		return nearestLineBoundary(content, mid)
	case SplitParagraphBoundary:
		return nearestParagraphBoundary(content, mid)
	default: // SplitSemanticSentence, SplitFixedTokenCount
		return nearestSentenceBoundary(content, mid)
	}
}

// nearestSQLBoundary finds the ';' character nearest to mid, returning the
// offset immediately after it so each split contains complete statements.
func nearestSQLBoundary(content string, mid int) int {
	n := len(content)
	for delta := 0; delta < n/2; delta++ {
		if i := mid + delta; i < n && content[i] == ';' {
			return i + 1
		}
		if i := mid - delta; i >= 0 && content[i] == ';' {
			return i + 1
		}
	}
	return mid
}

// nearestLineBoundary finds the '\n' nearest to mid.
func nearestLineBoundary(content string, mid int) int {
	n := len(content)
	for delta := 0; delta < n/2; delta++ {
		if i := mid + delta; i < n && content[i] == '\n' {
			return i + 1
		}
		if i := mid - delta; i >= 0 && content[i] == '\n' {
			return i + 1
		}
	}
	return mid
}

// nearestParagraphBoundary finds the "\n\n" sequence nearest to mid.
func nearestParagraphBoundary(content string, mid int) int {
	n := len(content)
	for delta := 0; delta < n/2-1; delta++ {
		if i := mid + delta; i+1 < n && content[i] == '\n' && content[i+1] == '\n' {
			return i + 2
		}
		if i := mid - delta; i >= 0 && i+1 < n && content[i] == '\n' && content[i+1] == '\n' {
			return i + 2
		}
	}
	return nearestLineBoundary(content, mid) // degrade to line boundary
}

// nearestSentenceBoundary finds '. ', '! ', or '? ' nearest to mid.
func nearestSentenceBoundary(content string, mid int) int {
	n := len(content)
	for delta := 0; delta < n/2-1; delta++ {
		for _, i := range [2]int{mid + delta, mid - delta} {
			if i < 1 || i+1 >= n {
				continue
			}
			if (content[i] == '.' || content[i] == '!' || content[i] == '?') && content[i+1] == ' ' {
				return i + 2
			}
		}
	}
	return mid
}

// splitChecksum returns a stable SHA-256 hex digest of content.
// Deterministic across identical inputs on any node.
func splitChecksum(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// estimateTokens approximates token count from character count.
// English text averages ~4 chars/token. Used for work-distribution planning only —
// not for billing or quota enforcement.
func estimateTokens(content string) int {
	n := len([]rune(content)) // rune count, not byte count, for Unicode correctness
	if n == 0 {
		return 0
	}
	est := n / 4
	if est < 1 {
		est = 1
	}
	return est
}

// SplitDocument splits content into maxSplits DocumentSplit values at semantically
// meaningful boundaries. maxSplits must be 2 — multi-way splitting is not validated
// for coordination overhead until Milestone 2 pilot data is available.
//
// Callers must verify job.Lane == JobLaneBackground before calling this function.
// Calling SplitDocument on a fast-lane job is a bug; see package-level comment.
func SplitDocument(content, parentJobID, modelID string, maxSplits int) ([]DocumentSplit, error) {
	if maxSplits != 2 {
		return nil, fmt.Errorf(
			"SplitDocument: only maxSplits=2 is supported (got %d); "+
				"multi-way splitting requires Milestone 2 overhead profiling before enabling",
			maxSplits,
		)
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("SplitDocument: empty content")
	}
	if len(content) < 64 {
		return nil, fmt.Errorf(
			"SplitDocument: content too short to split meaningfully (%d chars); "+
				"route as a single-node job instead",
			len(content),
		)
	}

	strategy := DetectSplitStrategy(content, modelID)
	mid := FindMidpoint(content, strategy)
	if mid <= 0 || mid >= len(content) {
		mid = len(content) / 2
	}

	left := content[:mid]
	right := content[mid:]

	return []DocumentSplit{
		{
			SplitID:           fmt.Sprintf("%s-split-left", parentJobID),
			ParentJobID:       parentJobID,
			Content:           left,
			ContentRef:        fmt.Sprintf("mem://%s/left", parentJobID),
			Position:          "left",
			TokenEstimate:     estimateTokens(left),
			SplitStrategyUsed: strategy,
			Checksum:          splitChecksum(left),
		},
		{
			SplitID:           fmt.Sprintf("%s-split-right", parentJobID),
			ParentJobID:       parentJobID,
			Content:           right,
			ContentRef:        fmt.Sprintf("mem://%s/right", parentJobID),
			Position:          "right",
			TokenEstimate:     estimateTokens(right),
			SplitStrategyUsed: strategy,
			Checksum:          splitChecksum(right),
		},
	}, nil
}

// EstimateParallelismGain returns the projected wall-clock speedup ratio from
// processing splits in parallel vs. the full document sequentially on one node.
// Returns 1.0 when splits are empty or nodeToksPerSec is zero.
// Callers should skip splitting when the returned ratio is below 1.1 —
// less than 10% speedup does not justify the coordination overhead.
//
// The 200-token coordination overhead constant accounts for the routing round-trip
// and the merge inference call. Tune against Milestone 2 pilot data.
func EstimateParallelismGain(splits []DocumentSplit, nodeToksPerSec float64) float64 {
	if len(splits) == 0 || nodeToksPerSec <= 0 {
		return 1.0
	}
	totalTokens := 0
	maxSplitTokens := 0
	for _, s := range splits {
		totalTokens += s.TokenEstimate
		if s.TokenEstimate > maxSplitTokens {
			maxSplitTokens = s.TokenEstimate
		}
	}
	if maxSplitTokens == 0 {
		return 1.0
	}
	const coordinationOverheadTokens = 200
	parallelTime := float64(maxSplitTokens+coordinationOverheadTokens) / nodeToksPerSec
	sequentialTime := float64(totalTokens) / nodeToksPerSec
	if parallelTime <= 0 {
		return 1.0
	}
	return sequentialTime / parallelTime
}
