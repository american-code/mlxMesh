// Copyright (C) 2024 Open Inference Mesh
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

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
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/open-inference-mesh/oim/internal/attestation"
	"github.com/open-inference-mesh/oim/internal/bench"
	"github.com/open-inference-mesh/oim/internal/capability"
	"github.com/open-inference-mesh/oim/internal/exoadapter"
	"github.com/open-inference-mesh/oim/internal/governor"
	"github.com/open-inference-mesh/oim/internal/httpmw"
	"github.com/open-inference-mesh/oim/internal/httptls"
	"github.com/open-inference-mesh/oim/internal/httpx"
	"github.com/open-inference-mesh/oim/internal/identity"
	"github.com/open-inference-mesh/oim/internal/jobrunner"
	"github.com/open-inference-mesh/oim/internal/nodeconfig"
	"github.com/open-inference-mesh/oim/internal/payloadcrypto"
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

	// ChaosDowntimePct simulates real-world churn for demo/simulation clusters:
	// on each refresh tick, this is the percent chance (0-100) the node "drops"
	// for a randomized window — it skips heartbeats (coordinator marks it stale
	// after livenessTTL) and refuses incoming jobs (coordinator marks it
	// unreachable on the failed dispatch). 0 disables chaos entirely — the
	// default for real contributor nodes, never set outside simulation.
	ChaosDowntimePct float64

	// AttemptEnclaveAttestation opts into trying to prove Secure Enclave
	// possession (see internal/attestation). OFF by default: generating a
	// usable Secure Enclave key from a plain `go build` binary requires the
	// binary to be code-signed with entitlements that in practice need an
	// Apple Developer Program enrollment + a registered App ID/provisioning
	// profile — confirmed by hitting errSecMissingEntitlement even with a
	// real (free) Apple Development signing identity. That's fundamentally
	// incompatible with "clone this repo and run it," so this project must
	// never require it. Attestation only ever gates eligibility for
	// SensitivityHighRequiresAttestation jobs — everything else works with
	// this off, which is the common case for anyone building from source.
	AttemptEnclaveAttestation bool

	// TLSCert/TLSKey serve this node's OWN job endpoint over HTTPS instead of
	// plain HTTP (task: coordinator->node TLS). Self-signed is fine — the
	// coordinator pins the exact certificate fingerprint recorded at this
	// node's registration rather than chain-verifying against a shared CA
	// (nodes are independently operated; there is no shared CA to verify
	// against). Both empty (the default) keeps today's plain-HTTP behavior
	// unchanged.
	TLSCert string
	TLSKey  string

	// DisableAutoPortMap opts OUT of the default automatic UPnP/NAT-PMP port
	// mapping attempt (see internal/natmap) that runs whenever
	// ReachabilityEndpoint is empty. Default false — the attempt is ON by
	// default because most real contributors are behind a home router's
	// NAT, and manually port-forwarding + finding your own public IP is not
	// something an average user should have to do. Has no effect at all
	// when ReachabilityEndpoint is explicitly set (an explicit override
	// always wins and skips the attempt entirely) — this is what already
	// happens for every simulated Docker node and every integration test
	// today, unchanged.
	DisableAutoPortMap bool
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

// Run starts the node agent. It blocks until ctx is canceled.
// priv and pub are the node's Ed25519 keypair loaded via identity.LoadOrCreate().
func Run(ctx context.Context, priv, pub []byte, cfg Config) error {
	exo := exoadapter.New(cfg.ExoURL)
	runner := jobrunner.New(cfg.ExoURL)

	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = ":8765"
	}

	// Coordinator->node TLS (task): this node's own job listener serves HTTPS
	// when both --tls-cert/--tls-key are set, self-signed and all — the
	// coordinator pins the fingerprint below rather than chain-verifying.
	nodeTLSEnabled := httptls.Enabled(cfg.TLSCert, cfg.TLSKey)

	// Delivery mode — the fix for nodes behind NAT/home routers. With NO
	// explicit reachability endpoint the node runs in PULL mode: it long-polls
	// the coordinator for work (outbound-only, the "mining-pool" model), so
	// port-forwarding / UPnP / NAT traversal are all irrelevant — the node
	// opens the connection, the coordinator never dials in. This is the default
	// for real contributors. An explicit --reachability-endpoint switches to
	// PUSH mode (coordinator dials into that address) — exactly what the
	// simulated Docker fleet, LAN nodes, and every integration test already
	// set, so their behavior is unchanged.
	//
	// portMappingStatus is mirrored to GET /detect so OIMMenuBar can show the
	// node's real connectivity: "pull" (outbound, always reachable) vs "manual"
	// (explicit push endpoint).
	pullMode := cfg.ReachabilityEndpoint == ""
	reachabilityEndpoint := cfg.ReachabilityEndpoint
	portMappingStatus := "manual"
	if pullMode {
		portMappingStatus = "pull"
		log.Printf("[agent] pull mode: receiving work over an outbound connection to %s (no inbound reachability needed)", cfg.CoordinatorURL)
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
	opts.PullDelivery = pullMode

	// Node-side pointer consumption (M8): load/generate this node's P-256
	// key-agreement keypair so it can decrypt payloads a client encrypted to
	// it. Non-fatal on failure — the node still serves ordinary (non-pointer)
	// jobs; it just won't advertise a key, so it's ineligible for a
	// reservation (POST /v1/reserve-node) until this succeeds.
	ecdhPriv, ecdhErr := identity.LoadOrCreateECDH()
	if ecdhErr != nil {
		log.Printf("[agent] encrypted-pointer support unavailable (no ecdh identity): %v", ecdhErr)
	} else {
		opts.ECDHPublicKey = base64.StdEncoding.EncodeToString(ecdhPriv.PublicKey().Bytes())
	}

	if nodeTLSEnabled {
		fp, fpErr := httptls.CertFingerprint(cfg.TLSCert)
		if fpErr != nil {
			return fmt.Errorf("read tls cert fingerprint: %w", fpErr)
		}
		opts.TLSCertFingerprint = fp
		httptls.WarnIfExpiringSoon(cfg.TLSCert, 30*24*time.Hour, "node")
	}

	// Initial registration.
	manifest, err := capability.AssembleManifest(ctx, exo, pub, opts)
	if err != nil {
		return fmt.Errorf("assemble manifest: %w", err)
	}
	logIfCapClamped(opts.MemoryCapPct, manifest.DeclaredMemoryCapPct)
	if err := register(ctx, cfg.CoordinatorURL, cfg.UserID, priv, pub, manifest); err != nil {
		return fmt.Errorf("initial registration failed: %w", err)
	}
	log.Printf("[agent] registered with coordinator %s as node %s", cfg.CoordinatorURL, manifest.NodeID)

	// Secure Enclave attestation is opt-in (--attempt-enclave-attestation) and
	// OFF by default — see Config.AttemptEnclaveAttestation. Most builds from
	// source can't use it (needs an Apple Developer Program-signed binary), so
	// it must never be attempted silently: that would print a failure line on
	// every single startup for the vast majority of contributors, which reads
	// as a bug rather than an optional, mostly-irrelevant capability.
	if cfg.AttemptEnclaveAttestation && manifest.HasSecureEnclave {
		enclaveSigner := attestation.NewSigner()
		go func() {
			if err := AttestSecureEnclave(ctx, cfg.CoordinatorURL, manifest.NodeID, priv, enclaveSigner); err != nil {
				log.Printf("[agent] secure enclave attestation not available on this build (requires a properly code-signed binary; job routing falls back to no-attestation): %v", err)
				return
			}
			log.Printf("[agent] secure enclave attestation verified by coordinator")
		}()
	}

	// chaosActive gates incoming jobs during a simulated downtime window — shared
	// between the job server (refuses jobs while true) and the heartbeat loop
	// (skips refresh while true, so the coordinator's own liveness TTL marks this
	// node stale independent of chaos logic). Always false when ChaosDowntimePct is 0.
	var chaosActive atomic.Bool

	// scheduleActive mirrors chaosActive but is driven by the operator's own
	// contribution schedule ("share overnight, not during my working hours")
	// instead of simulated churn — same gating mechanism, different trigger.
	// Starts true (available) so a node isn't dark for the ~RefreshInterval
	// gap before the first schedule check runs.
	var scheduleActive atomic.Bool
	scheduleActive.Store(true)

	// Job server: serves /health + /detect always (OIMMenuBar polls /detect on
	// loopback), and inbound /v1/chat/completions ONLY in push mode. In pull
	// mode the coordinator never dials in, so bind loopback-only — the inbound
	// job handler stays present but unreachable from the LAN, and work arrives
	// exclusively over the outbound claim loop below.
	nodeID := manifest.NodeID
	srv := buildJobServer(runner, exo, cfg.CapacityPct, nodeID, cfg.CoordinatorURL, cfg.ExoURL, priv, ecdhPriv, &chaosActive, &scheduleActive, reachabilityEndpoint, portMappingStatus)
	serverAddr := listenAddr
	if pullMode {
		if _, port, splitErr := net.SplitHostPort(listenAddr); splitErr == nil {
			serverAddr = "127.0.0.1:" + port
		}
	}
	ln, err := net.Listen("tcp", serverAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", serverAddr, err)
	}
	go func() {
		scheme := "http"
		if nodeTLSEnabled {
			scheme = "https"
		}
		log.Printf("[agent] serving jobs at %s://%s", scheme, serverAddr)
		if err := httptls.Serve(srv, ln, cfg.TLSCert, cfg.TLSKey); err != nil && err != http.ErrServerClosed {
			log.Printf("[agent] job server error: %v", err)
		}
	}()

	// Pull loop: in pull mode, the node claims work over its own outbound
	// connection to the coordinator and posts results back — the whole reason
	// no inbound reachability is needed. Runs until ctx is canceled. Respects
	// the same chaos/schedule gating as the inbound handler (won't claim while
	// paused). Push nodes skip this entirely.
	if pullMode {
		go runPullLoop(ctx, cfg, priv, nodeID, runner, ecdhPriv, &chaosActive, &scheduleActive)
	}

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
			// Re-read the contribution schedule live from disk every tick —
			// unlike the rest of Config, a schedule that only took effect on
			// restart would defeat the point ("share overnight" has to start
			// and stop while the process keeps running). CLI flags for
			// schedule write through to this same file at startup, so both
			// configuration paths converge here.
			savedCfg, _ := nodeconfig.Load()
			wasScheduled := scheduleActive.Load()
			isScheduled := savedCfg.Schedule.IsActiveNow(time.Now())
			scheduleActive.Store(isScheduled)
			if wasScheduled && !isScheduled {
				log.Printf("[agent] outside the configured contribution schedule — pausing job serving until the window reopens")
			} else if !wasScheduled && isScheduled {
				log.Printf("[agent] back within the configured contribution schedule — resuming")
			}
			if !isScheduled {
				// Skip this heartbeat so the coordinator's liveness TTL marks
				// this node stale on its own, same as the chaos path below.
				continue
			}

			if chaosActive.Load() {
				// Already in a simulated downtime window — skip this heartbeat so the
				// coordinator's own liveness TTL marks this node stale on its own.
				continue
			}
			if cfg.ChaosDowntimePct > 0 && rand.Float64()*100 < cfg.ChaosDowntimePct {
				downtime := time.Duration(20+rand.Intn(40)) * time.Second // 20-60s
				chaosActive.Store(true)
				log.Printf("[agent] chaos: simulating %s of downtime", downtime)
				go func() {
					time.Sleep(downtime)
					chaosActive.Store(false)
					log.Printf("[agent] chaos: recovered, resuming heartbeats")
				}()
				continue
			}
			fresh, err := capability.AssembleManifest(ctx, exo, pub, opts)
			if err != nil {
				log.Printf("[agent] manifest refresh error: %v", err)
				continue
			}
			logIfCapClamped(opts.MemoryCapPct, fresh.DeclaredMemoryCapPct)
			if err := refresh(ctx, cfg.CoordinatorURL, nodeID, priv, fresh); err != nil {
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
			if err := SubmitBenchmarkResult(ctx, cfg.CoordinatorURL, nodeID, priv, sig); err != nil {
				log.Printf("[agent] submit benchmark result: %v", err)
			} else {
				log.Printf("[agent] submitted benchmark: %.1f tok/s decode", sig.TokensPerSecDecode)
			}
		}
	}
}

// buildJobServer constructs the HTTP mux that accepts inference jobs from the coordinator
// and exposes /detect + /config for the dashboard Node Setup tab.
// priv is this node's registration private key — used to sign job-outcome reports
// so the coordinator can verify they came from this node, not a forged source.
// chaosActive, when set, makes this node refuse both health checks and jobs —
// simulating real downtime so the coordinator marks it unreachable on the failed
// dispatch, not just stale on a missed heartbeat. Always nil-safe / false in
// production; only simulation nodes set ChaosDowntimePct > 0.
// scheduleActive is the operator-controlled counterpart: false outside the
// contributor's configured sharing window (nodeconfig.Schedule) — same
// refuse-jobs effect as chaos, but deliberate rather than simulated.
func buildJobServer(runner *jobrunner.Runner, exo *exoadapter.Client, capPct float64, nodeID, coordinatorURL, exoURL string, priv []byte, ecdhPriv *ecdh.PrivateKey, chaosActive, scheduleActive *atomic.Bool, reachabilityEndpoint, portMappingStatus string) *http.Server {
	mux := http.NewServeMux()

	// Health endpoint for coordinator liveness checks.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if chaosActive.Load() || !scheduleActive.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "down"})
			return
		}
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

		// governor.SystemInfo() only ever sees the ONE machine oim node start is
		// running on — for a multi-device Exo cluster that badly under-reports
		// capacity (e.g. a 3-device 80 GB cluster showing as whatever the
		// smallest member has). Override with the cluster-aggregate view when
		// Exo reports one, same detection this node's actual CapabilityManifest
		// already uses (see capability.AssembleManifest) — the dashboard's "what
		// can I contribute" panel must never show a different number than what
		// the mesh itself was told.
		totalRAMGB := sysInfo["total_ram_gb"]
		availableRAMGB := sysInfo["available_ram_gb"]
		usedPct := sysInfo["used_pct"]
		isCluster := false
		var clusterDeviceCount int
		var clusterChipFamilies []string
		var safeContributableGB float64
		if cluster, clusterErr := capability.DetectClusterNode(r.Context(), exo); clusterErr == nil && cluster.IsCluster && cluster.TotalMemGB > 0 {
			isCluster = true
			clusterDeviceCount = cluster.DeviceCount
			clusterChipFamilies = cluster.ChipFamilies
			safeContributableGB = round2(cluster.SafeContributableGB)
			totalRAMGB = round2(cluster.TotalMemGB)
			availableRAMGB = round2(cluster.TotalAvailableGB)
			if cluster.TotalMemGB > 0 {
				usedPct = round2(100 * (1 - cluster.TotalAvailableGB/cluster.TotalMemGB))
			}
		}

		// Best-effort per-device breakdown for the Node Setup topology diagram.
		// nil (omitted) whenever Exo is unreachable or hasn't formed a topology
		// yet — the dashboard falls back to the simple RAM bar in that case.
		var deviceTopology *capability.DeviceTopology
		if exoHealthy {
			deviceTopology, _ = capability.GetDeviceTopology(r.Context(), exo)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"node_id":               nodeID,
			// reachability_endpoint/port_mapping: what the dashboard/OIMMenuBar
			// need to tell a user WHETHER their node is actually reachable by
			// the coordinator, not just registered — a node can register and
			// look "Running" while every real job dispatch to it silently
			// fails, which is exactly the bug this pair of fields exists to
			// make visible instead of silent. port_mapping is one of
			// "auto" (UPnP/NAT-PMP succeeded), "manual" (an explicit
			// --reachability-endpoint was configured), or "unavailable"
			// (neither worked — this node likely isn't reachable from outside
			// its own network).
			"reachability_endpoint": reachabilityEndpoint,
			"port_mapping":          portMappingStatus,
			"platform":              sysInfo["platform"],
			"is_apple_silicon":      sysInfo["is_apple_silicon"],
			"total_ram_gb":          totalRAMGB,
			"available_ram_gb":      availableRAMGB,
			"used_pct":              usedPct,
			"is_cluster":            isCluster,
			"cluster_device_count":  clusterDeviceCount,
			"cluster_chip_families": clusterChipFamilies,
			"safe_contributable_gb": safeContributableGB,
			"device_topology":       deviceTopology,
			"has_secure_enclave":    protocol.CheckSecureEnclaveAvailable(),
			"is_foregrounded":       governor.IsForegrounded(),
			"exo_healthy":           exoHealthy,
			"exo_url":               exoURL,
			"models":                models,
			"config":                cfg,
			"schedule_active":       scheduleActive.Load(),
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
		// Invalid values are the caller's fault → 400, not a server 500. Save
		// re-validates too, but checking here lets us distinguish bad input
		// (400) from an actual filesystem write failure (500).
		if err := nodeconfig.Validate(cfg); err != nil {
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
		if chaosActive.Load() || !scheduleActive.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "node unreachable"})
			return
		}
		var req struct {
			Model                  string           `json:"model"`
			Messages               []map[string]any `json:"messages"`
			Stream                 bool             `json:"stream"`
			JobID                  string           `json:"oim_job_id"`
			ModelID                string           `json:"oim_model_id"`
			Lane                   string           `json:"oim_lane"`
			Sensitive              string           `json:"oim_sensitivity"`
			PayloadFetchURL        string           `json:"oim_payload_fetch_url"`
			PayloadEphemeralPubKey string           `json:"oim_ephemeral_public_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if req.Model == "" && req.ModelID != "" {
			req.Model = req.ModelID
		}
		// The coordinator sends the job ID as the X-OIM-Job-ID header (fast-lane
		// dispatch) and may also inline it in the body. Prefer the header so the
		// outcome we report back carries the SAME job ID the coordinator recorded —
		// otherwise its double-credit guard (creditedJobs) can't match our report
		// and the job gets credited twice (once observed, once self-reported).
		if hdr := r.Header.Get("X-OIM-Job-ID"); hdr != "" {
			req.JobID = hdr
		}

		// Encrypted-pointer path (M8): the coordinator never fetches the payload —
		// only this assigned node does, decrypting with the ECDH key this node
		// advertised in its manifest. req.Messages is the (empty/placeholder)
		// plaintext field in this mode; the REAL messages come from the pointer.
		messages := req.Messages
		if req.PayloadFetchURL != "" {
			decoded, decErr := fetchAndDecryptPayload(r.Context(), req.PayloadFetchURL, req.PayloadEphemeralPubKey, ecdhPriv)
			if decErr != nil {
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "payload decrypt: " + decErr.Error()})
				return
			}
			messages = decoded
		}

		spec := protocol.JobSpec{
			JobID:   req.JobID,
			ModelID: req.Model,
			Lane:    protocol.JobLaneFast,
		}

		isContinuation := r.Header.Get("X-OIM-Continuation") == "true"
		start := time.Now()

		// Streaming (fast lane only — background lane stays buffered/polling by
		// design): relays Exo's SSE response directly to w instead of returning
		// one buffered blob.
		if req.Stream && req.Lane != string(protocol.JobLaneBackground) {
			tokensDelivered, headersSent, execErr := runner.ExecuteFastLaneStreaming(r.Context(), spec, messages, capPct, w)
			latencyMs := float64(time.Since(start).Milliseconds())
			go func() {
				if err := ReportJobOutcome(context.Background(), coordinatorURL, nodeID, priv, spec.JobID, execErr == nil, latencyMs, tokensDelivered); err != nil {
					log.Printf("[agent] report outcome: %v", err)
				}
			}()
			if execErr != nil {
				// If nothing was written yet (pre-flight refused, Exo
				// unreachable), a normal JSON error still works. Once streaming
				// has begun there is no status code left to change — writeJSON
				// would just corrupt the response, so it's skipped in that case.
				if !headersSent {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": execErr.Error()})
				} else {
					log.Printf("[agent] streaming inference failed mid-stream job=%s: %v", spec.JobID, execErr)
				}
			}
			return
		}

		var result map[string]any
		var execErr error

		if req.Lane == string(protocol.JobLaneBackground) {
			result, execErr = runner.ExecuteBackgroundLane(r.Context(), spec, messages, capPct, isContinuation)
		} else {
			result, execErr = runner.ExecuteFastLane(r.Context(), spec, messages, capPct)
		}

		latencyMs := float64(time.Since(start).Milliseconds())
		tokensDelivered := extractTokenCount(result)
		go func() {
			// Non-blocking outcome report; ignore error — reporting failure must not disrupt the job.
			if err := ReportJobOutcome(context.Background(), coordinatorURL, nodeID, priv, spec.JobID, execErr == nil, latencyMs, tokensDelivered); err != nil {
				log.Printf("[agent] report outcome: %v", err)
			}
		}()

		if execErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": execErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	return &http.Server{
		Handler:           httpmw.SecurityHeaders(agentCORS(mux)),
		ReadHeaderTimeout: 10 * time.Second, // slow-loris guard, same bound as the coordinator (task #53)
	}
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

// refresh sends a signed manifest update to the coordinator's /nodes/{id}/refresh
// endpoint. Signed with priv — the same keypair used at registration — so the
// coordinator can verify this refresh actually came from this node.
func refresh(ctx context.Context, coordinatorURL, nodeID string, priv []byte, manifest *protocol.CapabilityManifest) error {
	req := protocol.RefreshRequest{
		Manifest:  *manifest,
		Timestamp: time.Now().Unix(),
	}
	signingBytes, err := req.SigningBytes()
	if err != nil {
		return fmt.Errorf("build signing bytes: %w", err)
	}
	sig, err := protocol.SignPayload(priv, signingBytes)
	if err != nil {
		return fmt.Errorf("sign refresh request: %w", err)
	}
	req.Signature = sig
	return postJSON(ctx, coordinatorURL+"/nodes/"+nodeID+"/refresh", req)
}

// fetchAndDecryptPayload retrieves the encrypted payload from fetchURL and
// decrypts it with this node's ECDH identity, returning the plaintext
// `messages` array the client encrypted. Node-side half of the M8
// encrypted-pointer path (internal/payloadcrypto).
func fetchAndDecryptPayload(ctx context.Context, fetchURL, ephemeralPubKeyB64 string, ecdhPriv *ecdh.PrivateKey) ([]map[string]any, error) {
	if ecdhPriv == nil {
		return nil, fmt.Errorf("this node has no ecdh identity (encrypted-pointer jobs unsupported)")
	}
	// Defense in depth: the coordinator already validated this URL before
	// dispatch (task #53's SSRF guard), but this node is the one actually
	// making the connection, so it re-checks rather than trusting the
	// coordinator blindly.
	if err := httpmw.ValidateFetchURL(fetchURL); err != nil {
		return nil, fmt.Errorf("fetch url: %w", err)
	}
	ephemeralPubKeyRaw, err := base64.StdEncoding.DecodeString(ephemeralPubKeyB64)
	if err != nil {
		return nil, fmt.Errorf("decode ephemeral public key: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build fetch request: %w", err)
	}
	// SafeFetchClient re-validates every redirect hop — the initial
	// ValidateFetchURL above only covers the first URL, and the default client
	// would otherwise follow a 302 to a loopback/metadata target unchecked.
	resp, err := httpmw.SafeFetchClient(30 * time.Second).Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("fetch payload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch payload: HTTP %d", resp.StatusCode)
	}
	// Cap at the same 8 MiB ceiling the rest of the mesh enforces on request
	// bodies (httpmw.DefaultMaxBodyBytes) — an oversized "ciphertext" from a
	// malicious or misbehaving fetch URL must not be read unbounded.
	combined, err := io.ReadAll(io.LimitReader(resp.Body, httpmw.DefaultMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	plaintext, err := payloadcrypto.Decrypt(ecdhPriv, ephemeralPubKeyRaw, combined)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	var messages []map[string]any
	if err := json.Unmarshal(plaintext, &messages); err != nil {
		return nil, fmt.Errorf("parse decrypted messages: %w", err)
	}
	return messages, nil
}

// coordinatorClient is the HTTP client for all coordinator calls (register,
// refresh, job-outcome, benchmark, attestation). Defaults to the standard
// client; ConfigureTLS swaps in a CA-pinned / skip-verify transport when the
// node targets an HTTPS coordinator with a private or self-signed cert.
var coordinatorClient = http.DefaultClient

// outboundLimiter caps this node's outbound coordinator calls (task #24). It is
// a client-side safety valve: even if a bug spins register/refresh in a tight
// loop, we won't flood the coordinator. Generous (well above the heartbeat
// cadence) so normal operation never waits.
var outboundLimiter = httpx.NewLimiter(20, 40)

// ConfigureTLS points coordinatorClient at the given trust settings. Call once
// at node startup before registering. A no-op when both args are zero-valued.
func ConfigureTLS(caFile string, skipVerify bool) error {
	c := *http.DefaultClient // shallow copy so we don't mutate the global default
	if err := httptls.ConfigureClient(&c, caFile, skipVerify); err != nil {
		return err
	}
	coordinatorClient = &c
	return nil
}

// postJSON POSTs body to url with retry-and-backoff (task #22). All callers
// (register / refresh / reputation job-outcome / benchmark) are idempotent, so
// retrying a transient failure is safe: a dropped connection or a coordinator
// 5xx/429 is retried with exponential backoff, while a 4xx (permanent) fails
// fast without hammering the server.
func postJSON(ctx context.Context, url string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return httpx.Do(ctx, httpx.DefaultRetry(), func() error {
		if err := outboundLimiter.Wait(ctx); err != nil {
			return err // context canceled while throttled — permanent
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("build request: %w", err) // permanent
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := coordinatorClient.Do(req)
		if err != nil {
			return httpx.Transient(fmt.Errorf("POST %s: %w", url, err)) // network — retry
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			rb, _ := io.ReadAll(resp.Body)
			e := fmt.Errorf("POST %s HTTP %d: %s", url, resp.StatusCode, rb)
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				return httpx.Transient(e) // transient server-side — retry
			}
			return e // 4xx — permanent
		}
		return nil
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func round2(f float64) float64 {
	return float64(int(f*100)) / 100
}

// logIfCapClamped surfaces it when capability.AssembleManifest's per-device
// safety logic reduced the operator's requested memory-cap percentage — e.g.
// because a cluster member (or this solo machine) is already low on free
// memory. Without this, a contribution silently shrinking below what the
// operator configured in the dashboard would look like a bug, not a safety
// feature working as intended. A 1-point-of-percentage tolerance avoids
// log spam from float rounding noise between heartbeats.
func logIfCapClamped(requestedPct, effectivePct float64) {
	if requestedPct-effectivePct > 0.01 {
		log.Printf("[agent] reducing memory contribution cap from %.0f%% to %.0f%% of total — some device(s) are low on free memory right now; this self-adjusts as usage changes",
			requestedPct*100, effectivePct*100)
	}
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

// reachabilityPort extracts the numeric port from a listen address like
// ":8765" — natmap.TryMap needs the internal port as an int, not a string.
func reachabilityPort(listenAddr string) (int, error) {
	_, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return 0, fmt.Errorf("parse listen address %q: %w", listenAddr, err)
	}
	return strconv.Atoi(port)
}

// resolveReachabilityEndpoint converts a listen address like ":8765" to a full URL
// that the coordinator can reach back on.
func resolveReachabilityEndpoint(listenAddr string, tlsEnabled bool) (string, error) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", fmt.Errorf("parse listen address %q: %w", listenAddr, err)
	}
	if host == "" {
		host = "localhost"
	}
	scheme := "http://"
	if tlsEnabled {
		scheme = "https://"
	}
	return scheme + host + ":" + port, nil
}
