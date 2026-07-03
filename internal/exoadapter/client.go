// Package exoadapter is a thin HTTP client wrapping a LOCAL Exo instance.
// Every method maps to a real documented Exo endpoint (exo-explore/exo docs/api.md).
// No inference logic lives here — this is pure translation between this protocol's
// types and Exo's existing HTTP contract.
package exoadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultURL = "http://localhost:52415"

// Client wraps a local Exo instance's HTTP API.
type Client struct {
	baseURL string
	http    *http.Client
}

// New creates a Client. baseURL defaults to DefaultURL if empty.
func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// IsHealthy performs a quick liveness check against /state.
func (c *Client) IsHealthy(ctx context.Context) bool {
	_, err := c.GetState(ctx)
	return err == nil
}

// GetDownloadedModels returns models that are actually present on disk and ready
// to serve right now — not Exo's full catalog of models it merely knows how to fetch.
// Used by capability.go to auto-populate ModelCapability entries from what's
// actually present, not what the operator hand-declares.
//
// Exo's ?downloaded=true query param does not reliably filter server-side across
// versions (observed: returns the full catalog unfiltered), so this cross-checks
// each candidate against /state's per-node download progress and excludes anything
// that isn't fully downloaded. A node must never advertise a model it hasn't
// actually pulled — that's the exact self-declared-vs-measured gap this protocol
// exists to close (proposal §8.2/9.2), and it applies just as much to "do I have
// this model" as it does to "how fast can I run it."
func (c *Client) GetDownloadedModels(ctx context.Context) ([]map[string]any, error) {
	catalog, err := c.getJSON(ctx, "/models?downloaded=true")
	if err != nil {
		return nil, err
	}
	state, err := c.GetState(ctx)
	if err != nil {
		// Can't cross-check download progress — fail closed (advertise nothing)
		// rather than trust a catalog endpoint that may include undownloaded models.
		return nil, fmt.Errorf("get state for download cross-check: %w", err)
	}
	incomplete := incompleteModelIDs(state)

	out := make([]map[string]any, 0, len(catalog))
	for _, m := range catalog {
		id, _ := m["id"].(string)
		if id == "" || incomplete[id] {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// incompleteModelIDs walks /state's downloads map and returns the set of model IDs
// that have an in-progress or not-yet-started download on any local node
// (downloaded bytes < total bytes). Models absent from this set are either fully
// downloaded or were never queued — GetDownloadedModels treats "not incomplete" as
// the closest available signal to "ready to serve" without hand-parsing every
// possible Exo download-state enum value.
//
// A "DownloadCompleted" entry is trusted directly from its status key rather
// than compared by bytes: Exo omits the "downloaded" progress field entirely
// once a download finishes (only "total" remains), so byte-comparing it
// against the missing field (read as 0) would always read as 0 < total —
// misclassifying every genuinely-downloaded model as incomplete and
// silently emptying a node's whole servable-model list. Confirmed live: a
// fully downloaded Qwen model was excluded this way before this fix.
func incompleteModelIDs(state map[string]any) map[string]bool {
	incomplete := make(map[string]bool)
	downloads, _ := state["downloads"].(map[string]any)
	for _, rawEntries := range downloads {
		entries, _ := rawEntries.([]any)
		for _, rawEntry := range entries {
			entry, _ := rawEntry.(map[string]any)
			for status, rawInner := range entry { // single key: status name (e.g. "DownloadPending", "DownloadCompleted")
				inner, _ := rawInner.(map[string]any)
				modelID := extractModelID(inner)
				if modelID == "" {
					continue
				}
				if status == "DownloadCompleted" {
					continue
				}
				downloadedBytes := extractBytes(inner, "downloaded")
				totalBytes := extractBytes(inner, "total")
				if totalBytes <= 0 || downloadedBytes < totalBytes {
					incomplete[modelID] = true
				}
			}
		}
	}
	return incomplete
}

func extractModelID(inner map[string]any) string {
	shardMeta, _ := inner["shardMetadata"].(map[string]any)
	pipelineMeta, _ := shardMeta["PipelineShardMetadata"].(map[string]any)
	modelCard, _ := pipelineMeta["modelCard"].(map[string]any)
	id, _ := modelCard["modelId"].(string)
	return id
}

func extractBytes(inner map[string]any, key string) float64 {
	obj, _ := inner[key].(map[string]any)
	n, _ := obj["inBytes"].(float64)
	return n
}

// GetAllModels calls GET /models.
func (c *Client) GetAllModels(ctx context.Context) ([]map[string]any, error) {
	return c.getJSON(ctx, "/models")
}

// SearchModels calls GET /models/search?query=...
func (c *Client) SearchModels(ctx context.Context, query string) ([]map[string]any, error) {
	return c.getJSON(ctx, "/models/search?query="+url.QueryEscape(query))
}

// GetState calls GET /state.
// Always call this before claiming availability — never serve stale cached state.
func (c *Client) GetState(ctx context.Context) (map[string]any, error) {
	return c.getJSONObject(ctx, "/state")
}

// PreviewInstancePlacements calls GET /instance/previews?model_id=...
// Returns Exo's placement previews (memory_delta_by_node, sharding type, etc.).
// Use this to confirm a job fits before calling CreateInstance.
func (c *Client) PreviewInstancePlacements(ctx context.Context, modelID string) ([]map[string]any, error) {
	return c.getJSON(ctx, "/instance/previews?model_id="+url.QueryEscape(modelID))
}

// CreateInstance calls POST /instance.
// placement must come from PreviewInstancePlacements output — never hand-construct one.
// Returns the command_id.
func (c *Client) CreateInstance(ctx context.Context, placement map[string]any) (string, error) {
	body := map[string]any{"instance": placement}
	resp, err := c.postJSON(ctx, "/instance", body)
	if err != nil {
		return "", err
	}
	if id, ok := resp["command_id"].(string); ok && id != "" {
		return id, nil
	}
	if id, ok := resp["id"].(string); ok {
		return id, nil
	}
	return "", fmt.Errorf("create_instance: no id in response: %v", resp)
}

// DeleteInstance calls DELETE /instance/{instanceID}.
func (c *Client) DeleteInstance(ctx context.Context, instanceID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
		c.baseURL+"/instance/"+instanceID, nil)
	if err != nil {
		return fmt.Errorf("delete_instance build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete_instance: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("delete_instance HTTP %d: %s", resp.StatusCode, b)
	}
	return nil
}

// RunChatCompletion calls POST /v1/chat/completions (OpenAI-compatible).
// job_runner calls this to execute an assigned job.
// Raises immediately on failure — retry policy lives at the coordinator level.
func (c *Client) RunChatCompletion(
	ctx context.Context,
	modelID string,
	messages []map[string]any,
	stream bool,
	extra map[string]any,
) (map[string]any, error) {
	payload := map[string]any{
		"model":    modelID,
		"messages": messages,
		"stream":   stream,
	}
	for k, v := range extra {
		payload[k] = v
	}
	return c.postJSON(ctx, "/v1/chat/completions", payload)
}

// --- HTTP helpers ---

// getJSON handles both list response shapes Exo uses across its endpoints:
// a bare JSON array (e.g. /models/search), and an OpenAI-style wrapper
// {"object":"list","data":[...]} (e.g. /models, /v1/models). Guessing wrong on
// either shape previously caused GetDownloadedModels to silently fail and every
// node to register with models:null.
func (c *Client) getJSON(ctx context.Context, path string) ([]map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s HTTP %d: %s", path, resp.StatusCode, b)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var bare []map[string]any
	if err := json.Unmarshal(raw, &bare); err == nil {
		return bare, nil
	}
	// Not a bare array — Exo wraps list responses under a different key per
	// endpoint ("data" for /models, "previews" for /instance/previews, and
	// possibly others not yet observed). Confirmed live: hardcoding "data"
	// silently returned an empty list for /instance/previews, which blocked
	// ever creating an Exo instance for a real, fully-downloaded model.
	// Rather than hardcode every key by name, take the first array-of-objects
	// field in the response object — every real shape seen so far has
	// exactly one, so this is safe; if a response ever had two, this would
	// pick whichever map iteration happens to visit first (undefined order).
	var wrapped map[string]any
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, fmt.Errorf("decode %s (not a JSON object or array): %w", path, err)
	}
	for _, v := range wrapped {
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		out := make([]map[string]any, 0, len(arr))
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("decode %s: no array field found in object response", path)
}

func (c *Client) getJSONObject(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s HTTP %d: %s", path, resp.StatusCode, b)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return result, nil
}

func (c *Client) postJSON(ctx context.Context, path string, body any) (map[string]any, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body for %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		rb, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("POST %s HTTP %d: %s", path, resp.StatusCode, rb)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response for %s: %w", path, err)
	}
	return result, nil
}
