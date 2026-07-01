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
	"github.com/open-inference-mesh/oim/internal/governor"
	"github.com/open-inference-mesh/oim/internal/jobrunner"
	"github.com/open-inference-mesh/oim/internal/nodeconfig"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// Config holds the node agent's operational parameters.
type Config struct {
	CoordinatorURL       string
	ExoURL               string
	ListenAddr           string        // e.g. ":8765"
	ReachabilityEndpoint string        // overrides auto-derived endpoint (useful behind NAT / in containers)
	RefreshInterval      time.Duration // how often to re-register and refresh manifest
	BenchInterval        time.Duration // how often to re-run benchmark and submit result (0 = disabled)
	CapacityPct          float64       // memory contribution cap (0.0–1.0)
	DeclaredMemoryGB     float64       // when > 0, overrides governor.TotalRAMGB() for simulation
	AllowedModels        []string      // empty = all downloaded Exo models; non-empty = allowlist
	UserID               string        // when set, earned credits from this node's work go to this user account
	GeographicHint       string
	GeoLat               float64 // approximate latitude; 0 = not declared
	GeoLng               float64 // approximate longitude; 0 = not declared
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
	// knows how to reach back to this node. An explicit override takes precedence
	// (needed behind NAT and in Docker containers).
	reachabilityEndpoint := cfg.ReachabilityEndpoint
	if reachabilityEndpoint == "" {
		var epErr error
		reachabilityEndpoint, epErr = resolveReachabilityEndpoint(listenAddr)
		if epErr != nil {
			return fmt.Errorf("resolve reachability endpoint: %w", epErr)
		}
	}

	opts := capability.DefaultOptions()
	opts.MemoryCapPct = cfg.CapacityPct
	opts.DeclaredMemoryGB = cfg.DeclaredMemoryGB
	opts.AllowedModels = cfg.AllowedModels
	opts.ReachabilityEndpoint = reachabilityEndpoint
	if cfg.GeographicHint != "" {
		opts.GeographicHint = cfg.GeographicHint
	}
	opts.GeoLat = cfg.GeoLat
	opts.GeoLng = cfg.GeoLng

	// Initial registration.
	manifest, err := capability.AssembleManifest(ctx, exo, pub, opts)
	if err != nil {
		return fmt.Errorf("assemble manifest: %w", err)
	}
	if err := register(ctx, cfg.CoordinatorURL, cfg.UserID, priv, pub, manifest); err != nil {
		return fmt.Errorf("initial registration failed: %w", err)
	}
	log.Printf("[agent] registered with coordinator %s as node %s", cfg.CoordinatorURL, manifest.NodeID)

	// Start HTTP server for job reception (non-blocking).
	nodeID := manifest.NodeID
	srv := buildJobServer(runner, exo, cfg.CapacityPct, nodeID, cfg.CoordinatorURL, cfg.ExoURL)
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
				if regErr := register(ctx, cfg.CoordinatorURL, cfg.UserID, priv, pub, fresh); regErr != nil {
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

// buildJobServer constructs the HTTP mux that accepts inference jobs from the coordinator
// and exposes /detect + /config for the dashboard Node Setup tab.
func buildJobServer(runner *jobrunner.Runner, exo *exoadapter.Client, capPct float64, nodeID, coordinatorURL, exoURL string) *http.Server {
	mux := http.NewServeMux()

	// Health endpoint for coordinator liveness checks.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Detection endpoint — called by the dashboard Node Setup tab to auto-populate
	// machine specs, Exo status, and available models.
	mux.HandleFunc("GET /detect", func(w http.ResponseWriter, r *http.Request) {
		sysInfo, _ := governor.SystemInfo()
		cfg, _ := nodeconfig.Load()
		exoHealthy := exo.IsHealthy(r.Context())

		var models []map[string]any
		if exoHealthy {
			raw, err := exo.GetDownloadedModels(r.Context())
			if err == nil {
				models = raw
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"node_id":           nodeID,
			"platform":          sysInfo["platform"],
			"is_apple_silicon":  sysInfo["is_apple_silicon"],
			"total_ram_gb":      sysInfo["total_ram_gb"],
			"available_ram_gb":  sysInfo["available_ram_gb"],
			"used_pct":          sysInfo["used_pct"],
			"has_secure_enclave": protocol.CheckSecureEnclaveAvailable(),
			"is_foregrounded":   governor.IsForegrounded(),
			"exo_healthy":       exoHealthy,
			"exo_url":           exoURL,
			"models":            models,
			"config":            cfg,
		})
	})

	// Config save endpoint — called by the dashboard Node Setup tab when the user
	// saves their contributor settings. Written to ~/.config/oim/config.json and
	// read on the next oim node start invocation.
	mux.HandleFunc("POST /config", func(w http.ResponseWriter, r *http.Request) {
		var cfg nodeconfig.Config
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := nodeconfig.Save(cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "saved", "path": nodeconfig.ConfigPath()})
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
		tokensDelivered := extractTokenCount(result)
		go func() {
			// Non-blocking outcome report; ignore error — reporting failure must not disrupt the job.
			if err := ReportJobOutcome(context.Background(), coordinatorURL, nodeID, spec.JobID, execErr == nil, latencyMs, tokensDelivered); err != nil {
				log.Printf("[agent] report outcome: %v", err)
			}
		}()

		if execErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": execErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	return &http.Server{Handler: agentCORS(mux)}
}

// agentCORS allows the local dashboard (any localhost origin) to call /detect and /config.
func agentCORS(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// register signs the manifest and POSTs it to the coordinator's /nodes/register endpoint.
func register(ctx context.Context, coordinatorURL, userID string, priv, pub []byte, manifest *protocol.CapabilityManifest) error {
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
		UserID:    userID,
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

// extractTokenCount reads completion_tokens from an Exo response.
// Returns 0 when the field is absent (stub-exo may not populate it).
func extractTokenCount(result map[string]any) int {
	if result == nil {
		return 0
	}
	usage, _ := result["usage"].(map[string]any)
	if usage == nil {
		return 0
	}
	if n, ok := usage["completion_tokens"].(float64); ok && n > 0 {
		return int(n)
	}
	return 0
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
