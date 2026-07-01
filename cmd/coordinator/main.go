// Pod coordinator server — one per geographic/latency pod.
// Routing decisions happen here; the directory layer only does discovery (proposal §7.1).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/directory"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/settlement"
)

// apiKeyStore maps generated oim_* API keys ↔ user IDs.
// Both directions so we can look up by key (auth) and by user (revoke/check).
type apiKeyStore struct {
	mu      sync.RWMutex
	byKey   map[string]string // oim_xxx → userID
	byUser  map[string]string // userID → oim_xxx
}

func newAPIKeyStore() *apiKeyStore {
	return &apiKeyStore{
		byKey:  make(map[string]string),
		byUser: make(map[string]string),
	}
}

func (s *apiKeyStore) generate(userID string) (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	key := "oim_" + hex.EncodeToString(raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byUser[userID]; ok {
		delete(s.byKey, old) // revoke existing key
	}
	s.byKey[key] = userID
	s.byUser[userID] = key
	return key, nil
}

func (s *apiKeyStore) lookup(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	uid, ok := s.byKey[key]
	return uid, ok
}

func (s *apiKeyStore) revoke(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key, ok := s.byUser[userID]; ok {
		delete(s.byKey, key)
		delete(s.byUser, userID)
	}
}

func (s *apiKeyStore) exists(userID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.byUser[userID]
	return ok
}

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var listenAddr, podID, regionHint, directoryURL, publicURL, apiKey string
	var maxDispatchAttempts, directoryIntervalSec int

	cmd := &cobra.Command{
		Use:   "oim-coordinator",
		Short: "Open Inference Mesh pod coordinator",
		Long: `oim-coordinator is the pod coordinator for the Open Inference Mesh.
It maintains a live registry of contributing nodes within this geographic pod
and routes inference jobs to the best available node.

Nodes register with: oim node start --coordinator http://<this-host>:<port>
Optionally report aggregate health to a directory with --directory.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCoordinator(listenAddr, podID, regionHint, directoryURL, publicURL, apiKey,
				maxDispatchAttempts, time.Duration(directoryIntervalSec)*time.Second)
		},
	}
	cmd.Flags().StringVar(&listenAddr, "listen", ":9000", "Address to listen on")
	cmd.Flags().StringVar(&podID, "pod-id", "pod-local", "Unique identifier for this pod")
	cmd.Flags().StringVar(&regionHint, "region", "us", "Geographic region hint (us/eu/apac)")
	cmd.Flags().StringVar(&publicURL, "public-url", "", "Public URL clients use to reach this coordinator (reported to directory)")
	cmd.Flags().IntVar(&maxDispatchAttempts, "max-attempts", 3, "Max nodes to try per fast-lane dispatch")
	cmd.Flags().StringVar(&directoryURL, "directory", "", "Directory server URL for pod discovery registration (empty = disabled)")
	cmd.Flags().IntVar(&directoryIntervalSec, "directory-interval", 60, "Seconds between directory health-digest reports")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Bearer token required for write operations (empty = disabled)")
	return cmd
}

// podCapacitySource wraps NodeRegistry + MeasurementStore to satisfy settlement.NodeCapacitySource.
// Ignores the podID parameter because all nodes in this registry belong to this pod.
type podCapacitySource struct {
	registry     *coordinator.NodeRegistry
	measurements *coordinator.MeasurementStore
}

func (s *podCapacitySource) VerifiedCapacityForPod(_ string) float64 {
	return s.registry.VerifiedCapacityScore(s.measurements, 0.20)
}

// settlementRecordStore is a minimal in-memory store for published settlement records.
type settlementRecordStore struct {
	mu      sync.Mutex
	records []map[string]any
}

func (s *settlementRecordStore) store(r map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
}

func runCoordinator(listenAddr, podID, regionHint, directoryURL, publicURL, apiKey string, maxAttempts int, directoryInterval time.Duration) error {
	registry := coordinator.NewNodeRegistry()
	assignments := coordinator.NewAssignmentStore()
	measurements := coordinator.NewMeasurementStore()
	ledger := settlement.NewLedger()
	settlementRecords := &settlementRecordStore{}
	capacitySrc := &podCapacitySource{registry: registry, measurements: measurements}
	apiKeys := newAPIKeyStore()

	// ctx is cancelled on SIGINT/SIGTERM to drain the job queue workers cleanly.
	ctx, cancelCtx := context.WithCancel(context.Background())

	// MQTT-style bounded job queue: callers with X-OIM-Queue: true are held here
	// when all nodes are busy, rather than receiving an immediate 503.
	jobQueue := coordinator.NewJobQueue(ctx,
		coordinator.DefaultQueueCapacity,
		coordinator.DefaultQueueWorkers,
		registry, maxAttempts,
	)

	// nodeUsers maps node_id → user_id so earned credits reach the right account.
	var nodeUsers sync.Map // string → string

	// grantedUsers tracks which user IDs have claimed a startup grant this session.
	// In-memory dedup only — resets on coordinator restart, which is fine for dev/sim.
	var grantedUsers sync.Map

	mux := http.NewServeMux()

	// --- Node registration ---

	// POST /nodes/register — NodeRegistration → registry
	mux.HandleFunc("POST /nodes/register", func(w http.ResponseWriter, r *http.Request) {
		var reg protocol.NodeRegistration
		if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
			writeErr(w, http.StatusBadRequest, "parse registration: "+err.Error())
			return
		}
		ok, err := registry.Register(reg)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusForbidden, "signature verification failed")
			return
		}
		// Track node → user mapping for earnings attribution.
		// Falls back to node_id as account key when no user_id provided.
		earningsTarget := reg.Manifest.NodeID
		if reg.UserID != "" {
			earningsTarget = reg.UserID
		}
		nodeUsers.Store(reg.Manifest.NodeID, earningsTarget)
		log.Printf("[coordinator] registered node %s (%s) earnings→%s", reg.Manifest.NodeID, reg.Manifest.GeographicHint, earningsTarget)
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "registered",
			"node_id": reg.Manifest.NodeID,
		})
	})

	// POST /nodes/{id}/refresh — updated CapabilityManifest
	mux.HandleFunc("POST /nodes/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var manifest protocol.CapabilityManifest
		if err := json.NewDecoder(r.Body).Decode(&manifest); err != nil {
			writeErr(w, http.StatusBadRequest, "parse manifest: "+err.Error())
			return
		}
		if err := registry.Refresh(nodeID, manifest); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "refreshed"})
	})

	// DELETE /nodes/{id} — explicit deregister (optional; TTL handles stale nodes automatically)
	mux.HandleFunc("DELETE /nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		registry.MarkUnreachable(nodeID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "deregistered"})
	})

	// --- Job routing ---

	// POST /v1/chat/completions — OpenAI-compatible fast-lane dispatch.
	// Accepts standard OpenAI format + optional oim_* extension fields.
	// Credit check: if X-OIM-User-ID header is present, balance is checked before dispatch
	// and debited on completion. Omit the header for dev/anonymous access.
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model       string           `json:"model"`
			Messages    []map[string]any `json:"messages"`
			MaxTokens   int              `json:"max_tokens"`
			OIMJobID    string           `json:"oim_job_id"`
			OIMSensitiv string           `json:"oim_sensitivity"`
			OIMMaxPrice float64          `json:"oim_max_price_per_unit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "parse request: "+err.Error())
			return
		}

		const defaultMaxTokens = 2048
		maxTok := req.MaxTokens
		if maxTok <= 0 {
			maxTok = defaultMaxTokens
		}

		// Credit gate — only enforced when the caller identifies themselves.
		// Anonymous / internal calls are allowed through (dev mode, node-to-node).
		userID := r.Header.Get("X-OIM-User-ID")
		if userID != "" {
			rate := sensitivityRate(req.OIMSensitiv)
			estimatedCost := float64(maxTok) / 1000.0 * rate
			bal := ledger.GetBalance(userID)
			if bal.Total < estimatedCost {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusPaymentRequired)
				json.NewEncoder(w).Encode(map[string]any{
					"error":      "insufficient_credits",
					"balance":    bal.Total,
					"required":   estimatedCost,
					"max_tokens": maxTok,
				})
				return
			}
		}

		sensitivity := protocol.SensitivityModerate
		if req.OIMSensitiv == string(protocol.SensitivityLow) {
			sensitivity = protocol.SensitivityLow
		} else if req.OIMSensitiv == string(protocol.SensitivityHighRequiresAttestation) {
			sensitivity = protocol.SensitivityHighRequiresAttestation
		}

		jobID := req.OIMJobID
		if jobID == "" {
			jobID = fmt.Sprintf("job-%d", time.Now().UnixNano())
		}

		job := protocol.JobSpec{
			JobID:           jobID,
			ModelID:         req.Model,
			Lane:            protocol.JobLaneFast,
			Sensitivity:     sensitivity,
			MaxPricePerUnit: req.OIMMaxPrice,
			RedundancyDepth: 0,
			PayloadRef:      "",
		}

		result, err := coordinator.DispatchFastLane(r.Context(), job, req.Messages, registry, maxAttempts)
		if err != nil {
			// X-OIM-Queue: true — hold the request in the coordinator queue instead of 503.
			// Workers retry dispatch every ~400ms until a node accepts or the 30s timeout fires.
			if r.Header.Get("X-OIM-Queue") == "true" {
				result, err = jobQueue.Enqueue(r.Context(), job, req.Messages, coordinator.DefaultQueueTimeout)
			}
			if err != nil {
				writeErr(w, http.StatusServiceUnavailable, err.Error())
				return
			}
		}

		// Debit after successful dispatch. Use actual completion_tokens from the response
		// when available; fall back to the pre-dispatch estimate.
		if userID != "" {
			rate := sensitivityRate(req.OIMSensitiv)
			actualTok := extractCompletionTokens(result, maxTok)
			actualCost := float64(actualTok) / 1000.0 * rate
			if !ledger.DebitAccount(userID, actualCost, jobID) {
				// Balance may have shifted between check and debit (concurrent requests).
				// Job is complete — log the race but don't fail the response.
				log.Printf("[coordinator] debit race user=%s job=%s cost=%.4f", userID, jobID, actualCost)
			} else {
				log.Printf("[coordinator] debit user=%s job=%s tokens=%d cost=%.4f", userID, jobID, actualTok, actualCost)
			}
		}

		writeJSON(w, http.StatusOK, result)
	})

	// POST /jobs/background/assign — persisted sticky-session assignment.
	mux.HandleFunc("POST /jobs/background/assign", func(w http.ResponseWriter, r *http.Request) {
		var job protocol.JobSpec
		if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
			writeErr(w, http.StatusBadRequest, "parse job spec: "+err.Error())
			return
		}
		if err := job.Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid job spec: "+err.Error())
			return
		}
		a, err := coordinator.AssignBackgroundJob(job, registry)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		assignments.Save(a)
		writeJSON(w, http.StatusOK, a)
	})

	// POST /jobs/background/cycle — resolve which node handles this recurrence cycle.
	mux.HandleFunc("POST /jobs/background/cycle", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JobID string `json:"job_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "parse request: "+err.Error())
			return
		}
		a, ok := assignments.Get(req.JobID)
		if !ok {
			writeErr(w, http.StatusNotFound, fmt.Sprintf("no assignment for job %s; call /jobs/background/assign first", req.JobID))
			return
		}
		nodeID, isCont, err := coordinator.ResolveForCycle(a, registry)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"node_id":         nodeID,
			"is_continuation": isCont,
		})
	})

	// --- Reputation / verification ---

	// POST /nodes/{id}/benchmark-result — node submits a fresh MeasuredSignature.
	// Stored in MeasurementStore; used by VerifyTierClaim to detect fraud (proposal §8.2/9.2).
	mux.HandleFunc("POST /nodes/{id}/benchmark-result", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var sig protocol.MeasuredSignature
		if err := json.NewDecoder(r.Body).Decode(&sig); err != nil {
			writeErr(w, http.StatusBadRequest, "parse measurement: "+err.Error())
			return
		}
		measurements.Store(nodeID, &sig)
		log.Printf("[coordinator] node %s submitted benchmark: %.1f tok/s decode", nodeID, sig.TokensPerSecDecode)
		writeJSON(w, http.StatusOK, map[string]string{"status": "stored"})
	})

	// POST /nodes/{id}/job-outcome — node reports job completion.
	// When tokens_delivered > 0, credits the node's registered user at the moderate rate.
	// This is the earning side of the credit ledger: node operators earn as they serve.
	mux.HandleFunc("POST /nodes/{id}/job-outcome", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var outcome struct {
			JobID           string  `json:"job_id"`
			Success         bool    `json:"success"`
			LatencyMs       float64 `json:"latency_ms"`
			TokensDelivered int     `json:"tokens_delivered"`
		}
		if err := json.NewDecoder(r.Body).Decode(&outcome); err != nil {
			writeErr(w, http.StatusBadRequest, "parse outcome: "+err.Error())
			return
		}
		log.Printf("[coordinator] job-outcome node=%s job=%s success=%v latency=%.0fms tokens=%d",
			nodeID, outcome.JobID, outcome.Success, outcome.LatencyMs, outcome.TokensDelivered)

		if outcome.Success && outcome.TokensDelivered > 0 {
			accountKey, _ := nodeUsers.Load(nodeID)
			if accountKey == nil {
				accountKey = nodeID // fallback: credit node_id directly
			}
			earned := float64(outcome.TokensDelivered) / 1000.0 * 1.0 // moderate rate
			_ = ledger.CreditAccount(settlement.CreditEntry{
				UserID:            accountKey.(string),
				Origin:            settlement.CreditOriginEarnedContrib,
				Amount:            earned,
				GrantedOrEarnedAt: time.Now(),
				SourceReference:   outcome.JobID,
			})
			log.Printf("[coordinator] earned user=%s node=%s tokens=%d credits=%.4f",
				accountKey, nodeID, outcome.TokensDelivered, earned)
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "recorded"})
	})

	// GET /nodes/{id}/verify-tier?tolerance=0.20 — compare node's submitted benchmark
	// against its registered claimed MeasuredSignature.
	mux.HandleFunc("GET /nodes/{id}/verify-tier", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		tolerancePct := 0.20
		if t := r.URL.Query().Get("tolerance"); t != "" {
			if _, err := fmt.Sscanf(t, "%f", &tolerancePct); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid tolerance: "+err.Error())
				return
			}
		}
		// Look up claimed signature from registry.
		claimed, err := registry.ClaimedSignature(nodeID)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		if claimed == nil {
			// No claimed signature — node registered without a benchmark. Cannot verify.
			writeJSON(w, http.StatusOK, map[string]any{
				"node_id":  nodeID,
				"verified": false,
				"reason":   "node has no claimed MeasuredSignature to compare against",
			})
			return
		}
		ok, err := coordinator.VerifyTierClaim(nodeID, *claimed, measurements, tolerancePct)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"node_id":  nodeID,
				"verified": false,
				"reason":   err.Error(),
			})
			return
		}
		reason := "within tolerance"
		if !ok {
			reason = "measured performance diverges from claimed signature"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"node_id":   nodeID,
			"verified":  ok,
			"tolerance": tolerancePct,
			"reason":    reason,
		})
	})

	// --- Settlement (proposal §9 and §10) ---

	// POST /settlement/records — receive a signed settlement record from a node.
	// Stores the record regardless of verification_result — failed-verification records
	// are evidence for dispute resolution, not noise to be silently dropped (proposal §10).
	// Credits the node's earned balance when verification_result is true.
	mux.HandleFunc("POST /settlement/records", func(w http.ResponseWriter, r *http.Request) {
		var record map[string]any
		if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
			writeErr(w, http.StatusBadRequest, "parse record: "+err.Error())
			return
		}
		settlementRecords.store(record)

		if verified, _ := record["verification_result"].(bool); verified {
			if do, ok := record["division_order"].(map[string]any); ok {
				nodeID, _ := do["node_id"].(string)
				totalValue, _ := do["total_value"].(float64)
				recordID, _ := record["record_id"].(string)
				if nodeID != "" && totalValue > 0 {
					_ = ledger.CreditAccount(settlement.CreditEntry{
						UserID:            nodeID,
						Origin:            settlement.CreditOriginEarnedContrib,
						Amount:            totalValue,
						GrantedOrEarnedAt: time.Now(),
						SourceReference:   recordID,
					})
					log.Printf("[coordinator] credited node=%s amount=%.4f record=%s", nodeID, totalValue, recordID)
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "stored"})
	})

	// POST /users/{id}/startup-grant — issue a one-time bootstrap grant sized by pod verified capacity.
	// The grant amount steps down as verified capacity grows (proposal §9.4, §11).
	// Idempotent within a coordinator session: second claim returns already-claimed balance.
	mux.HandleFunc("POST /users/{id}/startup-grant", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("id")
		if _, alreadyClaimed := grantedUsers.LoadOrStore(userID, true); alreadyClaimed {
			bal := ledger.GetBalance(userID)
			writeJSON(w, http.StatusOK, map[string]any{
				"amount": bal.GrantBalance,
				"status": "already_claimed",
			})
			return
		}
		entry, err := settlement.IssueStartupGrant(ledger, userID, podID, capacitySrc, settlement.DEFAULT_DECAY_STEPS)
		if err != nil {
			grantedUsers.Delete(userID) // allow retry if issue failed
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("[coordinator] startup-grant user=%s amount=%.2f pod=%s", userID, entry.Amount, podID)
		writeJSON(w, http.StatusOK, entry)
	})

	// GET /users/{id}/balance — credit balance split by grant vs. earned origin.
	// The split must never be collapsed to one number — dashboard shows both separately (proposal §5a).
	mux.HandleFunc("GET /users/{id}/balance", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("id")
		writeJSON(w, http.StatusOK, ledger.GetBalance(userID))
	})

	// POST /users/{id}/api-key — generate (or replace) a per-user API key.
	// The key is returned ONCE here and never retrievable again; store it client-side.
	// The key can be used in "Authorization: Bearer oim_xxx" instead of X-OIM-User-ID.
	mux.HandleFunc("POST /users/{id}/api-key", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("id")
		key, err := apiKeys.generate(userID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "generate key: "+err.Error())
			return
		}
		log.Printf("[coordinator] api-key generated user=%s", userID)
		writeJSON(w, http.StatusOK, map[string]string{
			"api_key": key,
			"user_id": userID,
			"note":    "store this key — it cannot be retrieved again",
		})
	})

	// GET /users/{id}/api-key — check whether a key exists (does NOT return the key value).
	mux.HandleFunc("GET /users/{id}/api-key", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("id")
		writeJSON(w, http.StatusOK, map[string]any{
			"user_id": userID,
			"exists":  apiKeys.exists(userID),
		})
	})

	// DELETE /users/{id}/api-key — revoke the current key. A new one can be generated.
	mux.HandleFunc("DELETE /users/{id}/api-key", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("id")
		apiKeys.revoke(userID)
		log.Printf("[coordinator] api-key revoked user=%s", userID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
	})

	// --- Metrics ---

	// GET /metrics — live queue depth, backpressure, and in-flight counters.
	// Polled by the dashboard to render the backpressure panel.
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		snap := registry.Snapshot()
		liveCount := 0
		for _, n := range snap {
			if n.Status == "live" {
				liveCount++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"queue_depth":      jobQueue.Depth(),
			"queue_capacity":   jobQueue.Capacity(),
			"backpressure_pct": jobQueue.BackpressurePct(),
			"total_in_flight":  registry.TotalInFlight(),
			"nodes_live":       liveCount,
			"nodes_total":      len(snap),
		})
	})

	// --- Pod health ---

	// GET /nodes — per-node snapshot for dashboards and network graph rendering.
	// Returns live and recently-stale nodes with memory, tok/s, models, and endpoint.
	mux.HandleFunc("GET /nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"pod_id": podID,
			"region": regionHint,
			"nodes":  registry.Snapshot(),
			"metrics": map[string]any{
				"queue_depth":      jobQueue.Depth(),
				"queue_capacity":   jobQueue.Capacity(),
				"backpressure_pct": jobQueue.BackpressurePct(),
				"total_in_flight":  registry.TotalInFlight(),
			},
		})
	})

	// GET /nodes/stream — SSE push of node snapshots every 2 s.
	// Clients connect once; the server pushes updates without polling overhead.
	// EventSource cannot send custom headers, so this endpoint is always unauthenticated.
	mux.HandleFunc("GET /nodes/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeErr(w, http.StatusInternalServerError, "streaming not supported")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if behind proxy

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				snap := map[string]any{
					"pod_id": podID,
					"region": regionHint,
					"nodes":  registry.Snapshot(),
					"metrics": map[string]any{
						"queue_depth":      jobQueue.Depth(),
						"queue_capacity":   jobQueue.Capacity(),
						"backpressure_pct": jobQueue.BackpressurePct(),
						"total_in_flight":  registry.TotalInFlight(),
					},
				}
				data, _ := json.Marshal(snap)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	})

	// GET /health — PodHealthDigest for directory layer and monitoring.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		digest := registry.HealthDigest(podID, regionHint, publicURL)
		writeJSON(w, http.StatusOK, digest)
	})

	// GET / — index for browsers and health checkers hitting the root.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		digest := registry.HealthDigest(podID, regionHint, publicURL)
		writeJSON(w, http.StatusOK, map[string]any{
			"service": "oim-coordinator",
			"pod_id":  podID,
			"region":  regionHint,
			"health":  digest,
			"endpoints": []string{
				"GET  /health",
				"GET  /nodes",
				"GET  /nodes/stream   (SSE push)",
				"POST /nodes/register",
				"POST /v1/chat/completions",
				"GET  /users/{id}/balance",
				"POST /users/{id}/startup-grant",
				"POST /settlement/records",
			},
		})
	})

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	var handler http.Handler = mux
	if apiKey != "" {
		handler = authMiddleware(apiKey, apiKeys, mux)
		log.Printf("[coordinator] API key auth enabled for write operations")
	}
	srv := &http.Server{Handler: corsMiddleware(handler)}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})

	// Optional: report aggregate pod health to the directory on a recurring schedule.
	if directoryURL != "" {
		resolver := directory.NewCentralizedResolver([]string{directoryURL})
		go func() {
			reportToDirectory(resolver, registry, podID, regionHint, publicURL)
			ticker := time.NewTicker(directoryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					reportToDirectory(resolver, registry, podID, regionHint, publicURL)
				}
			}
		}()
		log.Printf("[coordinator] reporting to directory %s every %s", directoryURL, directoryInterval)
	}

	go func() {
		<-quit
		cancelCtx() // stop job queue workers
		close(done)
		log.Println("[coordinator] shutting down")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		srv.Shutdown(shutCtx)
	}()

	log.Printf("[coordinator] pod=%s region=%s listening on %s", podID, regionHint, listenAddr)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func reportToDirectory(resolver *directory.CentralizedResolver, registry *coordinator.NodeRegistry, podID, regionHint, publicURL string) {
	digest := registry.HealthDigest(podID, regionHint, publicURL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := resolver.RegisterPod(ctx, digest); err != nil {
		log.Printf("[coordinator] directory report: %v", err)
	} else {
		log.Printf("[coordinator] reported to directory: pod=%s models=%v", podID, digest.ServableModelIDs)
	}
}

// authMiddleware requires a Bearer token for all write operations (POST, DELETE).
// GET requests and CORS preflight are always open so the dashboard can read without auth.
// /nodes/stream is also always open since EventSource cannot send Authorization headers.
// Two token forms are accepted:
//   - The static admin key (--api-key flag) — grants full access, no user attribution
//   - A per-user oim_* key (generated via POST /users/{id}/api-key) — the user ID is
//     injected as X-OIM-User-ID so the credit gate can debit the right account
func authMiddleware(adminKey string, keys *apiKeyStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reads and preflight are open
		if r.Method == http.MethodGet || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if auth == "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: Bearer token required"})
			return
		}
		// Static admin key — accepted as-is.
		if auth == adminKey {
			next.ServeHTTP(w, r)
			return
		}
		// Per-user oim_* key — inject the user ID and allow.
		if strings.HasPrefix(auth, "oim_") {
			if uid, ok := keys.lookup(auth); ok {
				// Synthetic header so the credit gate picks up the caller's identity
				// without requiring the client to send X-OIM-User-ID separately.
				if r.Header.Get("X-OIM-User-ID") == "" {
					r = r.Clone(r.Context())
					r.Header.Set("X-OIM-User-ID", uid)
				}
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized: invalid or expired API key"})
	})
}

func corsMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-OIM-User-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// sensitivityRate returns credits charged per 1,000 output tokens for each sensitivity tier.
// moderate (1.0) is the baseline — ~100 inference calls on the 100-credit startup grant.
func sensitivityRate(sensitivity string) float64 {
	switch sensitivity {
	case string(protocol.SensitivityLow):
		return 0.5 // bulk / non-private embeddings or classification
	case string(protocol.SensitivityHighRequiresAttestation):
		return 5.0 // Secure Enclave gate; attestation overhead justifies higher cost
	default:
		return 1.0 // moderate — the common case
	}
}

// extractCompletionTokens reads usage.completion_tokens from a dispatch result.
// Falls back to maxTok when the field is absent (stub-exo may not populate it).
func extractCompletionTokens(result map[string]any, maxTok int) int {
	usage, ok := result["usage"].(map[string]any)
	if !ok {
		return maxTok
	}
	if n, ok := usage["completion_tokens"].(float64); ok && n > 0 {
		return int(n)
	}
	return maxTok
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
