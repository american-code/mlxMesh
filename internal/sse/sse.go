// Package sse holds tiny helpers shared by every SSE relay in the mesh
// (Exo -> node -> coordinator -> client, all three hops using the same
// line-by-line passthrough pattern).
package sse

import (
	"encoding/json"
	"strings"
)

// ExtractUsageTokens parses one raw SSE line and returns
// usage.completion_tokens if this line is a `data: {...}` frame carrying one,
// else 0. Used by both the node (reading Exo's stream) and the coordinator
// (reading a node's stream) to recover the credit-accounting signal from a
// byte-for-byte passthrough relay.
func ExtractUsageTokens(line string) int {
	const prefix = "data: "
	if !strings.HasPrefix(line, prefix) {
		return 0
	}
	payload := strings.TrimPrefix(line, prefix)
	if payload == "[DONE]" {
		return 0
	}
	var frame struct {
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(payload), &frame); err != nil {
		return 0
	}
	return frame.Usage.CompletionTokens
}
