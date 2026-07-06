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

// Directory / librarian server — global discovery layer.
// This instance must never see job payloads or sit in the inference data path (proposal §7.1).
// Run two instances with --peer pointing at each other for bootstrap-stage HA.
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
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-inference-mesh/oim/internal/directory"
	"github.com/open-inference-mesh/oim/internal/httpmw"
	"github.com/open-inference-mesh/oim/internal/httptls"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/version"
)

// directoryMaxConcurrentRequests bounds total in-flight requests across the
// whole directory server — the DDoS floor a per-IP limiter can't provide
// against a distributed flood (task #53), mirroring the coordinator.
const directoryMaxConcurrentRequests = 256

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var listenAddr, peerURL string
	var podTTLSec int
	var corsOrigins []string
	var tlsCert, tlsKey string
	var podPinsPath, authorizedPodsPath string
	var rateLimitRPS, rateLimitBurst float64
	var trustedProxies []string

	cmd := &cobra.Command{
		Use:   "oim-directory",
		Short: "Open Inference Mesh directory server",
		Long: `oim-directory is the centralized discovery layer (librarian).
Pod coordinators push aggregate health digests here; clients query it to
discover which pods can serve a given model. The directory never sees
individual node data, job payloads, or settlement data (proposal §7.1).

Bootstrap-stage HA: run two instances with --peer pointing at each other.
Each instance forwards received digests to its peer — one-hop only, no loops.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDirectory(listenAddr, peerURL, time.Duration(podTTLSec)*time.Second, corsOrigins, tlsCert, tlsKey, podPinsPath, authorizedPodsPath, rateLimitRPS, rateLimitBurst, trustedProxies)
		},
	}
	cmd.Flags().StringVar(&listenAddr, "listen", ":9100", "Address to listen on")
	cmd.Flags().StringVar(&peerURL, "peer", "", "Peer directory URL for bootstrap-stage HA (empty = standalone)")
	cmd.Flags().IntVar(&podTTLSec, "pod-ttl", 120, "Seconds before a pod digest is considered stale")
	cmd.Flags().StringSliceVar(&corsOrigins, "cors-origin", nil, "Allowed browser origin(s) for CORS (repeatable or comma-separated; empty = allow any origin, dev-friendly default)")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "Path to the TLS certificate (PEM). Set with --tls-key to serve HTTPS; unset serves plain HTTP")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "Path to the TLS private key (PEM). Required together with --tls-cert")
	cmd.Flags().StringVar(&podPinsPath, "pod-pins-path", "directory_pod_pins.json", "Where to persist trust-on-first-use pod_id->pubkey pins (task #52, M7) so a restart doesn't reset trust and let an impersonator claim an existing pod_id")
	cmd.Flags().StringVar(&authorizedPodsPath, "authorized-pods", "", "Path to a JSON {pod_id: hex_pubkey} allowlist. When set, ONLY listed pod_ids are accepted (no trust-on-first-use auto-learning) — for deployments that want to enumerate federation membership explicitly")
	cmd.Flags().Float64Var(&rateLimitRPS, "rate-limit-rps", 20.0, "Sustained requests per second allowed per client IP (0 disables). The directory is publicly exposed and decodes JSON POST bodies, so it needs the same per-IP floor as the coordinator")
	cmd.Flags().Float64Var(&rateLimitBurst, "rate-limit-burst", 40.0, "Burst capacity per client IP on top of the sustained rate")
	cmd.Flags().StringSliceVar(&trustedProxies, "trusted-proxy", nil, "IP or CIDR of reverse proxies (e.g. a fronting nginx) whose X-Forwarded-For may be trusted to identify the real client for rate limiting. Without this, requests behind a proxy all share one bucket (the proxy's IP). Ignored for any peer not in this list, since XFF is otherwise spoofable")
	return cmd
}

func runDirectory(listenAddr, peerURL string, podTTL time.Duration, corsOrigins []string, tlsCert, tlsKey, podPinsPath, authorizedPodsPath string, rateLimitRPS, rateLimitBurst float64, trustedProxies []string) error {
	log.Printf("[directory] oim-directory %s", version.String())
	store := directory.NewPodStore(podTTL)
	gossip := directory.NewGossip(store, peerURL)

	var pins *directory.PinStore
	var err error
	if authorizedPodsPath != "" {
		pins, err = directory.NewAllowlistPinStore(authorizedPodsPath)
		if err != nil {
			return fmt.Errorf("load authorized-pods allowlist: %w", err)
		}
		log.Printf("[directory] strict allowlist mode: only pod_ids in %s are accepted", authorizedPodsPath)
	} else {
		pins, err = directory.NewPinStore(podPinsPath)
		if err != nil {
			return fmt.Errorf("load pod pin store: %w", err)
		}
		log.Printf("[directory] trust-on-first-use pod pinning: %s", podPinsPath)
	}

	mux := http.NewServeMux()

	// POST /pods/register — pod coordinator reports aggregate health.
	// Verified against pins BEFORE being stored or gossiped (task #52, M7) —
	// an unsigned digest, or one signed by a key other than the one already
	// pinned/allowlisted for that pod_id, is rejected outright rather than
	// silently overwriting the topology entry for an existing pod.
	mux.HandleFunc("POST /pods/register", func(w http.ResponseWriter, r *http.Request) {
		var digest protocol.PodHealthDigest
		if err := json.NewDecoder(r.Body).Decode(&digest); err != nil {
			writeErr(w, http.StatusBadRequest, "parse digest: "+err.Error())
			return
		}
		if err := pins.Verify(digest); err != nil {
			log.Printf("[directory] REJECTED registration: %v", err)
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		gossip.ReceivePodDigest(digest)
		log.Printf("[directory] registered pod=%s region=%s models=%v score=%.2f nodes≈%d",
			digest.PodID, digest.RegionHint, digest.ServableModelIDs,
			digest.AggregateHealthScore, digest.NodeCountApprox)
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "registered",
			"pod_id": digest.PodID,
		})
	})

	// GET /pods?model_id=...&quantization=... — discovery query.
	// Returns all live pods that report serving the requested model.
	// Note: quantization is not filtered at this layer (pod digest is aggregate-only);
	// callers must contact the pod coordinator for per-variant availability.
	mux.HandleFunc("GET /pods", func(w http.ResponseWriter, r *http.Request) {
		modelID := r.URL.Query().Get("model_id")
		var pods []string
		if modelID != "" {
			pods = store.QueryForModel(modelID)
		} else {
			pods = store.AllPodIDs()
		}
		if pods == nil {
			pods = []string{}
		}
		writeJSON(w, http.StatusOK, protocol.DirectoryQueryResult{
			MatchingPods: pods,
			QueriedAt:    time.Now().UTC().Format(time.RFC3339),
		})
	})

	// POST /gossip/digest — receive a forwarded digest from a peer directory.
	// Stored directly; NOT re-propagated (prevents loop amplification). Verified
	// against the same pin/allowlist store as a direct registration — a peer
	// directory is a transport, not an authority; it forwarding a digest doesn't
	// make an unsigned or impersonating one trustworthy.
	mux.HandleFunc("POST /gossip/digest", func(w http.ResponseWriter, r *http.Request) {
		var digest protocol.PodHealthDigest
		if err := json.NewDecoder(r.Body).Decode(&digest); err != nil {
			writeErr(w, http.StatusBadRequest, "parse digest: "+err.Error())
			return
		}
		if err := pins.Verify(digest); err != nil {
			log.Printf("[directory] REJECTED gossiped digest: %v", err)
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		store.Upsert(digest) // direct store, no further propagation
		log.Printf("[directory] gossip received pod=%s from peer", digest.PodID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "stored"})
	})

	// GET /health — aggregate status for monitoring.
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"pod_count": store.LiveCount(),
			"peer":      peerURL,
		})
	})

	// GET /topology — full pod graph for dashboards: all live pods with aggregate stats
	// and coordinator URLs so clients can drill into per-node detail.
	mux.HandleFunc("GET /topology", func(w http.ResponseWriter, r *http.Request) {
		pods := store.AllPods()
		if pods == nil {
			pods = []protocol.PodHealthDigest{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"pods":       pods,
			"pod_count":  len(pods),
			"queried_at": time.Now().UTC().Format(time.RFC3339),
		})
	})

	// GET / — index for browsers and health checkers hitting the root.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"service":   "oim-directory",
			"status":    "ok",
			"pod_count": store.LiveCount(),
			"endpoints": []string{
				"GET  /health",
				"GET  /topology",
				"GET  /pods?model_id=<id>",
				"POST /pods/register",
				"POST /gossip/digest",
			},
		})
	})

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	proxyNets, err := httpmw.ParseTrustedProxies(trustedProxies)
	if err != nil {
		return err
	}
	limiter := httpmw.NewRateLimiter(rateLimitRPS, rateLimitBurst)
	defer limiter.Stop()
	if rateLimitRPS > 0 {
		log.Printf("[directory] rate limiting enabled: %.1f req/s per IP, burst %.0f", rateLimitRPS, rateLimitBurst)
	}

	// Same DDoS floor as the coordinator (task #53): the directory is publicly
	// exposed and decodes JSON bodies on /pods/register and /gossip/digest, so
	// it needs the per-IP rate limit, request-body cap, and a global in-flight
	// concurrency limit, not just headers/CORS. Without the body cap an
	// oversized POST is read unbounded into memory even though the coordinator
	// is protected.
	srv := &http.Server{
		Handler: httpmw.SecurityHeaders(
			corsMiddleware(corsOrigins,
				httpmw.MaxBodyBytes(httpmw.DefaultMaxBodyBytes,
					httpmw.LimitConcurrency(directoryMaxConcurrentRequests,
						httpmw.RateLimitByIP(limiter, proxyNets, mux))))),
		ReadHeaderTimeout: 10 * time.Second, // slow-loris guard, same bound as the coordinator (task #53)
	}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Println("[directory] shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	scheme := "http"
	if httptls.Enabled(tlsCert, tlsKey) {
		scheme = "https"
		httptls.WarnIfExpiringSoon(tlsCert, 30*24*time.Hour, "directory")
	} else {
		log.Printf("[directory] WARNING: serving PLAINTEXT HTTP — set --tls-cert/--tls-key before exposing beyond localhost")
	}
	log.Printf("[directory] listening on %s (%s peer=%q pod-ttl=%s)", listenAddr, scheme, peerURL, podTTL)
	if err := httptls.Serve(srv, ln, tlsCert, tlsKey); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// corsMiddleware allows browser requests from allowedOrigins. An empty
// allowedOrigins means "allow any origin" — the dev-friendly default. Operators
// exposing this directory to real user traffic should pass --cors-origin.
func corsMiddleware(allowedOrigins []string, h http.Handler) http.Handler {
	allowAll := len(allowedOrigins) == 0
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		switch {
		case allowAll:
			w.Header().Set("Access-Control-Allow-Origin", "*")
		case origin != "" && allowed[origin]:
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
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
