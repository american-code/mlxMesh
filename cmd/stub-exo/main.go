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
//	STUB_DEVICE_COUNT number of devices in the simulated Exo cluster topology
//	                 (default: derived from STUB_MEMORY_GB — one device per ~48 GB,
//	                 clamped 1..6). Drives the Node Setup cluster diagram.
//	STUB_DEVICES     explicit heterogeneous cluster, overrides the even split,
//	                 e.g. "Mac Studio:32,Mac Studio:32,MacBook Pro:16"
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// chipCatalog / chassisCatalog give each simulated device a plausible, stable
// identity so the Node Setup topology reads like a real heterogeneous cluster.
var chipCatalog = []string{"Apple M2 Ultra", "Apple M3 Max", "Apple M1 Max", "Apple M2 Pro", "Apple M3 Ultra", "Apple M1 Ultra"}
var chassisCatalog = []string{"Mac Studio", "MacBook Pro", "Mac mini", "Mac Studio", "MacBook Pro", "Mac Pro"}

// simDevice is one explicit device in a heterogeneous STUB_DEVICES spec.
type simDevice struct {
	name  string
	memGB float64
	chip  string
}

// parseDevices reads STUB_DEVICES like "Mac Studio:32,Mac Studio:32,MacBook Pro:16"
// into explicit heterogeneous devices, so the sim can mirror a real mixed cluster
// instead of an even split. Returns nil when unset/empty (caller falls back to
// the even-split derived count).
func parseDevices(spec string) []simDevice {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil
	}
	chipForModel := func(model string) string {
		switch {
		case strings.Contains(model, "Studio"):
			return "Apple M2 Ultra"
		case strings.Contains(model, "MacBook"):
			return "Apple M3 Pro"
		case strings.Contains(model, "mini"):
			return "Apple M2 Pro"
		case strings.Contains(model, "Pro"):
			return "Apple M2 Ultra"
		default:
			return "Apple M2"
		}
	}
	var out []simDevice
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, memStr, ok := strings.Cut(part, ":")
		name = strings.TrimSpace(name)
		mem := 32.0
		if ok {
			if v, err := strconv.ParseFloat(strings.TrimSpace(memStr), 64); err == nil && v > 0 {
				mem = v
			}
		}
		out = append(out, simDevice{name: name, memGB: mem, chip: chipForModel(name)})
	}
	return out
}

// buildTopologyState assembles an Exo-shaped /state topology. Devices come from
// an explicit STUB_DEVICES spec when provided (heterogeneous, real-hardware-like)
// or from an even split of totalMemGB across deviceCount otherwise. Live-ish
// gpu/temp/power are regenerated each call so the dashboard's per-device diagram
// animates across re-detects. The shape exactly matches what
// capability.GetDeviceTopology / DetectClusterNode parse.
func buildTopologyState(nodeName string, totalMemGB float64, deviceCount int, devices []simDevice) map[string]any {
	// Explicit heterogeneous spec wins; else even-split fallback.
	if len(devices) == 0 {
		if deviceCount < 1 {
			deviceCount = 1
		}
		perDeviceGB := totalMemGB / float64(deviceCount)
		for i := 0; i < deviceCount; i++ {
			devices = append(devices, simDevice{
				name:  fmt.Sprintf("%s-%d", chassisCatalog[i%len(chassisCatalog)], i+1),
				memGB: perDeviceGB,
				chip:  chipCatalog[i%len(chipCatalog)],
			})
		}
	}
	deviceCount = len(devices)

	ids := make([]any, deviceCount)
	nodeMemory := map[string]any{}
	nodeIdentities := map[string]any{}
	nodeSystem := map[string]any{}
	connections := map[string]any{}

	deviceID := func(i int) string {
		if i == 0 {
			return nodeName
		}
		return fmt.Sprintf("%s-d%d", nodeName, i)
	}

	for i := 0; i < deviceCount; i++ {
		id := deviceID(i)
		ids[i] = id
		dev := devices[i]

		// Available memory jitters 45–80% free; devices with heavier load run hotter.
		freeFrac := 0.45 + rand.Float64()*0.35
		totalBytes := dev.memGB * (1 << 30)
		nodeMemory[id] = map[string]any{
			"ramTotal":     map[string]any{"inBytes": totalBytes},
			"ramAvailable": map[string]any{"inBytes": totalBytes * freeFrac},
		}
		nodeIdentities[id] = map[string]any{
			"friendlyName": dev.name,
			"modelId":      dev.name,
			"chipId":       dev.chip,
		}
		load := rand.Float64()
		nodeSystem[id] = map[string]any{
			"gpuUsage": math.Round(load*0.30*100) / 100,  // 0–30%
			"temp":     math.Round((32+load*26)*10) / 10, // 32–58 °C
			"sysPower": math.Round(28 + load*90),         // 28–118 W
		}

		// Ring topology: each device links to its two neighbors (self excluded
		// for a single-device "cluster").
		if deviceCount > 1 {
			peers := map[string]any{}
			for _, j := range []int{(i + 1) % deviceCount, (i - 1 + deviceCount) % deviceCount} {
				if j != i {
					peers[deviceID(j)] = map[string]any{"status": "connected"}
				}
			}
			connections[id] = peers
		}
	}

	return map[string]any{
		"node_id": nodeName,
		"status":  "healthy",
		"topology": map[string]any{
			"nodes":       ids,
			"peers":       []any{}, // self-excluding list unused; nodes[] carries the full set
			"connections": connections,
		},
		"nodeMemory":     nodeMemory,
		"nodeIdentities": nodeIdentities,
		"nodeSystem":     nodeSystem,
	}
}

// deriveDeviceCount picks a device count from declared memory when
// STUB_DEVICE_COUNT isn't set: one device per ~48 GB, clamped to 1..6, so the
// sim's large-memory nodes render as multi-device clusters and small ones as
// single devices.
func deriveDeviceCount(memGB float64) int {
	n := int(math.Round(memGB / 48.0))
	if n < 1 {
		n = 1
	}
	if n > 6 {
		n = 6
	}
	return n
}

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

	deviceCount := deriveDeviceCount(memGB)
	if v := os.Getenv("STUB_DEVICE_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			deviceCount = n
		}
	}
	// STUB_DEVICES gives an explicit heterogeneous cluster, e.g.
	// "Mac Studio:32,Mac Studio:32,MacBook Pro:16" — overrides the even split.
	simDevices := parseDevices(os.Getenv("STUB_DEVICES"))

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

	// GET /state — Exo-shaped cluster state. DetectClusterNode reads
	// topology.nodes + nodeMemory; GetDeviceTopology additionally reads
	// nodeIdentities/nodeSystem/connections to draw the Node Setup diagram.
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, buildTopologyState(nodeName, memGB, deviceCount, simDevices))
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

	// POST /v1/chat/completions — exoadapter.RunChatCompletion (stream=false)
	// and exoadapter.StreamChatCompletion (stream=true, task: server-side
	// streaming). Real Exo/vLLM-compatible servers emit stream_options.
	// include_usage's trailing usage-only frame the same way this does — so a
	// node's SSE-parsing logic (bufio.Scanner + sse.ExtractUsageTokens) works
	// unmodified against a real backend, not just this stub.
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
		completionTokens := len(strings.Fields(content))

		stream, _ := req["stream"].(bool)
		if !stream {
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
					"completion_tokens": completionTokens,
					"total_tokens":      10 + completionTokens,
				},
			})
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, `{"error":"streaming not supported"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		id := fmt.Sprintf("chatcmpl-stub-%d", n)
		created := time.Now().Unix()
		sendChunk := func(choices []map[string]any) {
			chunk := map[string]any{
				"id": id, "object": "chat.completion.chunk", "model": modelID,
				"created": created, "choices": choices,
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		// Role-opening chunk, then the content in a few word-sized deltas —
		// enough to exercise multi-chunk reassembly without real token timing.
		sendChunk([]map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}})
		for _, word := range strings.Fields(content) {
			sendChunk([]map[string]any{{"index": 0, "delta": map[string]any{"content": word + " "}, "finish_reason": nil}})
		}
		sendChunk([]map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}})
		// Trailing usage-only frame (stream_options.include_usage) — empty
		// choices, matches real OpenAI/vLLM-compatible streaming servers.
		usageChunk := map[string]any{
			"id": id, "object": "chat.completion.chunk", "model": modelID,
			"created": created, "choices": []map[string]any{},
			"usage": map[string]any{
				"prompt_tokens": 10, "completion_tokens": completionTokens,
				"total_tokens": 10 + completionTokens,
			},
		}
		data, _ := json.Marshal(usageChunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	})

	log.Printf("[stub-exo] %s | models=%v | mem=%.0fGB | latency=%dms | listening on %s",
		nodeName, modelIDs, memGB, latMS, listenAddr)
	srv := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("[stub-exo] fatal: %v", err)
	}
}
