// Package agent implements the node agent lifecycle:
//  1. Register with the assigned pod coordinator
//  2. Serve inference jobs at the local HTTP endpoint
//  3. Refresh manifest on a heartbeat interval
//
// Build LAST among node-agent components — wire against real implementations,
// not stubs, so integration bugs surface as logic errors rather than "not implemented" (proposal §8 build order).
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/open-inference-mesh/oim/internal/bench"
	"github.com/open-inference-mesh/oim/internal/capability"
	"github.com/open-inference-mesh/oim/internal/exoadapter"
	"github.com/open-inference-mesh/oim/internal/jobrunner"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// Config holds the node agent's operational parameters.
type Config struct {
	CoordinatorURL  string
	ExoURL          string
	ListenAddr      string        // e.g. ":8765"
	RefreshInterval time.Duration // how often to re-register and refresh manifest
	BenchInterval   time.Duration // how often to re-run benchmark and submit result (0 = disabled)
	CapacityPct     float64       // memory contribution cap (0.0–1.0)
	GeographicHint  string
}

func DefaultConfig() Config {
	return Config{
		CoordinatorURL:  "http://localhost:9000",
		ExoURL:          exoadapter.DefaultURL,
		ListenAddr:      ":8765",
		RefreshInterval: 30 * time.Second,
		BenchInterval:   0, // disabled by default; enable with --bench-interval
		CapacityPct:     0.5,
		GeographicHint:  "",
	}
}

// Run starts the node agent. It blocks until ctx is cancelled.
// priv and pub are the node's Ed25519 keypair loaded via identity.LoadOrCreate().
func Run(ctx context.Context, priv, pub []byte, cfg Config) error {
	exo := exoadapter.New(cfg.ExoURL)
	runner := jobrunner.New(cfg.ExoURL)

	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = ":8765"
	}

	// Derive the reachability endpoint from the listen address so the coordinator
	// knows how to reach back to this node.
	reachabilityEndpoint, err := resolveReachabilityEndpoint(listenAddr)
	if err != nil {
		return fmt.Errorf("resolve reachability endpoint: %w", err)
	}

	opts := capability.DefaultOptions()
	opts.MemoryCapPct = cfg.CapacityPct
	opts.ReachabilityEndpoint = reachabilityEndpoint
	if cfg.GeographicHint != "" {
		opts.GeographicHint = cfg.GeographicHint
	}

	// Initial registration.
	manifest, err := capability.AssembleManifest(ctx, exo, pub, opts)
	if err != nil {
		return fmt.Errorf("assemble manifest: %w", err)
	}
	if err := register(ctx, cfg.CoordinatorURL, priv, pub, manifest); err != nil {
		return fmt.Errorf("initial registration failed: %w", err)
	}
	log.Printf("[agent] registered with coordinator %s as node %s", cfg.CoordinatorURL, manifest.NodeID)

	// Start HTTP server for job reception (non-blocking).
	nodeID := manifest.NodeID
	srv := buildJobServer(runner, cfg.CapacityPct, nodeID, cfg.CoordinatorURL)
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}
	go func() {
		log.Printf("[agent] serving jobs at %s", listenAddr)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[agent] job server error: %v", err)
		}
	}()

	// Heartbeat loop: refresh manifest at RefreshInterval; re-bench at BenchInterval if set.
	ticker := time.NewTicker(cfg.RefreshInterval)
	defer ticker.Stop()
	var benchC <-chan time.Time
	if cfg.BenchInterval > 0 {
		bt := time.NewTicker(cfg.BenchInterval)
		defer bt.Stop()
		benchC = bt.C
	}

	for {
		select {
		case <-ctx.Done():
			log.Printf("[agent] shutting down")
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutCtx)

		case <-ticker.C:
			fresh, err := capability.AssembleManifest(ctx, exo, pub, opts)
			if err != nil {
				log.Printf("[agent] manifest refresh error: %v", err)
				continue
			}
			if err := refresh(ctx, cfg.CoordinatorURL, nodeID, fresh); err != nil {
				log.Printf("[agent] refresh error (will retry): %v", err)
				if regErr := register(ctx, cfg.CoordinatorURL, priv, pub, fresh); regErr != nil {
					log.Printf("[agent] re-registration also failed: %v", regErr)
				}
			}
			manifest = fresh

		case <-benchC:
			// Re-benchmark and submit result so the coordinator can detect tier fraud.
			// Non-fatal — a failed bench does not kill the agent.
			if len(manifest.Models) == 0 {
				continue
			}
			sig, err := bench.Run(ctx, exo, manifest.Models[0].ModelID, "medium", 1)
			if err != nil {
				log.Printf("[agent] re-bench error: %v", err)
				continue
			}
			if err := SubmitBenchmarkResult(ctx, cfg.CoordinatorURL, nodeID, sig); err != nil {
				log.Printf("[agent] submit benchmark result: %v", err)
			} else {
				log.Printf("[agent] submitted benchmark: %.1f tok/s decode", sig.TokensPerSecDecode)
			}
		}
	}
}

// buildJobServer constructs the HTTP mux that accepts inference jobs from the coordinator.
func buildJobServer(runner *jobrunner.Runner, capPct float64, nodeID, coordinatorURL string) *http.Server {
	mux := http.NewServeMux()

	// Health endpoint for coordinator liveness checks.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// OpenAI-compatible inference endpoint. The coordinator dispatches here.
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string           `json:"model"`
			Messages  []map[string]any `json:"messages"`
			JobID     string           `json:"oim_job_id"`
			ModelID   string           `json:"oim_model_id"`
			Lane      string           `json:"oim_lane"`
			Sensitive string           `json:"oim_sensitivity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if req.Model == "" && req.ModelID != "" {
			req.Model = req.ModelID
		}

		spec := protocol.JobSpec{
			JobID:   req.JobID,
			ModelID: req.Model,
			Lane:    protocol.JobLaneFast,
		}

		isContinuation := r.Header.Get("X-OIM-Continuation") == "true"
		start := time.Now()
		var result map[string]any
		var execErr error

		if req.Lane == string(protocol.JobLaneBackground) {
			result, execErr = runner.ExecuteBackgroundLane(r.Context(), spec, req.Messages, capPct, isContinuation)
		} else {
			result, execErr = runner.ExecuteFastLane(r.Context(), spec, req.Messages, capPct)
		}

		latencyMs := float64(time.Since(start).Milliseconds())
		go func() {
			// Non-blocking outcome report; ignore error — reporting failure must not disrupt the job.
			if err := ReportJobOutcome(context.Background(), coordinatorURL, nodeID, spec.JobID, execErr == nil, latencyMs); err != nil {
				log.Printf("[agent] report outcome: %v", err)
			}
		}()

		if execErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": execErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	return &http.Server{Handler: mux}
}

// register signs the manifest and POSTs it to the coordinator's /nodes/register endpoint.
func register(ctx context.Context, coordinatorURL string, priv, pub []byte, manifest *protocol.CapabilityManifest) error {
	payload, err := manifest.Bytes()
	if err != nil {
		return fmt.Errorf("serialize manifest: %w", err)
	}
	sig, err := protocol.SignPayload(priv, payload)
	if err != nil {
		return fmt.Errorf("sign manifest: %w", err)
	}
	reg := protocol.NodeRegistration{
		Manifest:  *manifest,
		PublicKey: pub,
		Signature: sig,
	}
	return postJSON(ctx, coordinatorURL+"/nodes/register", reg)
}

// refresh sends an updated manifest to the coordinator's /nodes/{id}/refresh endpoint.
func refresh(ctx context.Context, coordinatorURL, nodeID string, manifest *protocol.CapabilityManifest) error {
	return postJSON(ctx, coordinatorURL+"/nodes/"+nodeID+"/refresh", manifest)
}

func postJSON(ctx context.Context, url string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		rb, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("POST %s HTTP %d: %s", url, resp.StatusCode, rb)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// resolveReachabilityEndpoint converts a listen address like ":8765" to a full URL
// that the coordinator can reach back on.
func resolveReachabilityEndpoint(listenAddr string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", fmt.Errorf("parse listen address %q: %w", listenAddr, err)
	}
	if host == "" {
		host = "localhost"
	}
	return "http://" + host + ":" + port, nil
}
