// stub-exo is a minimal Exo API stub for simulation and integration testing.
// It implements the six endpoints that oim's exoadapter actually calls, returning
// plausible fake data so node agents can register and accept jobs without a real
// GPU or a real Exo instance.
//
// Configuration (all via environment variables):
//
//	STUB_LISTEN      listen address (default :52415)
//	STUB_NODE_NAME   node name in responses (default stub-node)
//	STUB_MODELS      comma-separated model IDs (default llama-3.2-3b,mixtral-8x7b)
//	STUB_MEMORY_GB   declared memory for placement previews (default 16)
//	STUB_LATENCY_MS  simulated inference latency in ms (default 150)
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	listenAddr := envOr("STUB_LISTEN", ":52415")
	nodeName := envOr("STUB_NODE_NAME", "stub-node")
	modelsRaw := envOr("STUB_MODELS", "llama-3.2-3b,mixtral-8x7b")
	memGBStr := envOr("STUB_MEMORY_GB", "16")
	latMSStr := envOr("STUB_LATENCY_MS", "150")

	memGB, _ := strconv.ParseFloat(memGBStr, 64)
	latMS, _ := strconv.Atoi(latMSStr)

	var modelIDs []string
	for _, s := range strings.Split(modelsRaw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			modelIDs = append(modelIDs, s)
		}
	}
	if len(modelIDs) == 0 {
		modelIDs = []string{"llama-3.2-3b"}
	}

	var reqCount atomic.Int64
	var instanceCount atomic.Int64

	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(v); err != nil {
			log.Printf("[stub-exo] encode error: %v", err)
		}
	}

	modelList := func(filterQuery string) []map[string]any {
		out := make([]map[string]any, 0, len(modelIDs))
		for _, id := range modelIDs {
			if filterQuery != "" && !strings.Contains(id, filterQuery) {
				continue
			}
			out = append(out, map[string]any{
				"id":             id,
				"model_id":       id,
				"status":         "downloaded",
				"context_length": 4096,
			})
		}
		return out
	}

	mux := http.NewServeMux()

	// GET /state — health check; DetectClusterNode reads topology.peers
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"node_id": nodeName,
			"status":  "healthy",
			"topology": map[string]any{
				"peers": []any{},
				"nodes": []any{},
			},
		})
	})

	// GET /models[?status=downloaded] — capability.buildModelList
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		models := modelList("")
		writeJSON(w, models)
	})

	// GET /models/search?query=... — exoadapter.SearchModels
	mux.HandleFunc("GET /models/search", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		writeJSON(w, modelList(query))
	})

	// GET /instance/previews?model_id=... — exoadapter.PreviewInstancePlacements
	mux.HandleFunc("GET /instance/previews", func(w http.ResponseWriter, r *http.Request) {
		modelID := r.URL.Query().Get("model_id")
		if modelID == "" {
			modelID = modelIDs[0]
		}
		writeJSON(w, []map[string]any{
			{
				"model_id":      modelID,
				"sharding_type": "solo",
				"memory_delta_by_node": map[string]any{
					nodeName: memGB * 0.6,
				},
			},
		})
	})

	// POST /instance — exoadapter.CreateInstance
	mux.HandleFunc("POST /instance", func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("stub-inst-%d", instanceCount.Add(1))
		writeJSON(w, map[string]any{"command_id": id, "id": id})
	})

	// DELETE /instance/{id} — exoadapter.DeleteInstance
	mux.HandleFunc("DELETE /instance/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// POST /v1/chat/completions — exoadapter.RunChatCompletion (stream=false only)
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		if latMS > 0 {
			time.Sleep(time.Duration(latMS) * time.Millisecond)
		}
		n := reqCount.Add(1)
		modelID, _ := req["model"].(string)
		if modelID == "" {
			modelID = modelIDs[0]
		}
		content := fmt.Sprintf("Simulated response #%d from %s (model=%s) via Open Inference Mesh.", n, nodeName, modelID)
		writeJSON(w, map[string]any{
			"id":      fmt.Sprintf("chatcmpl-stub-%d", n),
			"object":  "chat.completion",
			"model":   modelID,
			"created": time.Now().Unix(),
			"choices": []map[string]any{
				{
					"index": 0,
					"message": map[string]any{
						"role":    "assistant",
						"content": content,
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": len(strings.Fields(content)),
				"total_tokens":      10 + len(strings.Fields(content)),
			},
		})
	})

	log.Printf("[stub-exo] %s | models=%v | mem=%.0fGB | latency=%dms | listening on %s",
		nodeName, modelIDs, memGB, latMS, listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("[stub-exo] fatal: %v", err)
	}
}
