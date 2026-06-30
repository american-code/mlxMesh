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

// GetDownloadedModels calls GET /models?status=downloaded.
// Used by capability.go to auto-populate ModelCapability entries from what's
// actually present, not what the operator hand-declares.
func (c *Client) GetDownloadedModels(ctx context.Context) ([]map[string]any, error) {
	return c.getJSON(ctx, "/models?status=downloaded")
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
	var result []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return result, nil
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
