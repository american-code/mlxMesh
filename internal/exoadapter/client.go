// Package exoadapter is a thin HTTP client wrapping a LOCAL Exo instance.
// Every method maps to a real documented Exo endpoint (exo-explore/exo docs/api.md).
// No inference logic lives here — this is pure translation between this protocol's
// types and Exo's existing HTTP contract.
package exoadapter

import (
	"bufio"
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
// versions (observed live: returns essentially its ENTIRE catalog — ~100 models,
// including ones with zero bytes ever downloaded), so this cross-checks each
// candidate against /state's per-node download progress and only includes a
// model with POSITIVE evidence of a completed download. A node must never
// advertise a model it hasn't actually pulled — that's the exact self-declared-
// vs-measured gap this protocol exists to close (proposal §8.2/9.2), and it
// applies just as much to "do I have this model" as it does to "how fast can I
// run it."
//
// This must be a whitelist (only include confirmed-complete IDs), not a
// blacklist (exclude confirmed-incomplete IDs): confirmed live, a model that
// had NEVER been queued for download at all (zero entries in /state's
// downloads map — the exact state of a model before a user ever triggers a
// pull) has no "incomplete" evidence against it either, so a blacklist
// approach wrongly treated "no evidence" as "downloaded" and advertised a
// model still sitting at 0 of 20.4GB. Whitelisting on an explicit
// "DownloadCompleted" record closes that gap.
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
	completed := completedModelIDs(state)

	out := make([]map[string]any, 0, len(catalog))
	for _, m := range catalog {
		id, _ := m["id"].(string)
		if id == "" || !completed[id] {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// completedModelIDs walks /state's downloads map and returns the set of model
// IDs with at least one "DownloadCompleted" entry on any local node — the
// only positive signal that a model is actually ready to serve. Any other
// status ("DownloadPending", in-progress, or simply absent from the downloads
// map entirely) does NOT count, on purpose: GetDownloadedModels must whitelist
// confirmed-complete models rather than blacklist confirmed-incomplete ones,
// since "absent from this map" describes both "fully downloaded before Exo
// started tracking it" and "never queued at all" identically, and only the
// former should ever be servable.
func completedModelIDs(state map[string]any) map[string]bool {
	completed := make(map[string]bool)
	downloads, _ := state["downloads"].(map[string]any)
	for _, rawEntries := range downloads {
		entries, _ := rawEntries.([]any)
		for _, rawEntry := range entries {
			entry, _ := rawEntry.(map[string]any)
			for status, rawInner := range entry { // single key: status name (e.g. "DownloadPending", "DownloadCompleted")
				if status != "DownloadCompleted" {
					continue
				}
				inner, _ := rawInner.(map[string]any)
				modelID := extractModelID(inner)
				if modelID != "" {
					completed[modelID] = true
				}
			}
		}
	}
	return completed
}

func extractModelID(inner map[string]any) string {
	shardMeta, _ := inner["shardMetadata"].(map[string]any)
	pipelineMeta, _ := shardMeta["PipelineShardMetadata"].(map[string]any)
	modelCard, _ := pipelineMeta["modelCard"].(map[string]any)
	id, _ := modelCard["modelId"].(string)
	return id
}

// GetActiveModels returns the set of model IDs Exo currently has an active
// inference instance for — a strict subset of GetDownloadedModels (a model
// can be downloaded to disk without an instance ever having been created for
// it). Backed by Exo's Ollama-compatibility endpoint (GET /ollama/api/ps,
// "returns list of running models"). The exact field name Exo uses per entry
// isn't documented upstream, so this defensively checks several plausible
// keys (mirrors capability.buildModelList's stringField pattern) rather than
// hard-coding one — confirm against a real Exo instance before trusting this
// blindly in production.
func (c *Client) GetActiveModels(ctx context.Context) (map[string]bool, error) {
	raw, err := c.getJSON(ctx, "/ollama/api/ps")
	if err != nil {
		return nil, err
	}
	active := make(map[string]bool, len(raw))
	for _, m := range raw {
		if id := firstStringField(m, "model_id", "model", "name", "id"); id != "" {
			active[id] = true
		}
	}
	return active, nil
}

// firstStringField returns the first non-empty string value found under any
// of keys, in order. Package-local mirror of capability.stringField (that
// helper is unexported in a different package — exoadapter must not import
// capability, which itself imports exoadapter).
func firstStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, _ := m[k].(string); v != "" {
			return v
		}
	}
	return ""
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

// AwaitInstance blocks until Exo reports the instance for modelID is ready to
// serve, or reports a timeout. POST /instance is documented as asynchronous
// ("wait until the API sees the new instance for this model" before sending
// inference requests) — this wraps the documented poll/await companion,
// GET /instance/await?model_id=..., which streams SSE events and terminates
// with exactly one of two documented types: {"type":"ready",...} or
// {"type":"timeout","message":"..."}. No client-wide timeout is set here
// (mirrors StreamChatCompletion's streamClient) — the caller controls the
// deadline entirely via ctx, since a cold multi-shard model load can
// legitimately take minutes.
func (c *Client) AwaitInstance(ctx context.Context, modelID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/instance/await?model_id="+url.QueryEscape(modelID), nil)
	if err != nil {
		return fmt.Errorf("build await instance request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("await instance: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("await instance HTTP %d: %s", resp.StatusCode, raw)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), "data: ")
		if !ok {
			continue
		}
		var event struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			continue // ignore malformed/unrecognized SSE lines rather than fail the whole await
		}
		switch event.Type {
		case "ready":
			return nil
		case "timeout":
			msg := event.Message
			if msg == "" {
				msg = "no instance became ready before timeout"
			}
			return fmt.Errorf("await instance: %s", msg)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read await instance stream: %w", err)
	}
	return fmt.Errorf("await instance: stream closed before a ready/timeout event")
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

// streamClient has no fixed timeout — a chat completion can run indefinitely;
// callers cancel via ctx, not a client-wide deadline (unlike c.http, used for
// short buffered calls where 30s is a reasonable ceiling).
var streamClient = &http.Client{}

// StreamChatCompletion calls POST /v1/chat/completions with stream:true and
// stream_options.include_usage:true (so the trailing SSE frame carries
// completion_tokens), returning the raw HTTP response for the caller to read
// as text/event-stream — the streaming counterpart to RunChatCompletion, fast
// lane only. The caller owns closing resp.Body.
func (c *Client) StreamChatCompletion(ctx context.Context, modelID string, messages []map[string]any) (*http.Response, error) {
	payload := map[string]any{
		"model":          modelID,
		"messages":       messages,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal stream payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("build stream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stream chat completion: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("stream chat completion HTTP %d: %s", resp.StatusCode, raw)
	}
	return resp, nil
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
