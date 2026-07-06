// Package sse holds tiny helpers shared by every SSE relay in the mesh
// (Exo -> node -> coordinator -> client, all three hops using the same
// line-by-line passthrough pattern).
package sse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// SetHeaders sets the standard response headers every SSE relay in the mesh
// uses. Callers that need additional headers (e.g. the coordinator's
// X-OIM-Served-By-Node-Id) must set those BEFORE calling SetHeaders — Go locks
// in the header set on the first Write, and Relay is typically the first
// writer.
func SetHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

// Relay reads Server-Sent Events line-by-line from src and writes each line to
// dst as it arrives, flushing after the blank line that terminates each SSE
// event. Shared mechanics behind both hops of the mesh's fast-lane streaming
// relay (Exo -> node, node -> coordinator) — previously duplicated verbatim in
// each hop's own package.
//
// Returns whether any line was relayed before src was exhausted or errored
// (started), and the completion-token count read from the trailing usage
// frame via ExtractUsageTokens (tokens) — the credit-accounting signal for a
// streamed response, in place of the buffered path's single JSON blob.
func Relay(dst http.ResponseWriter, src io.Reader) (started bool, tokens int, err error) {
	flusher, ok := dst.(http.Flusher)
	if !ok {
		return false, 0, fmt.Errorf("streaming not supported by response writer")
	}
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		started = true
		fmt.Fprintf(dst, "%s\n", line)
		if strings.TrimSpace(line) == "" {
			flusher.Flush() // blank line terminates one SSE event
		}
		if n := ExtractUsageTokens(line); n > 0 {
			tokens = n
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		return started, tokens, scanErr
	}
	return started, tokens, nil
}
