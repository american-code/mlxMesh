// Pod coordinator server — one per geographic/latency pod.
// Routing decisions happen here; the directory layer only does discovery (proposal §7.1).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/directory"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/settlement"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var listenAddr, podID, regionHint, directoryURL, publicURL string
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
			return runCoordinator(listenAddr, podID, regionHint, directoryURL, publicURL,
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

func runCoordinator(listenAddr, podID, regionHint, directoryURL, publicURL string, maxAttempts int, directoryInterval time.Duration) error {
	registry := coordinator.NewNodeRegistry()
	assignments := coordinator.NewAssignmentStore()
	measurements := coordinator.NewMeasurementStore()
	ledger := settlement.NewLedger()
	settlementRecords := &settlementRecordStore{}
	capacitySrc := &podCapacitySource{registry: registry, measurements: measurements}

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
		log.Printf("[coordinator] registered node %s (%s)", reg.Manifest.NodeID, reg.Manifest.GeographicHint)
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
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model       string           `json:"model"`
			Messages    []map[string]any `json:"messages"`
			OIMJobID    string           `json:"oim_job_id"`
			OIMSensitiv string           `json:"oim_sensitivity"`
			OIMMaxPrice float64          `json:"oim_max_price_per_unit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "parse request: "+err.Error())
			return
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
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
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
	// Foundation for M5 settlement; logged now, reconciled in settlement layer.
	mux.HandleFunc("POST /nodes/{id}/job-outcome", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var outcome struct {
			JobID     string  `json:"job_id"`
			Success   bool    `json:"success"`
			LatencyMs float64 `json:"latency_ms"`
		}
		if err := json.NewDecoder(r.Body).Decode(&outcome); err != nil {
			writeErr(w, http.StatusBadRequest, "parse outcome: "+err.Error())
			return
		}
		log.Printf("[coordinator] job-outcome node=%s job=%s success=%v latency=%.0fms",
			nodeID, outcome.JobID, outcome.Success, outcome.LatencyMs)
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
	mux.HandleFunc("POST /users/{id}/startup-grant", func(w http.ResponseWriter, r *http.Request) {
		userID := r.PathValue("id")
		entry, err := settlement.IssueStartupGrant(ledger, userID, podID, capacitySrc, settlement.DEFAULT_DECAY_STEPS)
		if err != nil {
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

	// --- Pod health ---

	// GET /nodes — per-node snapshot for dashboards and network graph rendering.
	// Returns live and recently-stale nodes with memory, tok/s, models, and endpoint.
	mux.HandleFunc("GET /nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"pod_id":  podID,
			"region":  regionHint,
			"nodes":   registry.Snapshot(),
		})
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

	srv := &http.Server{Handler: corsMiddleware(mux)}

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
		close(done)
		log.Println("[coordinator] shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
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

func corsMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
