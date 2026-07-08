package tests

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-inference-mesh/oim/internal/exoadapter"
)

// exoStubServer serves /models and /state from fixed responses, mimicking a
// real Exo instance closely enough for GetDownloadedModels's cross-check logic.
func exoStubServer(t *testing.T, models []map[string]any, state map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": models})
		case "/state":
			json.NewEncoder(w).Encode(state)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGetDownloadedModelsAcceptsCompletedDownloadWithoutProgressField guards a
// real bug caught live: a real Exo /state response's "DownloadCompleted" entry
// has NO "downloaded" byte-progress field at all (only "total" — Exo apparently
// considers the field meaningless once finished). Reading the missing field as
// 0 and comparing 0 < total wrongly excluded a fully-downloaded model — on the
// user's real cluster, this silently emptied every node's model list even
// though a 20GB Qwen model was genuinely fully downloaded on all 3 devices.
func TestGetDownloadedModelsAcceptsCompletedDownloadWithoutProgressField(t *testing.T) {
	models := []map[string]any{
		{"id": "mlx-community/Qwen3.6-35B-A3B-4bit", "context_length": float64(262144)},
	}
	state := map[string]any{
		"downloads": map[string]any{
			"device-a": []any{
				map[string]any{
					// Real captured shape: no "downloaded" key at all once complete.
					"DownloadCompleted": map[string]any{
						"nodeId": "device-a",
						"shardMetadata": map[string]any{
							"PipelineShardMetadata": map[string]any{
								"modelCard": map[string]any{
									"modelId": "mlx-community/Qwen3.6-35B-A3B-4bit",
								},
							},
						},
						"total": map[string]any{"inBytes": float64(20429169263)},
					},
				},
			},
		},
	}
	srv := exoStubServer(t, models, state)
	exo := exoadapter.New(srv.URL)

	got, err := exo.GetDownloadedModels(context.Background())
	if err != nil {
		t.Fatalf("GetDownloadedModels: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected the fully-downloaded model to be included, got %d models: %v", len(got), got)
	}
	if got[0]["id"] != "mlx-community/Qwen3.6-35B-A3B-4bit" {
		t.Errorf("unexpected model in result: %v", got[0])
	}
}

// TestGetDownloadedModelsExcludesInProgressDownload confirms the fix didn't
// overcorrect: a real "DownloadPending" entry DOES carry both "downloaded" and
// "total" fields, and a genuinely incomplete download (downloaded < total)
// must still be excluded.
func TestGetDownloadedModelsExcludesInProgressDownload(t *testing.T) {
	models := []map[string]any{
		{"id": "mlx-community/gemma-4-26b-a4b-it-6bit", "context_length": float64(262144)},
	}
	state := map[string]any{
		"downloads": map[string]any{
			"device-a": []any{
				map[string]any{
					"DownloadPending": map[string]any{
						"nodeId": "device-a",
						"shardMetadata": map[string]any{
							"PipelineShardMetadata": map[string]any{
								"modelCard": map[string]any{
									"modelId": "mlx-community/gemma-4-26b-a4b-it-6bit",
								},
							},
						},
						"downloaded": map[string]any{"inBytes": float64(0)},
						"total":      map[string]any{"inBytes": float64(21781015708)},
					},
				},
			},
		},
	}
	srv := exoStubServer(t, models, state)
	exo := exoadapter.New(srv.URL)

	got, err := exo.GetDownloadedModels(context.Background())
	if err != nil {
		t.Fatalf("GetDownloadedModels: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected an in-progress download to be excluded, got %d models: %v", len(got), got)
	}
}

// TestPreviewInstancePlacementsHandlesPreviewsWrapperKey guards a second real
// bug caught live alongside the download-status one: Exo's real
// /instance/previews response wraps its array under {"previews": [...]}, not
// {"data": [...]} like /models — getJSON only recognized "data", so this
// silently returned an empty list, making it impossible to ever create an
// Exo instance for a real, fully-downloaded model (CreateInstance requires a
// placement from this call).
func TestPreviewInstancePlacementsHandlesPreviewsWrapperKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"previews": []map[string]any{
				{"model_id": "mlx-community/Qwen3.6-35B-A3B-4bit", "sharding": "Pipeline"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	exo := exoadapter.New(srv.URL)

	previews, err := exo.PreviewInstancePlacements(context.Background(), "mlx-community/Qwen3.6-35B-A3B-4bit")
	if err != nil {
		t.Fatalf("PreviewInstancePlacements: %v", err)
	}
	if len(previews) != 1 {
		t.Fatalf("expected 1 preview from the {previews:[]} wrapper shape, got %d", len(previews))
	}
	if previews[0]["model_id"] != "mlx-community/Qwen3.6-35B-A3B-4bit" {
		t.Errorf("unexpected preview content: %v", previews[0])
	}
}

// TestGetDownloadedModelsExcludesModelAbsentFromDownloadsMap guards a second
// real bug caught live this session, the mirror image of the one above: Exo's
// /models?downloaded=true returns essentially its ENTIRE catalog (~100
// models) regardless of real download status. A model with NO entry at all
// in /state's downloads map — the exact state of a model before it's ever
// been queued — has no "incomplete" evidence against it, so the previous
// blacklist-only logic (exclude only on explicit incomplete evidence) wrongly
// treated "no evidence" as "downloaded" and advertised a 0-byte, never-pulled
// 35B model as this node's servable capability, while completely omitting a
// different, fully-downloaded-and-running model. Only an explicit
// "DownloadCompleted" entry may now mark a model available (whitelist, not
// blacklist).
func TestGetDownloadedModelsExcludesModelAbsentFromDownloadsMap(t *testing.T) {
	models := []map[string]any{
		{"id": "mlx-community/llama-3.2-3b-4bit", "context_length": float64(4096)},
	}
	state := map[string]any{"downloads": map[string]any{}}
	srv := exoStubServer(t, models, state)
	exo := exoadapter.New(srv.URL)

	got, err := exo.GetDownloadedModels(context.Background())
	if err != nil {
		t.Fatalf("GetDownloadedModels: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected the never-queued, untracked model to be excluded, got %d models: %v", len(got), got)
	}
}
