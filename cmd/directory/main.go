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
	"github.com/open-inference-mesh/oim/internal/protocol"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var listenAddr, peerURL string
	var podTTLSec int

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
			return runDirectory(listenAddr, peerURL, time.Duration(podTTLSec)*time.Second)
		},
	}
	cmd.Flags().StringVar(&listenAddr, "listen", ":9100", "Address to listen on")
	cmd.Flags().StringVar(&peerURL, "peer", "", "Peer directory URL for bootstrap-stage HA (empty = standalone)")
	cmd.Flags().IntVar(&podTTLSec, "pod-ttl", 120, "Seconds before a pod digest is considered stale")
	return cmd
}

func runDirectory(listenAddr, peerURL string, podTTL time.Duration) error {
	store := directory.NewPodStore(podTTL)
	gossip := directory.NewGossip(store, peerURL)

	mux := http.NewServeMux()

	// POST /pods/register — pod coordinator reports aggregate health.
	mux.HandleFunc("POST /pods/register", func(w http.ResponseWriter, r *http.Request) {
		var digest protocol.PodHealthDigest
		if err := json.NewDecoder(r.Body).Decode(&digest); err != nil {
			writeErr(w, http.StatusBadRequest, "parse digest: "+err.Error())
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
	// Stored directly; NOT re-propagated (prevents loop amplification).
	mux.HandleFunc("POST /gossip/digest", func(w http.ResponseWriter, r *http.Request) {
		var digest protocol.PodHealthDigest
		if err := json.NewDecoder(r.Body).Decode(&digest); err != nil {
			writeErr(w, http.StatusBadRequest, "parse digest: "+err.Error())
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

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	srv := &http.Server{Handler: mux}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-quit
		log.Println("[directory] shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	log.Printf("[directory] listening on %s (peer=%q pod-ttl=%s)", listenAddr, peerURL, podTTL)
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
