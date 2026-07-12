package exoadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetActiveModels_ParsesWrappedShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ollama/api/ps" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"model":"llama-3.2-3b"},{"name":"qwen-7b"}]}`)
	}))
	defer srv.Close()

	c := New(srv.URL)
	active, err := c.GetActiveModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !active["llama-3.2-3b"] {
		t.Error("expected llama-3.2-3b to be active (parsed from \"model\" key)")
	}
	if !active["qwen-7b"] {
		t.Error("expected qwen-7b to be active (parsed from \"name\" key)")
	}
	if len(active) != 2 {
		t.Errorf("expected exactly 2 active models, got %d", len(active))
	}
}

func TestGetActiveModels_EmptyWhenNothingLoaded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[]}`)
	}))
	defer srv.Close()

	active, err := New(srv.URL).GetActiveModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(active) != 0 {
		t.Errorf("expected no active models, got %v", active)
	}
}

func TestGetActiveModels_HTTPErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "boom")
	}))
	defer srv.Close()

	if _, err := New(srv.URL).GetActiveModels(context.Background()); err == nil {
		t.Error("expected an error from a 500 response")
	}
}

func TestAwaitInstance_Ready(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("model_id"); got != "llama-3.2-3b" {
			t.Errorf("expected model_id=llama-3.2-3b, got %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"ready\",\"instance\":{\"model_id\":\"llama-3.2-3b\"}}\n\n")
	}))
	defer srv.Close()

	if err := New(srv.URL).AwaitInstance(context.Background(), "llama-3.2-3b"); err != nil {
		t.Errorf("expected nil error on a ready event, got: %v", err)
	}
}

func TestAwaitInstance_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"timeout\",\"message\":\"no instance found for model x\"}\n\n")
	}))
	defer srv.Close()

	err := New(srv.URL).AwaitInstance(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error on a timeout event")
	}
	if err.Error() != "await instance: no instance found for model x" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestAwaitInstance_StreamClosedWithoutTerminalEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Closes immediately with no data at all — must not be mistaken for success.
	}))
	defer srv.Close()

	if err := New(srv.URL).AwaitInstance(context.Background(), "x"); err == nil {
		t.Error("expected an error when the stream closes without a ready/timeout event")
	}
}

// downloadEntry builds one /state downloads entry in Exo's real shape:
// {"<Status>": {"shardMetadata": {"PipelineShardMetadata": {"modelCard": {"modelId": "..."}}}}}
func downloadEntry(status, modelID string) map[string]any {
	return map[string]any{
		status: map[string]any{
			"shardMetadata": map[string]any{
				"PipelineShardMetadata": map[string]any{
					"modelCard": map[string]any{"modelId": modelID},
				},
			},
		},
	}
}

func fakeExoServer(t *testing.T, catalogIDs []string, downloads map[string][]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			data := make([]map[string]any, 0, len(catalogIDs))
			for _, id := range catalogIDs {
				data = append(data, map[string]any{"id": id})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
		case "/state":
			json.NewEncoder(w).Encode(map[string]any{"downloads": downloads})
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	}))
}

// TestGetDownloadedModels_ExcludesModelWithNoDownloadRecordAtAll reproduces
// the exact live bug found this session: a model can appear in Exo's
// /models?downloaded=true catalog (which returns essentially everything)
// while having ZERO entries in /state's downloads map — the state of a model
// that was never queued for download at all. The old blacklist-only logic
// (exclude if an incomplete entry exists) had no evidence against such a
// model and wrongly advertised it as servable at 0 bytes downloaded.
func TestGetDownloadedModels_ExcludesModelWithNoDownloadRecordAtAll(t *testing.T) {
	srv := fakeExoServer(t,
		[]string{"mlx-community/Qwen3.6-35B-A3B-4bit"},
		map[string][]any{}, // no download record for it whatsoever
	)
	defer srv.Close()

	got, err := New(srv.URL).GetDownloadedModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected a never-queued model to be excluded, got %v", got)
	}
}

// TestGetDownloadedModels_ExcludesPendingDownload reproduces the other half
// of the live bug: a model with an explicit DownloadPending entry (0 of
// 20.4GB downloaded) must still be excluded, not just a never-queued model.
func TestGetDownloadedModels_ExcludesPendingDownload(t *testing.T) {
	srv := fakeExoServer(t,
		[]string{"mlx-community/Qwen3.6-35B-A3B-4bit"},
		map[string][]any{
			"node-a": {downloadEntry("DownloadPending", "mlx-community/Qwen3.6-35B-A3B-4bit")},
		},
	)
	defer srv.Close()

	got, err := New(srv.URL).GetDownloadedModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected a DownloadPending model to be excluded, got %v", got)
	}
}

// TestGetDownloadedModels_IncludesConfirmedComplete is the positive case:
// a model with an explicit DownloadCompleted entry is the only thing that
// should ever be advertised as servable.
func TestGetDownloadedModels_IncludesConfirmedComplete(t *testing.T) {
	srv := fakeExoServer(t,
		[]string{"mlx-community/Qwen3.5-9B-4bit", "mlx-community/Qwen3.6-35B-A3B-4bit"},
		map[string][]any{
			"node-a": {
				downloadEntry("DownloadCompleted", "mlx-community/Qwen3.5-9B-4bit"),
				downloadEntry("DownloadPending", "mlx-community/Qwen3.6-35B-A3B-4bit"),
			},
		},
	)
	defer srv.Close()

	got, err := New(srv.URL).GetDownloadedModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0]["id"] != "mlx-community/Qwen3.5-9B-4bit" {
		t.Errorf("expected only the confirmed-complete model, got %v", got)
	}
}

// TestGetDownloadedModels_StateFetchErrorFailsClosed confirms a failure to
// cross-check download state advertises nothing, rather than trusting the
// unreliable catalog endpoint on its own.
func TestGetDownloadedModels_StateFetchErrorFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "some-model"}}})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := New(srv.URL).GetDownloadedModels(context.Background()); err == nil {
		t.Error("expected an error when /state can't be fetched for cross-check")
	}
}

func TestAwaitInstance_IgnoresMalformedLinesBeforeTerminalEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keep-alive comment\n\n")
		fmt.Fprint(w, "data: not-json\n\n")
		fmt.Fprint(w, "data: {\"type\":\"ready\"}\n\n")
	}))
	defer srv.Close()

	if err := New(srv.URL).AwaitInstance(context.Background(), "x"); err != nil {
		t.Errorf("expected malformed/unknown lines to be skipped, got error: %v", err)
	}
}
