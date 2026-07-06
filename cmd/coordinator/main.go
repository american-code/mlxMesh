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

// Pod coordinator server — one per geographic/latency pod.
// Routing decisions happen here; the directory layer only does discovery (proposal §7.1).
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/directory"
	"github.com/open-inference-mesh/oim/internal/economics"
	"github.com/open-inference-mesh/oim/internal/federation"
	"github.com/open-inference-mesh/oim/internal/httpmw"
	"github.com/open-inference-mesh/oim/internal/httptls"
	"github.com/open-inference-mesh/oim/internal/identity"
	"github.com/open-inference-mesh/oim/internal/metrics"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/settlement"
	"github.com/open-inference-mesh/oim/internal/wallet"
)

// hashAPIKey returns the SHA-256 hex digest of a raw oim_* key — what actually
// gets stored and compared (task: secrets hardening). Never store or compare
// the raw key: a compromised database must not hand out live credentials, the
// same reasoning as never storing a plaintext password.
func hashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// apiKeyStore maps generated oim_* API keys ↔ user IDs. Only the SHA-256 hash
// of a key is ever stored (byKey/DB) — the raw key exists only transiently, in
// the return value of generate(), which the caller must show the user once
// ("store this, it cannot be retrieved again" — with hashed storage that claim
// is now literally true, not just aspirational).
//
// Both directions so we can look up by key (auth) and by user (revoke/check).
//
// db is nil for a pure in-memory store (tests, or a coordinator run without
// --db-path) — behavior is identical to before persistence was added. When db
// is set, keys survive a coordinator restart instead of silently invalidating
// every user's saved key on every deploy.
//
// Upgrade note: this project has no production deployment yet (see README's
// "not yet production-safe"), so there is no migration path from the old
// plaintext column — any key generated before this change simply won't
// validate anymore and must be regenerated. Deliberately not building a
// migration shim for a pre-release security fix.
type apiKeyStore struct {
	mu     sync.RWMutex
	byKey  map[string]string // sha256(oim_xxx) → userID
	byUser map[string]string // userID → sha256(oim_xxx)
	db     *sql.DB
}

func newAPIKeyStore() *apiKeyStore {
	return &apiKeyStore{
		byKey:  make(map[string]string),
		byUser: make(map[string]string),
	}
}

// newPersistentAPIKeyStore opens (creating if needed) a SQLite-backed store at
// dbPath and loads any previously issued keys. Uses its own connection to the
// same file the ledger persists to — SQLite supports multiple connections to
// one file, and keeping the two stores' schemas independent avoids coupling
// this CLI-local type to the settlement package's tested constructor.
func newPersistentAPIKeyStore(dbPath string) (*apiKeyStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open api key db %s: %w", dbPath, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS api_keys (user_id TEXT PRIMARY KEY, api_key TEXT NOT NULL UNIQUE)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate api_keys schema: %w", err)
	}
	s := &apiKeyStore{
		byKey:  make(map[string]string),
		byUser: make(map[string]string),
		db:     db,
	}
	rows, err := db.Query(`SELECT user_id, api_key FROM api_keys`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("load api_keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var userID, key string
		if err := rows.Scan(&userID, &key); err != nil {
			db.Close()
			return nil, fmt.Errorf("scan api_key row: %w", err)
		}
		s.byUser[userID] = key
		s.byKey[key] = userID
	}
	if err := rows.Err(); err != nil {
		db.Close()
		return nil, fmt.Errorf("iterate api_keys: %w", err)
	}
	return s, nil
}

func (s *apiKeyStore) generate(userID string) (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	key := "oim_" + hex.EncodeToString(raw)
	hashed := hashAPIKey(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		// INSERT OR REPLACE keyed on user_id (PRIMARY KEY) atomically replaces
		// any prior key for this user — same semantics as the in-memory revoke+set below.
		// Only the hash is ever written; the raw key never touches the database.
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO api_keys (user_id, api_key) VALUES (?, ?)`, userID, hashed); err != nil {
			return "", fmt.Errorf("persist api key: %w", err)
		}
	}
	if old, ok := s.byUser[userID]; ok {
		delete(s.byKey, old) // revoke existing key
	}
	s.byKey[hashed] = userID
	s.byUser[userID] = hashed
	return key, nil
}

func (s *apiKeyStore) lookup(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	uid, ok := s.byKey[hashAPIKey(key)]
	return uid, ok
}

func (s *apiKeyStore) revoke(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		if _, err := s.db.Exec(`DELETE FROM api_keys WHERE user_id = ?`, userID); err != nil {
			log.Printf("[coordinator] revoke api key for %s: persist delete failed: %v", userID, err)
		}
	}
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
	var listenAddr, podID, regionHint, publicURL, apiKey, dbPath string
	var directoryURLs []string
	var maxDispatchAttempts, directoryIntervalSec, grantPoWBits int
	var rateLimitRPS, rateLimitBurst, grantRateLimitPerHour, userQuotaPerHour float64
	var protectUserReads bool
	var corsOrigins, trustedProxies []string
	var tlsCert, tlsKey string
	var identityPath, federationKey, federationDBPath string
	var federationPollIntervalSec int

	cmd := &cobra.Command{
		Use:   "oim-coordinator",
		Short: "Open Inference Mesh pod coordinator",
		Long: `oim-coordinator is the pod coordinator for the Open Inference Mesh.
It maintains a live registry of contributing nodes within this geographic pod
and routes inference jobs to the best available node.

Nodes register with: oim node start --coordinator http://<this-host>:<port>
Optionally report aggregate health to a directory with --directory.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCoordinator(listenAddr, podID, regionHint, directoryURLs, publicURL, apiKey, dbPath,
				maxDispatchAttempts, time.Duration(directoryIntervalSec)*time.Second, rateLimitRPS, rateLimitBurst,
				corsOrigins, grantPoWBits, grantRateLimitPerHour, tlsCert, tlsKey,
				identityPath, federationKey, federationDBPath, time.Duration(federationPollIntervalSec)*time.Second,
				protectUserReads, userQuotaPerHour, trustedProxies)
		},
	}
	cmd.Flags().StringVar(&listenAddr, "listen", ":9000", "Address to listen on")
	cmd.Flags().StringVar(&podID, "pod-id", "pod-local", "Unique identifier for this pod")
	cmd.Flags().StringVar(&regionHint, "region", "us", "Geographic region hint (us/eu/apac)")
	cmd.Flags().StringVar(&publicURL, "public-url", "", "Public URL clients use to reach this coordinator (reported to directory)")
	cmd.Flags().IntVar(&maxDispatchAttempts, "max-attempts", 3, "Max nodes to try per fast-lane dispatch")
	cmd.Flags().StringSliceVar(&directoryURLs, "directory", nil, "Directory server URL(s) for pod discovery registration (repeatable or comma-separated; empty = disabled). Registers with ALL of them and, for reads (peer topology polling), tries each in order until one answers — no single directory instance is a hard dependency once more than one is configured (task #49)")
	cmd.Flags().IntVar(&directoryIntervalSec, "directory-interval", 60, "Seconds between directory health-digest reports")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "Bearer token required for write operations (empty = disabled)")
	cmd.Flags().StringVar(&dbPath, "db-path", "", "SQLite file for the credit ledger and API keys (empty = in-memory only, resets on restart)")
	cmd.Flags().Float64Var(&rateLimitRPS, "rate-limit-rps", 20.0, "Sustained requests per second allowed per client IP (0 disables rate limiting)")
	cmd.Flags().Float64Var(&rateLimitBurst, "rate-limit-burst", 40.0, "Burst capacity per client IP on top of the sustained rate")
	cmd.Flags().StringSliceVar(&corsOrigins, "cors-origin", nil, "Allowed browser origin(s) for CORS (repeatable or comma-separated; empty = allow any origin, dev-friendly default)")
	cmd.Flags().IntVar(&grantPoWBits, "grant-pow-bits", settlement.DefaultGrantPoWBits, "Proof-of-work difficulty (leading zero bits) required to claim a startup grant; raises the cost of minting disposable user_ids to farm grants (0 disables)")
	cmd.Flags().Float64Var(&grantRateLimitPerHour, "grant-rate-limit-per-hour", 6.0, "Startup-grant claims allowed per client IP per hour, independent of the general write-path rate limit (0 disables)")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "Path to the TLS certificate (PEM). Set with --tls-key to serve HTTPS; unset serves plain HTTP")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "Path to the TLS private key (PEM). Required together with --tls-cert")
	cmd.Flags().StringVar(&identityPath, "identity-path", "", "Path for this coordinator's own Ed25519 identity (task #52, M7) — signs PodHealthDigest and ledger-event federation data. Empty = ~/.config/oim/coordinator_identity.json. Distinct from any node's identity; two coordinators on one host need different paths")
	cmd.Flags().StringVar(&federationKey, "federation-key", "", "Bearer credential peer coordinators use to pull this pod's signed ledger-event history (GET /federation/ledger-events, /federation/audit/*) and this pod uses to pull theirs. Empty = federation witnessing disabled (task #52, M7 is opt-in like the other hardening features)")
	cmd.Flags().StringVar(&federationDBPath, "federation-db-path", "", "SQLite file for this pod's own signed ledger-event history and witnessed peer history (empty = in-memory only, resets on restart). Deliberately a SEPARATE flag from --db-path: coordinators sharing one --db-path/volume for ledger merging must NOT also share a federation store, or their self-event sequence numbers collide")
	cmd.Flags().IntVar(&federationPollIntervalSec, "federation-poll-interval", 30, "Seconds between polling peer pods (discovered via --directory) for new signed ledger events")
	cmd.Flags().BoolVar(&protectUserReads, "protect-user-reads", false, "Require auth on per-user read endpoints (GET /users/{id}/balance, GET /users/{id}/api-key): the caller must present the admin --api-key or that user's own oim_ key. Off by default so the public dashboard can read aggregate topology; turn ON for a public deployment so balances aren't enumerable by user_id. Aggregate reads (/topology, /nodes, /metrics) stay open")
	cmd.Flags().Float64Var(&userQuotaPerHour, "user-quota-per-hour", 0, "Per-account request quota: max requests per hour for a single authenticated user_id, layered on top of the per-IP --rate-limit-rps so one account can't abuse the API from many IPs (0 disables). Only applies to requests that resolve to a user via Bearer auth")
	cmd.Flags().StringSliceVar(&trustedProxies, "trusted-proxy", nil, "IP or CIDR of reverse proxies (e.g. a fronting nginx) whose X-Forwarded-For may be trusted to identify the real client for per-IP rate limiting. Without this, requests behind a proxy all share one bucket (the proxy's IP), making per-IP limits ineffective. Ignored for any peer not in this list, since XFF is otherwise spoofable")
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

func runCoordinator(listenAddr, podID, regionHint string, directoryURLs []string, publicURL, apiKey, dbPath string, maxAttempts int, directoryInterval time.Duration, rateLimitRPS, rateLimitBurst float64, corsOrigins []string, grantPoWBits int, grantRateLimitPerHour float64, tlsCert, tlsKey, identityPath, federationKey, federationDBPath string, federationPollInterval time.Duration, protectUserReads bool, userQuotaPerHour float64, trustedProxies []string) error {
	proxyNets, err := httpmw.ParseTrustedProxies(trustedProxies)
	if err != nil {
		return err
	}
	registry := coordinator.NewNodeRegistry()
	// Structured logging (task #20): OIM_LOG_FORMAT=json emits machine-parseable
	// JSON logs for aggregation; the default stays human-readable text. Key money
	// and security events (credit, debit) use slog with typed fields.
	if os.Getenv("OIM_LOG_FORMAT") == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	}

	coordReg := coordinator.NewCoordinationRegistry()
	walletMgr := wallet.NewManager()
	reservations := coordinator.NewReservationStore() // node-side pointer consumption (M8)
	mx := metrics.New()                               // observability counters/gauges, exposed at GET /metrics (task #20)
	assignments := coordinator.NewAssignmentStore()
	measurements := coordinator.NewMeasurementStore()
	settlementRecords := &settlementRecordStore{}
	capacitySrc := &podCapacitySource{registry: registry, measurements: measurements}

	// Registry/assignments stay in-memory: nodes self-heal via re-registration
	// within one heartbeat interval of a restart, so persisting them buys little.
	// The ledger and API keys are different — losing those on restart means real
	// financial data loss and silently invalidating every user's saved key, so
	// --db-path backs them with SQLite when set.
	var ledger *settlement.Ledger
	var apiKeys *apiKeyStore
	if dbPath != "" {
		var err error
		ledger, err = settlement.NewPersistentLedger(dbPath)
		if err != nil {
			return fmt.Errorf("open persistent ledger: %w", err)
		}
		apiKeys, err = newPersistentAPIKeyStore(dbPath)
		if err != nil {
			return fmt.Errorf("open persistent api key store: %w", err)
		}
		log.Printf("[coordinator] persisting ledger + api keys to %s", dbPath)
	} else {
		ledger = settlement.NewLedger()
		apiKeys = newAPIKeyStore()
		log.Printf("[coordinator] --db-path not set: ledger and api keys are in-memory only and will reset on restart")
	}

	// Coordinator identity + federated ledger witnessing (task #52, M7): a
	// coordinator's own Ed25519 keypair, distinct from any node's, used to
	// sign the PodHealthDigest sent to the directory and every credit-issuance
	// event in this pod's federation history — see internal/federation and
	// internal/directory's PinStore for what these signatures are checked
	// against on the receiving end.
	coordIdentityPath := identityPath
	if coordIdentityPath == "" {
		home, _ := os.UserHomeDir()
		coordIdentityPath = home + "/.config/oim/coordinator_identity.json"
	}
	coordPriv, coordPub, err := identity.LoadOrCreateAt(coordIdentityPath)
	if err != nil {
		return fmt.Errorf("load coordinator identity: %w", err)
	}
	log.Printf("[coordinator] identity public key: %s", hex.EncodeToString(coordPub))

	// federationDBPath is its OWN flag, not derived from --db-path: two
	// coordinators can (and, on the current EC2 seed, do) share one --db-path
	// volume/file for ledger merging — deriving the federation path from that
	// would make them silently share one federation store too, corrupting
	// each other's signed self-event sequence numbers. Explicit and separate
	// keeps this safe regardless of how --db-path is deployed.
	fedStore, err := federation.NewStore(federationDBPath)
	if err != nil {
		return fmt.Errorf("open federation store: %w", err)
	}
	if federationDBPath == "" {
		log.Printf("[coordinator] --federation-db-path not set: federation witnessing state is in-memory only and will reset on restart")
	}

	// Every credit this pod issues (grant or earned) becomes a signed,
	// sequenced federation event — so a peer pod that witnesses this pod's
	// history can catch a future balance claim that contradicts it (see
	// GET /federation/audit/{user_id} below). Runs synchronously on the credit
	// path but is local disk I/O only (no network), same cost class as the
	// ledger's own SQLite write it's piggybacking on.
	ledger.SetOnCredit(func(entry settlement.CreditEntry) {
		evtType := federation.EventEarnedContrib
		if entry.Origin == settlement.CreditOriginStartupGrant {
			evtType = federation.EventStartupGrant
		}
		evt := federation.LedgerEvent{
			PodID:     podID,
			Sequence:  fedStore.NextSequence(),
			EventType: evtType,
			UserID:    entry.UserID,
			Amount:    entry.Amount,
			IssuedAt:  entry.GrantedOrEarnedAt.UTC().Format(time.RFC3339Nano),
		}
		signed, err := federation.Sign(evt, coordPriv, coordPub)
		if err != nil {
			log.Printf("[coordinator] federation: sign event failed (credit still recorded in ledger): %v", err)
			return
		}
		if err := fedStore.AppendSelfEvent(signed); err != nil {
			log.Printf("[coordinator] federation: append event failed (credit still recorded in ledger): %v", err)
		}
	})

	// ctx is canceled on SIGINT/SIGTERM to drain the job queue workers cleanly.
	// Also deferred here so an early return (e.g. net.Listen failure) can't leak it —
	// cancelCtx is idempotent, so the later explicit call in the shutdown goroutine is safe.
	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	// MQTT-style bounded job queue: callers with X-OIM-Queue: true are held here
	// when all nodes are busy, rather than receiving an immediate 503.
	jobQueue := coordinator.NewJobQueue(ctx,
		coordinator.DefaultQueueCapacity,
		coordinator.DefaultQueueWorkers,
		registry, maxAttempts,
	)

	// nodeUsers maps node_id → user_id so earned credits reach the right account.
	var nodeUsers sync.Map // string → string

	// creditedJobs records job IDs already credited from the coordinator's OWN
	// observed token count (fast-lane). The node's self-reported /job-outcome must
	// not also credit these — otherwise a node double-earns, and could inflate the
	// self-report above what the coordinator actually saw (task #51: verified
	// earnings). Fast-lane earnings are therefore coordinator-authoritative; the
	// node's report is reputation-only for jobs the coordinator already observed.
	var creditedJobs sync.Map // jobID → struct{}

	// Separate, much stricter bucket just for startup-grant claims — the general
	// write-path limiter (constructed below, applied to the whole mux) is tuned
	// for legitimate high-frequency node refreshes, not for "how many free
	// credit grants can one IP mint per hour."
	grantLimiter := httpmw.NewRateLimiter(grantRateLimitPerHour/3600.0, 2)
	defer grantLimiter.Stop()

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

	// POST /nodes/{id}/refresh — updated CapabilityManifest.
	// Must be signed with the SAME keypair used at /nodes/register — an unsigned
	// refresh would let anyone hijack a victim node's ReachabilityEndpoint or
	// inflate its MeasuredSignature to win routing (Fable security review #2).
	mux.HandleFunc("POST /nodes/{id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var req protocol.RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "parse refresh request: "+err.Error())
			return
		}
		signingBytes, err := req.SigningBytes()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "build signing bytes: "+err.Error())
			return
		}
		if err := coordinator.VerifyNodeSignature(registry, nodeID, req.Timestamp, signingBytes, req.Signature); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		if err := registry.Refresh(nodeID, req.Manifest); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "refreshed"})
	})

	// POST /nodes/{id}/attest-enclave — proves possession of a Secure Enclave-backed
	// signing key, replacing trust in the self-declared manifest.HasSecureEnclave
	// boolean for SensitivityHighRequiresAttestation routing decisions (Fable
	// security review: self-declared attestation, unenforced privacy claims).
	// Must be signed with BOTH the enclave's own P-256 key (proves the key is real
	// and usable right now) AND the node's registered Ed25519 identity key (proves
	// this attestation is for the node that owns {id}, not an attacker's own enclave).
	mux.HandleFunc("POST /nodes/{id}/attest-enclave", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var req protocol.EnclaveAttestationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "parse attestation request: "+err.Error())
			return
		}
		req.NodeID = nodeID
		if err := coordinator.VerifyEnclaveAttestation(registry, req); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		if err := registry.MarkEnclaveAttested(nodeID, req.EnclavePublicKey); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[coordinator] node %s: secure enclave attested", nodeID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "attested"})
	})

	// DELETE /nodes/{id} — explicit deregister (optional; TTL handles stale nodes automatically)
	mux.HandleFunc("DELETE /nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		registry.MarkUnreachable(nodeID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "deregistered"})
	})

	// --- Job routing ---

	// POST /v1/reserve-node — pins a specific node for an upcoming
	// encrypted-pointer job (node-side pointer consumption, M8). A client needs
	// the recipient node's ECDH public key BEFORE it can encrypt a payload, but
	// normal dispatch only picks a node once the (already-encrypted) request
	// arrives. This runs the SAME eligibility/scoring the fast-lane router uses
	// (coordinator.PickBestNode), reserves that node for
	// coordinator.ReservationTTL, and returns its public key + a reservation ID.
	// The client then encrypts locally and submits /v1/chat/completions with
	// oim_reservation_id, which dispatches straight to the reserved node.
	mux.HandleFunc("POST /v1/reserve-node", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model       string `json:"model"`
			Sensitivity string `json:"sensitivity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "parse request: "+err.Error())
			return
		}
		sensitivity := protocol.SensitivityModerate
		if req.Sensitivity == string(protocol.SensitivityLow) {
			sensitivity = protocol.SensitivityLow
		} else if req.Sensitivity == string(protocol.SensitivityHighRequiresAttestation) {
			sensitivity = protocol.SensitivityHighRequiresAttestation
		}
		job := protocol.JobSpec{ModelID: req.Model, Lane: protocol.JobLaneFast, Sensitivity: sensitivity}
		node, err := coordinator.PickBestNode(job, registry)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if node.Manifest.ECDHPublicKey == "" {
			writeErr(w, http.StatusServiceUnavailable, "selected node does not support encrypted-pointer jobs")
			return
		}
		now := time.Now()
		resID, err := reservations.Create(coordinator.TargetFromManifest(node.Manifest), now)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"reservation_id":  resID,
			"node_id":         node.Manifest.NodeID,
			"ecdh_public_key": node.Manifest.ECDHPublicKey,
			"expires_at":      now.Add(coordinator.ReservationTTL).UTC().Format(time.RFC3339),
		})
	})

	// POST /v1/chat/completions — OpenAI-compatible fast-lane dispatch.
	// Accepts standard OpenAI format + optional oim_* extension fields.
	// Credit check: if X-OIM-User-ID header is present, balance is checked before dispatch
	// and debited on completion. Omit the header for dev/anonymous access.
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model       string           `json:"model"`
			Messages    []map[string]any `json:"messages"`
			MaxTokens   int              `json:"max_tokens"`
			Stream      bool             `json:"stream"`
			OIMJobID    string           `json:"oim_job_id"`
			OIMSensitiv string           `json:"oim_sensitivity"`
			OIMMaxPrice float64          `json:"oim_max_price_per_unit"`
			// On-device routing hint fields (all optional). A request omitting
			// them — every non-iOS client, curl, the Python agent, sim traffic —
			// is handled identically to a nil hint: the coordinator classifies
			// and routes normally. See internal/coordinator/hintvalidator.go.
			OIMHint            *coordinator.RouterHint `json:"oim_hint"`
			OIMSensOverride    string                  `json:"oim_sensitivity_override"`
			OIMPayloadHash     string                  `json:"oim_payload_hash"`
			OIMPayloadFetchURL string                  `json:"oim_payload_fetch_url"`
			OIMEphemeralPubKey string                  `json:"oim_ephemeral_public_key"`
			// OIMReservationID pins this job to the node reserved via
			// POST /v1/reserve-node (node-side pointer consumption, M8) — the
			// client encrypted its payload to THAT node's key, so this request
			// must dispatch there, not wherever normal routing would otherwise pick.
			OIMReservationID string `json:"oim_reservation_id"`
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
			estimatedCost := economics.ConsumerCost(economics.LaneFast, req.OIMSensitiv, maxTok)
			bal := ledger.GetBalance(userID)
			if bal.Total < estimatedCost {
				mx.Counter(`oim_rejections_total{reason="insufficient_credits"}`).Inc()
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

		// The coordinator's OWN classification of the job. Today it derives this
		// from the declared oim_sensitivity (default moderate); a future
		// coordinator-side classifier would replace this line. Crucially, this is
		// the authority the client can escalate but never fall below.
		coordinatorTier := protocol.SensitivityModerate
		if req.OIMSensitiv == string(protocol.SensitivityLow) {
			coordinatorTier = protocol.SensitivityLow
		} else if req.OIMSensitiv == string(protocol.SensitivityHighRequiresAttestation) {
			coordinatorTier = protocol.SensitivityHighRequiresAttestation
		}

		// Validate the on-device hint (nil for non-iOS clients) and resolve the
		// effective sensitivity. store is nil until per-requester hint-accuracy
		// history is wired, so weight is 0 and the coordinator re-classifies —
		// exactly today's behavior for hintless requests. The override can only
		// escalate; de-escalation is impossible (hintvalidator.go).
		sensitivity, _, _ := coordinator.ResolveRouting(userID, req.OIMHint, req.OIMSensOverride, coordinatorTier, nil)

		jobID := req.OIMJobID
		if jobID == "" {
			jobID = fmt.Sprintf("job-%d", time.Now().UnixNano())
		}

		// PayloadRef carries the content-address pointer in privacy mode; the
		// coordinator passes it (and the fetch URL + ephemeral pubkey) through to
		// the assigned node and NEVER fetches it. Empty for legacy/plaintext.
		payloadRef := req.OIMPayloadHash

		// SSRF guard (task #53): the coordinator hands the fetch URL to a node,
		// which DOES fetch it, so a malicious URL could make nodes hit internal
		// targets (cloud metadata, loopback admin). Reject non-http(s), loopback,
		// and link-local before the URL ever reaches a node.
		if req.OIMPayloadFetchURL != "" {
			if err := httpmw.ValidateFetchURL(req.OIMPayloadFetchURL); err != nil {
				mx.Counter(`oim_rejections_total{reason="ssrf"}`).Inc()
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}

		// Pointer-path attribution: when a request rides an encrypted pointer, the
		// device hosting/serving that ciphertext is a coordination participant
		// doing real work. It self-identifies via X-OIM-Pointer-Host so we credit
		// the served pointer to it. A stale/unknown ID is simply ignored (the
		// registry returns false) — attribution never affects routing or the reply.
		if payloadRef != "" {
			creditPointerHost(ledger, coordReg, walletMgr, r.Header.Get("X-OIM-Pointer-Host"), jobID)
		}

		job := protocol.JobSpec{
			JobID:                  jobID,
			ModelID:                req.Model,
			Lane:                   protocol.JobLaneFast,
			Sensitivity:            sensitivity,
			MaxPricePerUnit:        req.OIMMaxPrice,
			RedundancyDepth:        0,
			PayloadRef:             payloadRef,
			PayloadFetchURL:        req.OIMPayloadFetchURL,
			PayloadEphemeralPubKey: req.OIMEphemeralPubKey,
		}

		// Streaming (fast lane only): relays the assigned node's SSE response
		// directly to the client instead of buffering the whole reply. Scoped
		// out of the reservation (encrypted-pointer) path for now — combining
		// both is more surface area than needed yet; a reservation request with
		// stream:true is served buffered, same as before.
		if req.Stream && req.OIMReservationID == "" {
			servedBy, tokens, headersSent, streamErr := coordinator.DispatchFastLaneStreaming(r.Context(), job, req.Messages, registry, maxAttempts, w)
			if streamErr != nil {
				if !headersSent {
					writeErr(w, http.StatusServiceUnavailable, streamErr.Error())
				} else {
					// The client already received a partial SSE stream — the
					// connection is the only signal left; there is no status
					// code left to change at this point.
					log.Printf("[coordinator] streaming dispatch failed mid-stream job=%s: %v", jobID, streamErr)
				}
				return
			}
			mx.Counter(`oim_jobs_dispatched_total{lane="fast"}`).Inc()
			// A node that streamed content but sent no (or a malformed) trailing
			// SSE usage frame leaves tokens==0 — the coordinator-observed-billing
			// invariant (task #51) then correctly refuses to guess a cost, but
			// that silently means $0 for the consumer and no pay for the node.
			// Surfaced here rather than left silent, so a node/backend that
			// doesn't honor stream_options.include_usage shows up in logs/metrics
			// instead of quietly giving away free inference.
			if servedBy != "" && tokens == 0 {
				mx.Counter(`oim_rejections_total{reason="stream_missing_usage_frame"}`).Inc()
				log.Printf("[coordinator] streaming job=%s served_by=%s completed with no usage frame — not billed, node not credited", jobID, servedBy)
			}
			// Same observed-token credit/debit accounting as the buffered path
			// below — just sourced from the trailing SSE usage frame instead of
			// one buffered JSON blob (task #51's coordinator-observed guarantee
			// applies identically either way).
			if servedBy != "" && tokens > 0 {
				if _, dup := creditedJobs.LoadOrStore(jobID, struct{}{}); !dup {
					creditNodeEarning(ledger, &nodeUsers, servedBy, jobID, economics.LaneFast, req.OIMSensitiv, tokens)
					mx.Counter("oim_credits_total").Inc()
					mx.AddCounter("oim_tokens_served_total", int64(tokens))
				}
			}
			// tokens==0 means no observed usage — never invent a cost for it
			// (see the missing-usage-frame log above); skip the ledger write
			// entirely rather than recording a meaningless $0.00 debit.
			if userID != "" && tokens > 0 {
				actualCost := economics.ConsumerCost(economics.LaneFast, req.OIMSensitiv, tokens)
				if !ledger.DebitAccount(userID, actualCost, jobID) {
					log.Printf("[coordinator] debit race user=%s job=%s cost=%.4f", userID, jobID, actualCost)
				} else {
					mx.Counter("oim_debits_total").Inc()
					slog.Info("debit", "user", userID, "job", jobID, "tokens", tokens, "cost", actualCost)
				}
			}
			return
		}

		var result map[string]any
		var err error
		if req.OIMReservationID != "" {
			// Encrypted-pointer job: the client already encrypted its payload to a
			// SPECIFIC node's key via a prior POST /v1/reserve-node, so this must
			// dispatch there — any other node couldn't decrypt it. An
			// expired/unknown reservation fails outright rather than silently
			// falling back to normal routing, which would dispatch undecryptable
			// ciphertext to a node that can never serve it.
			res, ok := reservations.Resolve(req.OIMReservationID, time.Now())
			if !ok {
				mx.Counter(`oim_rejections_total{reason="reservation_expired"}`).Inc()
				writeErr(w, http.StatusConflict, "reservation_expired_or_unknown: re-reserve and re-encrypt")
				return
			}
			result, err = coordinator.DispatchToResolvedNode(r.Context(), job, req.Messages, registry, res.Target)
		} else {
			result, err = coordinator.DispatchFastLane(r.Context(), job, req.Messages, registry, maxAttempts)
		}
		if err != nil {
			// X-OIM-Queue: true — hold the request in the coordinator queue instead of 503.
			// Workers retry dispatch every ~400ms until a node accepts or the 30s timeout fires.
			// Not applicable to a reserved job — the reservation is already consumed
			// and specific to one node, so there is nothing generic left to queue.
			if r.Header.Get("X-OIM-Queue") == "true" && req.OIMReservationID == "" {
				result, err = jobQueue.Enqueue(r.Context(), job, req.Messages, coordinator.DefaultQueueTimeout)
			}
			if err != nil {
				writeErr(w, http.StatusServiceUnavailable, err.Error())
				return
			}
		}

		// Credit the serving node from the coordinator's OWN observed token count.
		// This is the authoritative earning path for fast-lane: the coordinator
		// proxied the response, so it counted the tokens itself — the node cannot
		// inflate its earnings by lying in /job-outcome (task #51). Marked in
		// creditedJobs so the later self-report can't double-credit.
		mx.Counter(`oim_jobs_dispatched_total{lane="fast"}`).Inc()
		observedTok := extractCompletionTokens(result, maxTok)
		if servedBy, _ := result["oim_served_by_node_id"].(string); servedBy != "" && observedTok > 0 {
			if _, dup := creditedJobs.LoadOrStore(jobID, struct{}{}); !dup {
				creditNodeEarning(ledger, &nodeUsers, servedBy, jobID, economics.LaneFast, req.OIMSensitiv, observedTok)
				mx.Counter("oim_credits_total").Inc()
				mx.AddCounter("oim_tokens_served_total", int64(observedTok))
			}
		}

		// Debit after successful dispatch. The consumer pays the full matrix cost;
		// the serving node earned only (1 − house edge) of it above, with the
		// remainder booked to the treasury.
		if userID != "" {
			actualTok := extractCompletionTokens(result, maxTok)
			actualCost := economics.ConsumerCost(economics.LaneFast, req.OIMSensitiv, actualTok)
			if !ledger.DebitAccount(userID, actualCost, jobID) {
				// Balance may have shifted between check and debit (concurrent requests).
				// Job is complete — log the race but don't fail the response.
				log.Printf("[coordinator] debit race user=%s job=%s cost=%.4f", userID, jobID, actualCost)
			} else {
				mx.Counter("oim_debits_total").Inc()
				slog.Info("debit", "user", userID, "job", jobID, "tokens", actualTok, "cost", actualCost)
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

	// POST /jobs/background/execute — actually run one recurrence cycle of an
	// assigned background job. /assign and /cycle only ever answered "which
	// node," never dispatched anything — there was no coordinator-mediated
	// execution path at all for background-lane jobs, which is why decomposition
	// (allow_decomposition=true) was dormant code with nothing that could ever
	// reach it. This endpoint closes that gap: it uses the JobSpec captured at
	// /assign time, so callers only need to resend job_id + messages per cycle.
	//
	// Decomposition path: when the assigned job has AllowDecomposition=true,
	// this routes through RouteWithDecomposition (splitting into sub-tasks,
	// dispatching them in parallel, verifying, merging). Otherwise it dispatches
	// once to whichever node ResolveForCycle picked for this cycle — the same
	// single-node behavior a caller doing this by hand would have gotten before.
	mux.HandleFunc("POST /jobs/background/execute", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JobID    string           `json:"job_id"`
			Messages []map[string]any `json:"messages"`
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

		if a.JobSpec.AllowDecomposition {
			result, err := coordinator.RouteWithDecomposition(r.Context(), a.JobSpec, req.Messages, registry, nil, maxAttempts, jobQueue)
			if err != nil {
				writeErr(w, http.StatusServiceUnavailable, err.Error())
				return
			}
			log.Printf("[coordinator] background/execute job=%s via decomposition", req.JobID)
			writeJSON(w, http.StatusOK, result)
			return
		}

		nodeID, isCont, err := coordinator.ResolveForCycle(a, registry)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		manifest, ok := registry.Manifest(nodeID)
		if !ok {
			writeErr(w, http.StatusServiceUnavailable, fmt.Sprintf("resolved node %s is no longer registered", nodeID))
			return
		}
		result, err := coordinator.DispatchToResolvedNode(r.Context(), a.JobSpec, req.Messages, registry, coordinator.TargetFromManifest(manifest))
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		// Credit the serving node from the coordinator's OWN observed token count,
		// at the background-lane rate (task #51). Like fast-lane, this makes the
		// earning coordinator-authoritative — the node's self-reported /job-outcome
		// can't inflate it. Recurring background jobs reuse the same job_id across
		// cycles, and each executed cycle is a distinct earning, so this is NOT
		// deduped by job_id (job-outcome no longer credits, so there's no double).
		if observedTok := extractCompletionTokens(result, 2048); observedTok > 0 {
			creditNodeEarning(ledger, &nodeUsers, nodeID, req.JobID, economics.LaneBackground, string(a.JobSpec.Sensitivity), observedTok)
		}
		log.Printf("[coordinator] background/execute job=%s node=%s continuation=%v", req.JobID, nodeID, isCont)
		writeJSON(w, http.StatusOK, result)
	})

	// --- Reputation / verification ---

	// POST /nodes/{id}/benchmark-result — node submits a fresh MeasuredSignature.
	// Stored in MeasurementStore; used by VerifyTierClaim to detect fraud (proposal §8.2/9.2).
	// Must be signed — an unsigned submission would let an attacker forge any
	// node's measured throughput and defeat the fraud check that reconciles against it.
	mux.HandleFunc("POST /nodes/{id}/benchmark-result", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var req protocol.BenchmarkResultRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "parse measurement: "+err.Error())
			return
		}
		signingBytes, err := req.SigningBytes()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "build signing bytes: "+err.Error())
			return
		}
		if err := coordinator.VerifyNodeSignature(registry, nodeID, req.Timestamp, signingBytes, req.Signature); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		measurements.Store(nodeID, &req.Measured)
		log.Printf("[coordinator] node %s submitted benchmark: %.1f tok/s decode", nodeID, req.Measured.TokensPerSecDecode)
		writeJSON(w, http.StatusOK, map[string]string{"status": "stored"})
	})

	// POST /nodes/{id}/job-outcome — node reports job completion. REPUTATION ONLY:
	// this endpoint no longer credits (task #51). Every earning path is now driven
	// by the coordinator's OWN observed token count — fast-lane proxy and
	// background-lane /execute both dispatch through the coordinator, so it counts
	// the tokens itself and a node cannot inflate earnings by self-reporting. The
	// signed outcome is kept for latency/success reputation and audit.
	mux.HandleFunc("POST /nodes/{id}/job-outcome", func(w http.ResponseWriter, r *http.Request) {
		nodeID := r.PathValue("id")
		var outcome protocol.JobOutcomeRequest
		if err := json.NewDecoder(r.Body).Decode(&outcome); err != nil {
			writeErr(w, http.StatusBadRequest, "parse outcome: "+err.Error())
			return
		}
		signingBytes, err := outcome.SigningBytes()
		if err != nil {
			writeErr(w, http.StatusBadRequest, "build signing bytes: "+err.Error())
			return
		}
		if err := coordinator.VerifyNodeSignature(registry, nodeID, outcome.Timestamp, signingBytes, outcome.Signature); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		log.Printf("[coordinator] job-outcome(reputation) node=%s job=%s success=%v latency=%.0fms tokens=%d",
			nodeID, outcome.JobID, outcome.Success, outcome.LatencyMs, outcome.TokensDelivered)
		writeJSON(w, http.StatusOK, map[string]string{"status": "recorded", "credit": "coordinator_observed"})
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
	// Stores the record regardless of verification_result OR signature validity —
	// failed-verification and even forged records are evidence for dispute
	// resolution, not noise to be silently dropped (proposal §10). But crediting
	// only happens when the record's signature verifies against the claimed node's
	// registered public key — otherwise anyone could POST a record with
	// verification_result:true and mint credits for any node_id (Fable security
	// review #1).
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

				pubKey, known := registry.PublicKey(nodeID)
				if !known {
					log.Printf("[coordinator] settlement record %s: node %s not registered, refusing credit", recordID, nodeID)
				} else if err := settlement.VerifySettlementRecord(record, pubKey); err != nil {
					log.Printf("[coordinator] settlement record %s: signature invalid, refusing credit: %v", recordID, err)
				} else if nodeID != "" && totalValue > 0 {
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
	// Idempotent forever, not just "within a session" — dedup is checked against
	// the ledger itself (see Ledger.ClaimStartupGrantOnce), so it survives a
	// coordinator restart and can't be farmed by bouncing the process.
	//
	// Per-user dedup alone doesn't stop Sybil farming: user_id is a free,
	// client-generated UUID (dashboard's getOrCreateUserId) — clearing
	// localStorage mints a fresh "user" and a fresh grant at zero cost. Two
	// independent mitigations close that gap (Fable security review, Sybil-farmable
	// grants): a dedicated per-IP rate limit far stricter than the general
	// write-path limiter, and a proof-of-work challenge that makes minting each
	// claimable identity cost real wall-clock CPU time instead of being free.
	mux.HandleFunc("POST /users/{id}/startup-grant", func(w http.ResponseWriter, r *http.Request) {
		if !grantLimiter.Allow(httpmw.ClientIP(r, proxyNets)) {
			writeErr(w, http.StatusTooManyRequests, "too many startup-grant claims from this address; try again later")
			return
		}
		userID := r.PathValue("id")
		var body struct {
			Nonce uint64 `json:"nonce"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
			writeErr(w, http.StatusBadRequest, "parse request: "+err.Error())
			return
		}
		if !settlement.VerifyProofOfWork(userID, body.Nonce, grantPoWBits) {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf(
				"missing or insufficient proof of work: need sha256(user_id||nonce) with %d leading zero bits", grantPoWBits))
			return
		}
		entry, err := settlement.IssueStartupGrant(ledger, userID, podID, capacitySrc, settlement.DEFAULT_DECAY_STEPS)
		if errors.Is(err, settlement.ErrStartupGrantAlreadyClaimed) {
			bal := ledger.GetBalance(userID)
			writeJSON(w, http.StatusOK, map[string]any{
				"amount": bal.GrantBalance,
				"status": "already_claimed",
			})
			return
		}
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
		if !authorizeUserRead(r, userID, protectUserReads, apiKey, apiKeys) {
			writeErr(w, http.StatusForbidden, "forbidden: reading this balance requires the account's own API key or the admin key")
			return
		}
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
		if !authorizeUserRead(r, userID, protectUserReads, apiKey, apiKeys) {
			writeErr(w, http.StatusForbidden, "forbidden: checking this key requires the account's own API key or the admin key")
			return
		}
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
			// iOS security/coordination participants — a separate list so the
			// dashboard can render them with a distinct icon and toggle the layer,
			// while the inference routers never see them.
			"coordination_nodes": coordReg.Snapshot(),
			"metrics": map[string]any{
				"queue_depth":      jobQueue.Depth(),
				"queue_capacity":   jobQueue.Capacity(),
				"backpressure_pct": jobQueue.BackpressurePct(),
				"total_in_flight":  registry.TotalInFlight(),
			},
		})
	})

	// POST /coordination/announce — an iOS device announces (or heartbeats) that
	// it is participating as a security/coordination layer (hosts encrypted
	// payload pointers). Additive: pods work identically with zero participants.
	mux.HandleFunc("POST /coordination/announce", func(w http.ResponseWriter, r *http.Request) {
		var p coordinator.CoordinationParticipant
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, "parse announce: "+err.Error())
			return
		}
		if p.DeviceID == "" {
			writeErr(w, http.StatusBadRequest, "device_id required")
			return
		}
		// Region is coordinator-assigned (not client-trusted) so the map/shield
		// layer always has a placement region. Fall back to the device's locale
		// hint only if this coordinator has no region of its own.
		p.Region = regionHint
		if p.Region == "" {
			p.Region = p.GeographicHint
		}
		coordReg.Announce(p, time.Now())
		writeJSON(w, http.StatusOK, map[string]any{"status": "announced", "device_id": p.DeviceID})
	})

	// POST /coordination/withdraw — clean departure of a coordination participant.
	mux.HandleFunc("POST /coordination/withdraw", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeviceID string `json:"device_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "parse withdraw: "+err.Error())
			return
		}
		coordReg.Withdraw(body.DeviceID)
		writeJSON(w, http.StatusOK, map[string]any{"status": "withdrawn"})
	})

	// --- Wallet: portable, recoverable account identity (internal/wallet) ---
	// The account address (hash of the account's Ed25519 pubkey) IS the ledger
	// user_id, so the same wallet key controls the same balance from any device.

	// POST /account/challenge {address} → a one-time nonce to sign.
	mux.HandleFunc("POST /account/challenge", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Address string `json:"address"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Address == "" {
			writeErr(w, http.StatusBadRequest, "address required")
			return
		}
		ch, err := walletMgr.IssueChallenge(body.Address, time.Now())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, ch)
	})

	// POST /account/auth {address, nonce, public_key, signature} — proves account
	// ownership; on success mints a session oim_ API key bound to the address.
	mux.HandleFunc("POST /account/auth", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Address   string `json:"address"`
			Nonce     string `json:"nonce"`
			PublicKey string `json:"public_key"` // base64 Ed25519 raw
			Signature string `json:"signature"`  // base64
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "parse auth: "+err.Error())
			return
		}
		pub, err1 := base64.StdEncoding.DecodeString(body.PublicKey)
		sig, err2 := base64.StdEncoding.DecodeString(body.Signature)
		if err1 != nil || err2 != nil {
			writeErr(w, http.StatusBadRequest, "public_key and signature must be base64")
			return
		}
		if err := walletMgr.VerifyChallenge(body.Address, body.Nonce, pub, sig, time.Now()); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		key, err := apiKeys.generate(body.Address)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "mint session key: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"address": body.Address, "api_key": key})
	})

	// POST /account/{address}/link-device {device_node_id, account_public_key,
	// signature} — binds a device's earnings to this account (account-signed).
	mux.HandleFunc("POST /account/{address}/link-device", func(w http.ResponseWriter, r *http.Request) {
		address := r.PathValue("address")
		var body struct {
			DeviceNodeID     string `json:"device_node_id"`
			AccountPublicKey string `json:"account_public_key"`
			Signature        string `json:"signature"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "parse link: "+err.Error())
			return
		}
		pub, err1 := base64.StdEncoding.DecodeString(body.AccountPublicKey)
		sig, err2 := base64.StdEncoding.DecodeString(body.Signature)
		if err1 != nil || err2 != nil {
			writeErr(w, http.StatusBadRequest, "account_public_key and signature must be base64")
			return
		}
		if err := walletMgr.LinkDevice(address, body.DeviceNodeID, pub, sig, time.Now()); err != nil {
			writeErr(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "linked", "device_node_id": body.DeviceNodeID, "account": address})
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
					"pod_id":             podID,
					"region":             regionHint,
					"nodes":              registry.Snapshot(),
					"coordination_nodes": coordReg.Snapshot(),
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

	// GET /federation/identity — this pod's coordinator identity (task #52,
	// M7). Public and non-sensitive, same sensitivity class as GET /health:
	// a pod_id and public key don't need protecting, only the ledger-event
	// history below does.
	mux.HandleFunc("GET /federation/identity", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, federation.IdentityResponse{
			PodID:     podID,
			PublicKey: hex.EncodeToString(coordPub),
		})
	})

	// GET /federation/ledger-events?since=N — this pod's own signed
	// credit-issuance history, for a peer to pull and witness (task #52, M7).
	// Gated by --federation-key rather than the general admin --api-key: it
	// exposes every user_id + amount this pod has ever credited, which is a
	// meaningfully bigger exposure than any single-user balance lookup, and
	// is meant for pod-operator-to-pod-operator federation, not end users.
	// GET requests bypass authMiddleware by design (dashboard reads need no
	// auth) so this checks its own bearer token rather than relying on it.
	mux.HandleFunc("GET /federation/ledger-events", func(w http.ResponseWriter, r *http.Request) {
		if !federationAuthorized(r, federationKey) {
			writeErr(w, http.StatusUnauthorized, "federation-key required")
			return
		}
		since := uint64(0)
		if s := r.URL.Query().Get("since"); s != "" {
			parsed, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "invalid since: "+err.Error())
				return
			}
			since = parsed
		}
		events := fedStore.SelfEventsSince(since)
		if events == nil {
			events = []federation.LedgerEvent{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"pod_id": podID, "events": events})
	})

	// GET /federation/audit/{user_id}?peer_endpoint=<url> — checks a peer
	// pod's LIVE, self-reported balance for user_id against that same peer's
	// own witnessed signed credit history (task #52, M7). A peer can spend
	// credits down over time (so balance < witnessed gross credits is
	// normal), but balance can never legitimately EXCEED everything that pod
	// has ever signed as credited to this user — if it does, the peer is
	// either minting credits its own signed history doesn't back, or
	// crediting silently without emitting an event at all. Either way, this
	// is the concrete "verifiable" check the debt-collector balance sum
	// (dashboard/src/api.ts fetchBalanceAllPods) can't provide on its own.
	mux.HandleFunc("GET /federation/audit/{user_id}", func(w http.ResponseWriter, r *http.Request) {
		if !federationAuthorized(r, federationKey) {
			writeErr(w, http.StatusUnauthorized, "federation-key required")
			return
		}
		userID := r.PathValue("user_id")
		peerEndpoint := r.URL.Query().Get("peer_endpoint")
		if peerEndpoint == "" {
			writeErr(w, http.StatusBadRequest, "peer_endpoint query param required")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		client := federation.NewClient()

		ident, err := client.FetchIdentity(ctx, peerEndpoint)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "fetch peer identity: "+err.Error())
			return
		}
		since := fedStore.WitnessedHighWatermark(ident.PodID)
		events, err := client.FetchEventsSince(ctx, peerEndpoint, federationKey, since)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "fetch peer events: "+err.Error())
			return
		}
		var rejectedEvents []string
		for _, evt := range events {
			if err := fedStore.StoreWitnessedEvent(evt); err != nil {
				rejectedEvents = append(rejectedEvents, fmt.Sprintf("seq=%d: %v", evt.Sequence, err))
			}
		}
		balance, err := client.FetchBalance(ctx, peerEndpoint, userID)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "fetch peer balance: "+err.Error())
			return
		}
		witnessed := fedStore.WitnessedGrossCredits(ident.PodID, userID)
		writeJSON(w, http.StatusOK, map[string]any{
			"peer_pod_id":              ident.PodID,
			"user_id":                  userID,
			"claimed_balance":          balance.Total,
			"witnessed_gross_credits":  witnessed,
			"consistent":               balance.Total <= witnessed,
			"rejected_events_detected": rejectedEvents, // non-empty = signature/fork red flag, independent of the balance check
		})
	})

	// GET /metrics/prometheus — Prometheus text exposition of coordinator
	// counters/gauges (task #20). Separate from the dashboard's JSON /metrics.
	// Live gauges are refreshed on each scrape from the registries.
	mux.HandleFunc("GET /metrics/prometheus", func(w http.ResponseWriter, r *http.Request) {
		mx.SetGauge("oim_nodes_registered", int64(len(registry.Snapshot())))
		mx.SetGauge("oim_coordination_participants", int64(coordReg.Count()))
		mx.SetGauge("oim_queue_depth", int64(jobQueue.Depth()))
		// Real-vs-simulated capacity split (task #49, progressive
		// decentralization) — the missing "parity" ingredient, exposed here
		// too so it's scrapeable without polling the directory. Memory/tps
		// gauges are int64-only, so scaled x1000 for sub-GB/sub-tok precision;
		// consumers divide back down.
		digest := registry.HealthDigest(podID, regionHint, publicURL)
		mx.SetGauge("oim_nodes_real", int64(digest.RealNodeCountApprox))
		mx.SetGauge("oim_capacity_memory_gb_total_x1000", int64(digest.TotalMemoryGB*1000))
		mx.SetGauge("oim_capacity_memory_gb_real_x1000", int64(digest.RealTotalMemoryGB*1000))
		mx.SetGauge("oim_capacity_toks_per_sec_total_x1000", int64(digest.AggregateToksPerSec*1000))
		mx.SetGauge("oim_capacity_toks_per_sec_real_x1000", int64(digest.RealAggregateToksPerSec*1000))
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		w.Write([]byte(mx.Expose()))
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
				"POST /nodes/{id}/attest-enclave",
				"POST /v1/chat/completions",
				"GET  /users/{id}/balance",
				"POST /users/{id}/startup-grant",
				"POST /settlement/records",
				"GET  /federation/identity",
				"GET  /federation/ledger-events  (requires --federation-key)",
				"GET  /federation/audit/{user_id}?peer_endpoint=  (requires --federation-key)",
			},
		})
	})

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", listenAddr, err)
	}

	// Per-account quota (task: read-endpoint auth + abuse limits): a token
	// bucket keyed on the AUTHENTICATED user_id, enforced inside authMiddleware
	// where the key→user mapping is verified — not on the client-supplied
	// X-OIM-User-ID header, which a caller could spoof to evade the cap or
	// grief another account. Burst = ~1 minute of the hourly budget (floor 5)
	// so normal interactive use isn't throttled while sustained abuse is.
	userBurst := userQuotaPerHour / 60.0
	if userBurst < 5 {
		userBurst = 5
	}
	userLimiter := httpmw.NewRateLimiter(userQuotaPerHour/3600.0, userBurst)
	defer userLimiter.Stop()

	var handler http.Handler = mux
	if apiKey != "" {
		handler = authMiddleware(apiKey, apiKeys, userLimiter, mux)
		log.Printf("[coordinator] API key auth enabled for write operations")
		if userQuotaPerHour > 0 {
			log.Printf("[coordinator] per-account quota enabled: %.0f req/hour per user", userQuotaPerHour)
		}
	} else if userQuotaPerHour > 0 {
		log.Printf("[coordinator] WARNING: --user-quota-per-hour set but --api-key is not; per-account quota only applies to authenticated (oim_ key) requests, so it will not engage without auth")
	}
	if protectUserReads {
		log.Printf("[coordinator] per-user read protection enabled: GET /users/{id}/balance and /api-key require the account's own key or the admin key")
	}
	limiter := httpmw.NewRateLimiter(rateLimitRPS, rateLimitBurst)
	defer limiter.Stop()
	if rateLimitRPS > 0 {
		log.Printf("[coordinator] rate limiting enabled: %.1f req/s per IP, burst %.0f", rateLimitRPS, rateLimitBurst)
	}
	// Metrics: count every request + track in-flight as a gauge (task #20).
	metricsMiddleware := func(next http.Handler) http.Handler {
		reqs := mx.Counter("oim_http_requests_total")
		inflight := mx.Gauge("oim_http_requests_in_flight")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqs.Inc()
			inflight.Inc()
			defer inflight.Dec()
			next.ServeHTTP(w, r)
		})
	}

	// Middleware order (outermost first): security headers → CORS → metrics →
	// body-size cap → global concurrency limit → per-IP rate limit → auth/handler.
	// The size cap and concurrency limit are the DDoS floor a per-IP limiter can't
	// provide against a distributed flood (task #53).
	chain := httpmw.SecurityHeaders(
		corsMiddleware(corsOrigins,
			metricsMiddleware(
				httpmw.MaxBodyBytes(httpmw.DefaultMaxBodyBytes,
					httpmw.LimitConcurrency(maxConcurrentRequests,
						rateLimitMiddleware(limiter, proxyNets, handler))))))
	srv := &http.Server{
		Handler:           chain,
		ReadHeaderTimeout: readHeaderTimeout, // slow-loris guard (task #53)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})

	// Optional: report aggregate pod health to the directory on a recurring schedule.
	// Registering against MULTIPLE directories (task #49, progressive
	// decentralization) means no single directory instance going down takes
	// this pod off the map — CentralizedResolver.RegisterPod already pushes
	// to every configured endpoint and only reports an error if ALL of them
	// rejected it.
	if len(directoryURLs) > 0 {
		resolver := directory.NewCentralizedResolver(directoryURLs)
		go func() {
			reportToDirectory(resolver, registry, podID, regionHint, publicURL, coordPriv, coordPub)
			ticker := time.NewTicker(directoryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					reportToDirectory(resolver, registry, podID, regionHint, publicURL, coordPriv, coordPub)
				}
			}
		}()
		log.Printf("[coordinator] reporting to directories %v every %s", directoryURLs, directoryInterval)
	}

	// Optional: witness peer pods' signed credit-issuance history (task #52,
	// M7). Only runs when both --directory (to discover peers) and
	// --federation-key (to authenticate the pull) are set — opt-in, like the
	// rest of the security hardening in this codebase.
	if len(directoryURLs) > 0 && federationKey != "" {
		go pollFederationPeers(done, directoryURLs, federationKey, podID, fedStore, federationPollInterval)
		log.Printf("[coordinator] federation witnessing enabled: polling peers via %v every %s", directoryURLs, federationPollInterval)
	} else if len(directoryURLs) > 0 {
		log.Printf("[coordinator] federation witnessing disabled: set --federation-key to enable (task #52, M7)")
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

	scheme := "http"
	if httptls.Enabled(tlsCert, tlsKey) {
		scheme = "https"
		httptls.WarnIfExpiringSoon(tlsCert, 30*24*time.Hour, "coordinator")
	} else {
		log.Printf("[coordinator] WARNING: serving PLAINTEXT HTTP — set --tls-cert/--tls-key before exposing beyond localhost")
	}
	log.Printf("[coordinator] pod=%s region=%s listening on %s (%s)", podID, regionHint, listenAddr, scheme)
	if err := httptls.Serve(srv, ln, tlsCert, tlsKey); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// federationAuthorized checks the request's Bearer token against
// federationKey. GET requests never pass through authMiddleware (it leaves
// all reads open so the dashboard needs no auth) so federation endpoints that
// need protecting check this directly rather than relying on that middleware.
// An empty federationKey always denies — federation witnessing is opt-in
// (see --federation-key), and these two handlers are only reached with a
// zero-value key when an operator wired federation up incompletely.
// authorizeUserRead gates the per-user read endpoints when --protect-user-reads
// is on. Allowed callers: the static admin key, or the user's own oim_ key
// (whose stored user_id matches the requested id). When protection is off it
// always allows — preserving the open-reads default the sim and the public
// dashboard rely on. Aggregate reads (/topology, /nodes, /metrics) are never
// gated here; only per-user data (balance, key existence) is.
func authorizeUserRead(r *http.Request, id string, protect bool, adminKey string, keys *apiKeyStore) bool {
	if !protect {
		return true
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if auth == "" {
		return false
	}
	if adminKey != "" && subtle.ConstantTimeCompare([]byte(auth), []byte(adminKey)) == 1 {
		return true
	}
	if strings.HasPrefix(auth, "oim_") {
		if uid, ok := keys.lookup(auth); ok && uid == id {
			return true
		}
	}
	return false
}

func federationAuthorized(r *http.Request, federationKey string) bool {
	if federationKey == "" {
		return false
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	// Constant-time compare: a plain == leaks how many leading bytes matched
	// via timing, which over many requests can recover a bearer secret.
	return subtle.ConstantTimeCompare([]byte(auth), []byte(federationKey)) == 1
}

func reportToDirectory(resolver *directory.CentralizedResolver, registry *coordinator.NodeRegistry, podID, regionHint, publicURL string, coordPriv, coordPub []byte) {
	digest := registry.HealthDigest(podID, regionHint, publicURL)
	// Signed with this coordinator's own identity (task #52, M7) so the
	// directory's PinStore can verify it wasn't forged and bind pod_id to
	// this specific key — see internal/directory.PinStore.Verify.
	signed, err := protocol.SignPodHealthDigest(digest, coordPriv, coordPub)
	if err != nil {
		log.Printf("[coordinator] sign digest for directory report: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := resolver.RegisterPod(ctx, signed); err != nil {
		log.Printf("[coordinator] directory report: %v", err)
	} else {
		log.Printf("[coordinator] reported to directory: pod=%s models=%v", podID, digest.ServableModelIDs)
	}
}

// pollFederationPeers periodically discovers peer pods via the directory's
// topology and pulls each one's new signed ledger events, storing them as
// witnessed history (task #52, M7). A malformed or impersonating peer logs
// loudly rather than being silently dropped — this IS the detection
// mechanism, not just plumbing.
func pollFederationPeers(done <-chan struct{}, directoryURLs []string, federationKey, selfPodID string, fedStore *federation.Store, interval time.Duration) {
	client := federation.NewClient()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	poll := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Try each configured directory in order — one being down doesn't stop
		// federation witnessing as long as ANY of them has current topology
		// (task #49, progressive decentralization: no single directory is a
		// hard dependency once more than one is configured).
		var topo struct {
			Pods []protocol.PodHealthDigest `json:"pods"`
		}
		fetched := false
		for _, dirURL := range directoryURLs {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, dirURL+"/topology", nil)
			if err != nil {
				continue
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Printf("[coordinator] federation: fetch topology from %s: %v", dirURL, err)
				continue
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&topo)
			resp.Body.Close()
			if decodeErr != nil {
				log.Printf("[coordinator] federation: parse topology from %s: %v", dirURL, decodeErr)
				continue
			}
			fetched = true
			break
		}
		if !fetched {
			log.Printf("[coordinator] federation: all %d configured director(ies) unreachable this cycle", len(directoryURLs))
			return
		}
		for _, pod := range topo.Pods {
			if pod.PodID == selfPodID || pod.CoordinatorEndpoint == "" {
				continue
			}
			since := fedStore.WitnessedHighWatermark(pod.PodID)
			events, err := client.FetchEventsSince(ctx, pod.CoordinatorEndpoint, federationKey, since)
			if err != nil {
				log.Printf("[coordinator] federation: pull from pod=%s: %v", pod.PodID, err)
				continue
			}
			for _, evt := range events {
				if err := fedStore.StoreWitnessedEvent(evt); err != nil {
					log.Printf("[coordinator] federation: REJECTED event from pod=%s seq=%d: %v", evt.PodID, evt.Sequence, err)
				}
			}
		}
	}
	poll()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			poll()
		}
	}
}

// isSelfAuthenticatingWrite reports whether a POST endpoint already verifies
// its own credential — an Ed25519 signature over a registered node/account
// key, or a caller-must-be-reachable-before-they-have-any-token bootstrap
// step (wallet challenge/auth, first api-key mint, startup-grant claim,
// already PoW/rate-limited) — so gating it behind the coordinator's admin
// Bearer token would be redundant at best and a hard lockout at worst: no
// node has any way to send that token, and a brand-new user/wallet can't
// reach the ONE endpoint that would mint their first credential.
// /settlement/records is included even though it can't verify a signature at
// the HTTP layer (the caller hasn't been resolved to a node yet) — an
// unsigned or forged record is inert (settlement.VerifySettlementRecord runs
// inside the handler and only credits on success), so gating it here would
// only block legitimate nodes, not attackers.
func isSelfAuthenticatingWrite(method, path string) bool {
	if method != http.MethodPost {
		return false // DELETE (deregister node, revoke api-key) stays admin-gated
	}
	switch {
	case path == "/nodes/register",
		path == "/settlement/records",
		path == "/account/challenge",
		path == "/account/auth",
		path == "/coordination/announce",
		path == "/coordination/withdraw":
		return true
	case strings.HasPrefix(path, "/nodes/") &&
		(strings.HasSuffix(path, "/refresh") ||
			strings.HasSuffix(path, "/attest-enclave") ||
			strings.HasSuffix(path, "/benchmark-result") ||
			strings.HasSuffix(path, "/job-outcome")):
		return true
	case strings.HasPrefix(path, "/users/") &&
		(strings.HasSuffix(path, "/startup-grant") || strings.HasSuffix(path, "/api-key")):
		return true
	case strings.HasPrefix(path, "/account/") && strings.HasSuffix(path, "/link-device"):
		return true
	}
	return false
}

// authMiddleware requires a Bearer token for write operations (POST, DELETE)
// that don't already authenticate themselves some other way (see
// isSelfAuthenticatingWrite). GET requests and CORS preflight are always open
// so the dashboard can read without auth. /nodes/stream is also always open
// since EventSource cannot send Authorization headers.
// Two token forms are accepted:
//   - The static admin key (--api-key flag) — grants full access, no user attribution
//   - A per-user oim_* key (generated via POST /users/{id}/api-key) — the user ID is
//     injected as X-OIM-User-ID so the credit gate can debit the right account
func authMiddleware(adminKey string, keys *apiKeyStore, userLimiter *httpmw.RateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reads, preflight, and self-authenticating/bootstrap writes are open
		if r.Method == http.MethodGet || r.Method == http.MethodOptions || isSelfAuthenticatingWrite(r.Method, r.URL.Path) {
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
		// Static admin key — accepted as-is (constant-time to avoid leaking it
		// byte-by-byte via comparison timing). Exempt from the per-account quota:
		// it's the operator, not a metered user account.
		if adminKey != "" && subtle.ConstantTimeCompare([]byte(auth), []byte(adminKey)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		// Per-user oim_* key — inject the user ID and allow.
		if strings.HasPrefix(auth, "oim_") {
			if uid, ok := keys.lookup(auth); ok {
				// Per-account quota, keyed on the VERIFIED user_id (not the
				// spoofable X-OIM-User-ID header), so one account can't exceed
				// its hourly budget across many IPs.
				if userLimiter != nil && !userLimiter.Allow("user:"+uid) {
					w.Header().Set("Retry-After", "60")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					json.NewEncoder(w).Encode(map[string]string{"error": "per-account quota exceeded, retry shortly"})
					return
				}
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

// rateLimitMiddleware enforces a per-client-IP token-bucket limit across every
// endpoint except the SSE stream (a single long-lived connection, not a series
// of discrete requests — counting it against the limiter would make it trip
// immediately) and CORS preflight. Returns 429 with Retry-After when exceeded.
func rateLimitMiddleware(limiter *httpmw.RateLimiter, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || r.URL.Path == "/nodes/stream" {
			next.ServeHTTP(w, r)
			return
		}
		if !limiter.Allow(httpmw.ClientIP(r, trustedProxies)) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded, retry shortly"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware allows browser requests from allowedOrigins. An empty
// allowedOrigins means "allow any origin" — the dev-friendly default so the
// dashboard works out of the box without configuration. Operators serving
// real user traffic should pass --cors-origin so a malicious page can't drive
// a visitor's browser into hitting the coordinator with their session.
func corsMiddleware(allowedOrigins []string, h http.Handler) http.Handler {
	allowAll := len(allowedOrigins) == 0
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		switch {
		case allowAll:
			w.Header().Set("Access-Control-Allow-Origin", "*")
		case origin != "" && originAllowed(origin, allowedOrigins):
			// Echo back the specific matched origin (not "*") — required for any
			// future credentialed-request support and more correct than a wildcard
			// even without it: it tells the browser exactly which origin was vetted.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-OIM-User-ID")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// originAllowed reports whether the request Origin matches the operator's CORS
// allowlist (task #27). Matching is case-insensitive on scheme+host. Beyond
// exact matches it supports a leading-`*.` wildcard so an operator can allow a
// whole domain family — e.g. `https://*.mlxmesh.io` matches
// `https://app.mlxmesh.io` and `https://dash.mlxmesh.io` but NOT the bare apex
// `https://mlxmesh.io` (list that explicitly) nor a lookalike suffix
// `https://evilmlxmesh.io`.
func originAllowed(origin string, allowlist []string) bool {
	origin = strings.ToLower(strings.TrimSpace(origin))
	for _, entry := range allowlist {
		entry = strings.ToLower(strings.TrimSpace(entry))
		if entry == "" {
			continue
		}
		if i := strings.Index(entry, "*."); i >= 0 {
			// Wildcard sits in the host, after the scheme, e.g. "https://*.mlxmesh.io".
			// Split into scheme prefix ("https://") and domain suffix ("mlxmesh.io");
			// the origin must share the scheme and end in ".<suffix>" with a real
			// label in between (so the apex and suffix lookalikes don't match).
			schemePrefix, domainSuffix := entry[:i], entry[i+2:]
			if !strings.HasPrefix(origin, schemePrefix) {
				continue
			}
			host := origin[len(schemePrefix):]
			if strings.HasSuffix(host, "."+domainSuffix) && len(host) > len(domainSuffix)+1 {
				return true
			}
			continue
		}
		if origin == entry {
			return true
		}
	}
	return false
}

// maxConcurrentRequests bounds total in-flight requests across the whole server
// (task #53). A distributed flood defeats per-IP limiting, so this caps aggregate
// resource use; excess requests get 503 + Retry-After. Sized for a single
// coordinator box — raise it behind bigger hardware or a load balancer.
const maxConcurrentRequests = 256

// readHeaderTimeout bounds how long a client may take to send request headers,
// closing the slow-loris hold-open attack (task #53, #25).
const readHeaderTimeout = 10 * time.Second

// creditPointerHost attributes one served encrypted pointer to the coordination
// participant that hosted it (self-identified via X-OIM-Pointer-Host) and, if
// that device is linked to a wallet account, pays it the flat per-pointer
// coordination reward out of the treasury. A stale/unknown/empty host is ignored
// — attribution never affects routing or the reply. Extracted from the fast-lane
// handler (task #21) so the money path is unit-testable and the handler stays
// readable.
func creditPointerHost(ledger *settlement.Ledger, coordReg *coordinator.CoordinationRegistry, walletMgr *wallet.Manager, host, jobID string) {
	if host == "" || !coordReg.RecordPointerServed(host) {
		return
	}
	log.Printf("[coordinator] pointer served host=%s job=%s", host, jobID)
	// iOS devices do a security service, not compute, so this is small but
	// nonzero — hosting pointers earns, it isn't just altruism.
	acct, ok := walletMgr.AccountForDevice(host)
	if !ok {
		return
	}
	reward := economics.CoordinationReward(1)
	_ = ledger.CreditAccount(settlement.CreditEntry{
		UserID:            acct,
		Origin:            settlement.CreditOriginEarnedContrib,
		Amount:            reward,
		GrantedOrEarnedAt: time.Now(),
		SourceReference:   jobID + "-coord",
	})
	if ledger.GetBalance(economics.TreasuryAccount).Total >= reward {
		_ = ledger.DebitAccount(economics.TreasuryAccount, reward, jobID+"-coord")
	}
	log.Printf("[coordinator] coordination reward acct=%s host=%s credits=%.4f", acct, host, reward)
}

// creditNodeEarning pays the account behind servedByNodeID for tokenCount output
// tokens at the given lane/sensitivity, and credits the network treasury the
// house-edge margin (economics.NetworkMargin). Provider reward is ALWAYS less
// than what the consumer was charged — the treasury keeps the spread. Callers
// must dedup by jobID (via creditedJobs) before calling.
func creditNodeEarning(ledger *settlement.Ledger, nodeUsers *sync.Map, servedByNodeID, jobID string, lane economics.Lane, sensitivity string, tokenCount int) {
	accountKey, _ := nodeUsers.Load(servedByNodeID)
	if accountKey == nil {
		accountKey = servedByNodeID // fallback: credit node_id directly
	}
	reward := economics.ProviderReward(lane, sensitivity, tokenCount)
	margin := economics.NetworkMargin(lane, sensitivity, tokenCount)
	_ = ledger.CreditAccount(settlement.CreditEntry{
		UserID:            accountKey.(string),
		Origin:            settlement.CreditOriginEarnedContrib,
		Amount:            reward,
		GrantedOrEarnedAt: time.Now(),
		SourceReference:   jobID,
	})
	if margin > 0 {
		_ = ledger.CreditAccount(settlement.CreditEntry{
			UserID:            economics.TreasuryAccount,
			Origin:            settlement.CreditOriginEarnedContrib,
			Amount:            margin,
			GrantedOrEarnedAt: time.Now(),
			SourceReference:   jobID + "-margin",
		})
	}
	slog.Info("earned",
		"user", accountKey, "node", servedByNodeID, "lane", lane, "tier", sensitivity,
		"tokens", tokenCount, "reward", reward, "margin", margin, "source", "observed")
}

// Pricing/rewards now live in internal/economics (ConsumerCost / ProviderReward /
// NetworkMargin / CoordinationReward). The old sensitivityRate was replaced so the
// debit and credit paths can never diverge and the house edge is always applied.

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
