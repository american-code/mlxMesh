// Package coordinator implements the pod coordinator — one per geographic/latency pod.
// Routing decisions are made here; the directory layer only does discovery.
package coordinator

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/open-inference-mesh/oim/internal/bench"
	"github.com/open-inference-mesh/oim/internal/protocol"
)

// livenessTTL must be > 1× RefreshInterval so a single slow heartbeat isn't a false stale,
// but tight enough that a stopped node is visible within two polling cycles (~45s default).
const livenessTTL = 45 * time.Second

// evictionTTL is how long a stale/unreachable node stays in the registry before being purged.
// This prevents unbounded memory growth; 5 min is long enough for transient reconnects.
const evictionTTL = 5 * time.Minute

type nodeEntry struct {
	manifest    protocol.CapabilityManifest
	publicKey   []byte
	lastSeen    time.Time
	unreachable bool
	inFlight    int32 // atomic — jobs currently dispatched to this node

	// lastJobServedAt is the last time this node actually COMPLETED a real,
	// credited job — distinct from lastSeen, which only reflects a heartbeat/
	// refresh and says nothing about whether the node has ever served real
	// traffic. Zero value means "never served a job this registration."
	lastJobServedAt time.Time

	// registeredAt is set ONCE at first Register() and never touched by
	// Refresh() (unlike lastSeen, which resets on every heartbeat and is
	// therefore always fresh for any live node — using it as an idle
	// fallback would make a node that has never served anything look
	// perpetually "just active"). This is the correct fallback for
	// "idle since" when a node has never served a job at all.
	registeredAt time.Time

	// enclaveAttested/enclavePublicKey are set ONLY by MarkEnclaveAttested,
	// after the coordinator itself verifies a Secure Enclave proof — never
	// from the client-submitted manifest, which is exactly what
	// manifest.HasSecureEnclave is (self-declared, untrustworthy for gating).
	// Reset to false on every re-registration; a node must re-attest.
	enclaveAttested  bool
	enclavePublicKey []byte

	// observedTPS is a coordinator-computed EMA of real per-job decode tok/s,
	// fed by RecordObservedThroughput after each completed dispatch. nil
	// until at least one job has been observed. Deliberately separate from
	// manifest.MeasuredSignature (the node's self-declared/benchmarked
	// claim) — Refresh() replaces manifest wholesale every heartbeat, so a
	// value stored there would be erased by the node's own next check-in.
	// This field persists across heartbeats; it resets only when the entry
	// itself is evicted/re-registered.
	observedTPS     *float64
	observedSamples int
}

// throughputEMAAlpha weights how quickly RecordObservedThroughput's rolling
// average reacts to a new sample — recent-weighted but not single-sample-jumpy.
const throughputEMAAlpha = 0.3

// RecordObservedThroughput folds one real, coordinator-measured decode tok/s
// sample into nodeID's rolling average. Call only from dispatch paths that
// measured real wall-clock time against a real completion-token count —
// never from anything the node self-reports (that self-reported path is
// exactly what VerifyTierClaim/MeasurementStore exist to audit separately).
func (r *NodeRegistry) RecordObservedThroughput(nodeID string, tokensPerSec float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[nodeID]
	if !ok || tokensPerSec <= 0 {
		return
	}
	if e.observedTPS == nil {
		v := tokensPerSec
		e.observedTPS = &v
	} else {
		*e.observedTPS = *e.observedTPS*(1-throughputEMAAlpha) + tokensPerSec*throughputEMAAlpha
	}
	e.observedSamples++
}

func (e *nodeEntry) isLive() bool {
	return time.Since(e.lastSeen) < livenessTTL && !e.unreachable
}

// NodeRegistry is a live, in-memory scoreboard of every node registered to this pod.
// It decays — stale entries are excluded from Candidates without explicit removal.
type NodeRegistry struct {
	mu      sync.RWMutex
	entries map[string]*nodeEntry

	// pull, when set, delivers jobs to pull-mode nodes (those that long-poll
	// for work instead of accepting inbound dispatch — see PullDispatcher).
	// Held here rather than threaded through every DispatchFastLane/
	// DispatchToResolvedNode signature: the registry is already passed to
	// every dispatch path, and deliverJob is the single branch point between
	// pull (mailbox) and push (outbound HTTP) delivery. nil = pull disabled
	// (every node treated as push), which is the pre-pull behavior.
	pull *PullDispatcher
}

// SetPullDispatcher wires the coordinator's PullDispatcher into the registry so
// deliverJob can route pull-mode nodes through it. Called once at startup.
func (r *NodeRegistry) SetPullDispatcher(pd *PullDispatcher) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pull = pd
}

// IsPullNode reports whether nodeID registered as a pull-delivery node. Used by
// the resolved-node dispatch path, which has only a NodeTarget (endpoint+pin)
// and not the full manifest in hand.
func (r *NodeRegistry) IsPullNode(nodeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[nodeID]
	return ok && e.manifest.PullDelivery
}

// deliverJob is the single push-vs-pull branch point every buffered dispatch
// path funnels through. A pull node (manifest.PullDelivery, and a wired
// PullDispatcher) gets the job via the mailbox — the node claims it over its
// own outbound long-poll, so the coordinator never dials in. Everything else
// (the simulated fleet, LAN nodes with an explicit endpoint) takes the
// unchanged outbound-HTTP path.
func (r *NodeRegistry) deliverJob(ctx context.Context, nodeID string, isPull bool, target NodeTarget, job protocol.JobSpec, messages []map[string]any) (map[string]any, error) {
	r.mu.RLock()
	pull := r.pull
	r.mu.RUnlock()
	if isPull && pull != nil {
		return pull.Dispatch(ctx, nodeID, job, messages)
	}
	return dispatchToNode(ctx, job, messages, target)
}

func NewNodeRegistry() *NodeRegistry {
	r := &NodeRegistry{entries: make(map[string]*nodeEntry)}
	go r.runEviction()
	return r
}

func (r *NodeRegistry) runEviction() {
	t := time.NewTicker(30 * time.Second)
	for range t.C {
		r.mu.Lock()
		for id, e := range r.entries {
			if time.Since(e.lastSeen) > evictionTTL {
				delete(r.entries, id)
			}
		}
		r.mu.Unlock()
	}
}

// Register verifies the signature and node_id derivation before accepting.
// Returns false (without error) on signature failure — the caller decides how to respond.
func (r *NodeRegistry) Register(reg protocol.NodeRegistration) (bool, error) {
	expectedID := protocol.NodeIDFromPubKey(reg.PublicKey)
	if expectedID != reg.Manifest.NodeID {
		return false, fmt.Errorf("node_id mismatch: manifest %s, pubkey derives %s",
			reg.Manifest.NodeID, expectedID)
	}
	payload, err := reg.Manifest.Bytes()
	if err != nil {
		return false, fmt.Errorf("serialize manifest: %w", err)
	}
	if !protocol.VerifySignature(reg.PublicKey, payload, reg.Signature) {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[reg.Manifest.NodeID] = &nodeEntry{
		manifest:     reg.Manifest,
		publicKey:    reg.PublicKey,
		lastSeen:     time.Now(),
		registeredAt: time.Now(),
		unreachable:  false,
	}
	return true, nil
}

// Refresh updates a node's manifest and last-seen timestamp. Clears unreachable flag.
func (r *NodeRegistry) Refresh(nodeID string, manifest protocol.CapabilityManifest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[nodeID]
	if !ok {
		return fmt.Errorf("node %s not registered; send a full registration first", nodeID)
	}
	e.manifest = manifest
	e.lastSeen = time.Now()
	e.unreachable = false
	return nil
}

// MarkEnclaveAttested records a coordinator-verified Secure Enclave proof for
// nodeID. Callers MUST have already validated the attestation (see
// VerifyEnclaveAttestation) — this method itself does no verification, it
// only records the outcome.
func (r *NodeRegistry) MarkEnclaveAttested(nodeID string, enclavePublicKey []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[nodeID]
	if !ok {
		return fmt.Errorf("node %s not registered", nodeID)
	}
	e.enclaveAttested = true
	e.enclavePublicKey = enclavePublicKey
	return nil
}

// MarkUnreachable is called by the router on failed dispatch — not just missed heartbeat.
// The node must re-register or send a refresh to clear this flag.
func (r *NodeRegistry) MarkUnreachable(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[nodeID]; ok {
		e.unreachable = true
	}
}

// MarkJobServed records that nodeID just completed a real, credited job —
// called from every real-traffic credit site (creditNodeEarning) AND from
// the availability-reward prober's own successful credit, so a probed node
// isn't immediately re-selected ahead of nodes that have gone longer without
// any credited activity. A no-op if the node isn't currently registered
// (e.g. it deregistered between dispatch and credit).
func (r *NodeRegistry) MarkJobServed(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[nodeID]; ok {
		e.lastJobServedAt = time.Now()
	}
}

// MarkSeen refreshes a node's liveness without a full manifest refresh — a
// pull node actively long-polling /jobs/claim is provably alive, so its claim
// doubles as a heartbeat (keeps it in the candidate set between the slower
// manifest-refresh ticks). No-op for an unregistered node.
func (r *NodeRegistry) MarkSeen(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[nodeID]; ok {
		e.lastSeen = time.Now()
		e.unreachable = false
	}
}

// IsLive returns true if the node is registered, within TTL, and not marked unreachable.
func (r *NodeRegistry) IsLive(nodeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[nodeID]
	return ok && e.isLive()
}

// clusterStandbyNodeIDs returns the set of live node IDs that duplicate
// another live node's same physical Exo ring (same non-empty
// manifest.ClusterSignature). Every device in a clustered ring runs its own
// agent and independently registers claiming the SAME pooled capacity
// (capability.DetectClusterNode runs per-device against a per-device view of
// the whole ring) — without this, routing, idle-probing, and capacity totals
// would all double-count one physical ring. Deterministic, stateless
// tiebreak: within each ClusterSignature group, the lexicographically lowest
// live NodeID is the sole non-standby representative — recomputed fresh on
// every call, so when the primary goes stale/evicted the standby becomes
// eligible immediately, with no promotion bookkeeping to get out of sync.
// Must be called with r.mu held (read or write).
func (r *NodeRegistry) clusterStandbyNodeIDs() map[string]bool {
	best := map[string]string{}
	for id, e := range r.entries {
		if e.manifest.ClusterSignature == "" || !e.isLive() {
			continue
		}
		if cur, ok := best[e.manifest.ClusterSignature]; !ok || id < cur {
			best[e.manifest.ClusterSignature] = id
		}
	}
	standby := map[string]bool{}
	for id, e := range r.entries {
		sig := e.manifest.ClusterSignature
		if sig != "" && e.isLive() && best[sig] != id {
			standby[id] = true
		}
	}
	return standby
}

// Candidates returns all currently-eligible nodes for a job.
// Filtering only — no scoring/ranking (that's the router's job).
// Excludes nodes past the liveness TTL even if not explicitly marked unreachable.
// Also excludes cluster-standby duplicates (see clusterStandbyNodeIDs) so one
// physical Exo ring is never dispatched to twice under two registrations.
func (r *NodeRegistry) Candidates(modelID, quantization string) ([]protocol.CapabilityManifest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	standby := r.clusterStandbyNodeIDs()
	var out []protocol.CapabilityManifest
	for id, e := range r.entries {
		if !e.isLive() || standby[id] {
			continue
		}
		if !hasModel(e.manifest, modelID, quantization) {
			continue
		}
		out = append(out, e.manifest)
	}
	return out, nil
}

// NodeWithLoad pairs a node's capability manifest with its current in-flight job count.
type NodeWithLoad struct {
	Manifest protocol.CapabilityManifest
	InFlight int32
	// EnclaveAttested is the coordinator-VERIFIED Secure Enclave proof status —
	// never the client-declared Manifest.HasSecureEnclave. This is what
	// routing gates must check for SensitivityHighRequiresAttestation jobs.
	EnclaveAttested bool
	// ObservedTPS is the coordinator's own rolling-average decode tok/s for
	// this node (see nodeEntry.observedTPS), nil until at least one real job
	// has been observed. Routing should prefer this over the node's
	// self-declared Manifest.MeasuredSignature — see effectiveManifest.
	ObservedTPS *float64
}

// CandidatesWithLoad is like Candidates but includes the live in-flight counter for each node.
// The router uses this to score nodes by both throughput and current load.
func (r *NodeRegistry) CandidatesWithLoad(modelID, quantization string) ([]NodeWithLoad, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	standby := r.clusterStandbyNodeIDs()
	var out []NodeWithLoad
	for id, e := range r.entries {
		if !e.isLive() || standby[id] {
			continue
		}
		if !hasModel(e.manifest, modelID, quantization) {
			continue
		}
		out = append(out, NodeWithLoad{
			Manifest:        e.manifest,
			InFlight:        atomic.LoadInt32(&e.inFlight),
			EnclaveAttested: e.enclaveAttested,
			ObservedTPS:     e.observedTPS,
		})
	}
	return out, nil
}

// IdleCandidates returns live, non-simulated, model-capable nodes that
// haven't completed a real job in at least idleSince — the availability-
// reward prober's candidate pool. Simulated/seed nodes (OIM_SIMULATED_NODE)
// are always excluded: they're demo capacity, not real operator hardware,
// and subsidizing them would just mint credits into nothing. A node with no
// advertised models is excluded too — the same requirement real traffic
// already has (nothing to actually dispatch). Sorted oldest-idle-first so
// the longest-neglected real capacity is probed before nodes that are merely
// between real dispatches — a bootstrap nudge, not a top-up for nodes
// already earning.
//
// "Idle since" falls back to registeredAt (set once at first Register(), NEVER
// refreshed by heartbeats) for a node that has never served a job at all —
// deliberately NOT lastSeen, which resets on every heartbeat/refresh and is
// therefore always fresh for any live node (isLive() itself requires it to be
// within livenessTTL). Falling back to lastSeen would make a never-served
// node's "idle since" perpetually look like "just now," so it would never
// clear the idleSince cutoff no matter how long it had actually been sitting
// idle — the opposite of what this method needs to detect.
func (r *NodeRegistry) IdleCandidates(idleSince time.Duration) []protocol.CapabilityManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()

	type candidate struct {
		manifest protocol.CapabilityManifest
		idleFrom time.Time
	}
	cutoff := time.Now().Add(-idleSince)
	standby := r.clusterStandbyNodeIDs()
	var candidates []candidate
	for id, e := range r.entries {
		// Cluster-standby duplicates are excluded for the same reason as
		// Simulated: probing both registrations of one physical ring would
		// mint two availability rewards for one ring's worth of hardware.
		if !e.isLive() || e.manifest.Simulated || standby[id] || len(e.manifest.Models) == 0 {
			continue
		}
		idleFrom := e.lastJobServedAt
		if idleFrom.IsZero() {
			idleFrom = e.registeredAt
		}
		if idleFrom.After(cutoff) {
			continue // active recently enough — not idle
		}
		candidates = append(candidates, candidate{manifest: e.manifest, idleFrom: idleFrom})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].idleFrom.Before(candidates[j].idleFrom) })

	out := make([]protocol.CapabilityManifest, len(candidates))
	for i, c := range candidates {
		out[i] = c.manifest
	}
	return out
}

// IncrInFlight atomically increments the in-flight job counter for a node.
func (r *NodeRegistry) IncrInFlight(nodeID string) {
	r.mu.RLock()
	e, ok := r.entries[nodeID]
	r.mu.RUnlock()
	if ok {
		atomic.AddInt32(&e.inFlight, 1)
	}
}

// DecrInFlight atomically decrements the in-flight job counter (floors at 0).
func (r *NodeRegistry) DecrInFlight(nodeID string) {
	r.mu.RLock()
	e, ok := r.entries[nodeID]
	r.mu.RUnlock()
	if !ok {
		return
	}
	if atomic.AddInt32(&e.inFlight, -1) < 0 {
		atomic.StoreInt32(&e.inFlight, 0)
	}
}

// TotalInFlight returns the sum of in-flight jobs across all live nodes.
func (r *NodeRegistry) TotalInFlight() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var total int32
	for _, e := range r.entries {
		if e.isLive() {
			total += atomic.LoadInt32(&e.inFlight)
		}
	}
	return int(total)
}

// PublicKey returns the Ed25519 public key captured at registration for nodeID.
// Write-path requests (refresh, benchmark-result, job-outcome) must be signed with
// this SAME keypair — never a key supplied in the request itself, which would let
// anyone forge requests for a node they don't control.
func (r *NodeRegistry) PublicKey(nodeID string) ([]byte, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[nodeID]
	if !ok {
		return nil, false
	}
	return e.publicKey, true
}

// Manifest returns the currently registered CapabilityManifest for nodeID —
// used to resolve a node ID (e.g. from ResolveForCycle's sticky-session pick)
// to its live ReachabilityEndpoint before dispatching.
func (r *NodeRegistry) Manifest(nodeID string) (protocol.CapabilityManifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[nodeID]
	if !ok {
		return protocol.CapabilityManifest{}, false
	}
	return e.manifest, true
}

// ClaimedSignature returns the MeasuredSignature from a node's registered manifest,
// or (nil, nil) if the node registered without a benchmark. Returns error if node unknown.
func (r *NodeRegistry) ClaimedSignature(nodeID string) (*protocol.MeasuredSignature, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[nodeID]
	if !ok {
		return nil, fmt.Errorf("node %s not registered", nodeID)
	}
	return e.manifest.MeasuredSignature, nil
}

// HealthDigest returns the aggregate pod health for the directory layer.
// Deliberately aggregate-only — individual node data never leaves the pod (proposal §7.1).
// coordinatorEndpoint is the public URL clients should use to reach this coordinator;
// pass empty string to omit it from the digest.
func (r *NodeRegistry) HealthDigest(podID, regionHint, coordinatorEndpoint string) protocol.PodHealthDigest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	modelSet := map[string]bool{}
	liveCount := 0
	totalTPS := 0.0
	totalMemGB := 0.0
	// Real-vs-simulated split (task #49, progressive decentralization): the
	// README flags "parity" — the network takes over from the EC2 seed once
	// independently-operated capacity reaches parity with seed-hosted
	// capacity — as an undefined metric. These sub-totals are the missing
	// ingredient: every seeded demo node is tagged Simulated (OIM_SIMULATED_NODE),
	// so real*/total gives an honest, live progress ratio instead of a made-up
	// number. Deliberately NOT collapsed into one "parity %" here — what
	// threshold counts as "at parity" is a policy decision for whoever
	// eventually builds the handoff logic, not something to bake in silently.
	realCount := 0
	realTPS := 0.0
	realMemGB := 0.0
	standby := r.clusterStandbyNodeIDs()
	for id, e := range r.entries {
		// A cluster-standby duplicate's manifest already reports its ring's
		// FULL pooled memory/throughput — counting it again isn't extra
		// capacity, it's the same capacity twice. Excluded from every total,
		// including liveCount.
		if !e.isLive() || standby[id] {
			continue
		}
		liveCount++
		for _, m := range e.manifest.Models {
			modelSet[m.ModelID] = true
		}
		nodeTPS := 0.0
		if e.manifest.MeasuredSignature != nil {
			nodeTPS = e.manifest.MeasuredSignature.TokensPerSecDecode
		}
		// Prefer the coordinator's own observed throughput over the node's
		// self-declared/benchmarked claim — same rationale as Snapshot()
		// below. Deliberately NOT applied to VerifiedCapacityScore, which
		// must keep comparing the claimed signature against a submitted
		// benchmark for fraud detection.
		if e.observedTPS != nil {
			nodeTPS = *e.observedTPS
		}
		nodeMemGB := e.manifest.DeclaredMemoryGB * e.manifest.DeclaredMemoryCapPct
		totalTPS += nodeTPS
		totalMemGB += nodeMemGB
		if !e.manifest.Simulated {
			realCount++
			realTPS += nodeTPS
			realMemGB += nodeMemGB
		}
	}
	models := make([]string, 0, len(modelSet))
	for m := range modelSet {
		models = append(models, m)
	}
	health := 0.0
	if liveCount > 0 && totalTPS > 0 {
		health = min(1.0, totalTPS/float64(liveCount)/100.0)
	}
	return protocol.PodHealthDigest{
		PodID:                   podID,
		RegionHint:              regionHint,
		CoordinatorEndpoint:     coordinatorEndpoint,
		ServableModelIDs:        models,
		AggregateHealthScore:    health,
		NodeCountApprox:         liveCount,
		TotalMemoryGB:           totalMemGB,
		AggregateToksPerSec:     totalTPS,
		RealNodeCountApprox:     realCount,
		RealTotalMemoryGB:       realMemGB,
		RealAggregateToksPerSec: realTPS,
	}
}

// NodeSnapshot is a dashboard-friendly view of one live node's state.
type NodeSnapshot struct {
	NodeID               string                     `json:"node_id"`
	Status               string                     `json:"status"` // "live" | "stale" | "unreachable"
	GeographicHint       string                     `json:"geographic_hint"`
	GeoLat               float64                    `json:"geo_lat,omitempty"` // 0 = not declared
	GeoLng               float64                    `json:"geo_lng,omitempty"` // 0 = not declared
	ReachabilityEndpoint string                     `json:"reachability_endpoint"`
	DeclaredMemoryGB     float64                    `json:"declared_memory_gb"`
	CommittedMemoryGB    float64                    `json:"committed_memory_gb"` // declared * cap_pct
	Models               []protocol.ModelCapability `json:"models"`
	MeasuredToksPerSec   float64                    `json:"measured_toks_per_sec"` // 0 if not yet benchmarked
	HasSecureEnclave     bool                       `json:"has_secure_enclave"`    // self-declared by the node — informational only
	EnclaveAttested      bool                       `json:"enclave_attested"`      // coordinator-verified Secure Enclave proof — trust this for gating
	IsCluster            bool                       `json:"is_cluster"`
	ClusterDeviceCount   *int                       `json:"cluster_device_count,omitempty"`
	ClusterChipFamilies  []string                   `json:"cluster_chip_families,omitempty"`
	LastSeenAt           string                     `json:"last_seen_at"`
	InFlightJobs         int                        `json:"in_flight_jobs"` // currently dispatched jobs
	ECDHPublicKey        string                     `json:"ecdh_public_key,omitempty"`
	// Simulated is decorative/seed capacity, not a real operator's hardware —
	// see protocol.CapabilityManifest.Simulated.
	Simulated bool `json:"simulated,omitempty"`
	// ClusterStandby marks a duplicate registration of a physical Exo ring
	// another live node already represents (same cluster_signature, higher
	// node ID) — kept visible here for operator transparency, but excluded
	// from routing, idle-probing, and every HealthDigest capacity total.
	// See NodeRegistry.clusterStandbyNodeIDs.
	ClusterStandby bool `json:"cluster_standby,omitempty"`
	// ObservedToksPerSec is the coordinator's own rolling-average decode
	// tok/s from real dispatched jobs (nil until at least one job has been
	// observed) — distinct from MeasuredToksPerSec, which may still reflect
	// a one-time self-reported benchmark. MeasuredToksPerSec already
	// prefers this value once set; these are exposed separately purely for
	// operator transparency (e.g. "benchmark: 40 t/s vs. observed: 61 t/s").
	ObservedToksPerSec  *float64 `json:"observed_toks_per_sec,omitempty"`
	ObservedSampleCount int      `json:"observed_sample_count,omitempty"`
}

// Snapshot returns a point-in-time view of all registered nodes (live and recently stale).
func (r *NodeRegistry) Snapshot() []NodeSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]NodeSnapshot, 0, len(r.entries))
	standby := r.clusterStandbyNodeIDs()
	for id, e := range r.entries {
		status := "live"
		if e.unreachable {
			status = "unreachable"
		} else if !e.isLive() {
			status = "stale"
		}
		tps := 0.0
		if e.manifest.MeasuredSignature != nil {
			tps = e.manifest.MeasuredSignature.TokensPerSecDecode
		}
		// Prefer the coordinator's own observed throughput over the node's
		// self-declared/benchmarked claim — this is what actually clears a
		// stale "Reduced perf" badge once real traffic has been served.
		if e.observedTPS != nil {
			tps = *e.observedTPS
		}
		models := e.manifest.Models
		if models == nil {
			models = []protocol.ModelCapability{}
		}
		out = append(out, NodeSnapshot{
			NodeID:               e.manifest.NodeID,
			Status:               status,
			GeographicHint:       e.manifest.GeographicHint,
			GeoLat:               e.manifest.GeoLat,
			GeoLng:               e.manifest.GeoLng,
			ReachabilityEndpoint: e.manifest.ReachabilityEndpoint,
			DeclaredMemoryGB:     e.manifest.DeclaredMemoryGB,
			CommittedMemoryGB:    e.manifest.DeclaredMemoryGB * e.manifest.DeclaredMemoryCapPct,
			Models:               models,
			MeasuredToksPerSec:   tps,
			HasSecureEnclave:     e.manifest.HasSecureEnclave,
			EnclaveAttested:      e.enclaveAttested,
			IsCluster:            e.manifest.IsCluster,
			ClusterDeviceCount:   e.manifest.ClusterDeviceCount,
			ClusterChipFamilies:  e.manifest.ClusterChipFamilies,
			LastSeenAt:           e.lastSeen.UTC().Format("2006-01-02T15:04:05Z"),
			InFlightJobs:         int(atomic.LoadInt32(&e.inFlight)),
			ECDHPublicKey:        e.manifest.ECDHPublicKey,
			Simulated:            e.manifest.Simulated,
			ClusterStandby:       standby[id],
			ObservedToksPerSec:   e.observedTPS,
			ObservedSampleCount:  e.observedSamples,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// VerifiedCapacityScore sums the measured TPS of all live nodes whose submitted benchmark
// passes tier verification within tolerancePct of their claimed signature.
// Nodes that have never submitted a measurement, whose measurement diverges too far from
// their claim, or that are not currently live contribute zero to the score.
// This is the input to settlement/grant_decay — spinning up junk nodes that never pass
// verification must not drive grants toward zero (proposal §9.4).
func (r *NodeRegistry) VerifiedCapacityScore(measurements *MeasurementStore, tolerancePct float64) float64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var score float64
	for _, e := range r.entries {
		if !e.isLive() || e.manifest.MeasuredSignature == nil {
			continue
		}
		measured, ok := measurements.Get(e.manifest.NodeID)
		if !ok {
			continue
		}
		if bench.CompareSignatures(e.manifest.MeasuredSignature, measured, tolerancePct) {
			score += measured.TokensPerSecDecode
		}
	}
	return score
}

func hasModel(m protocol.CapabilityManifest, modelID, quantization string) bool {
	for _, model := range m.Models {
		if model.ModelID != modelID {
			continue
		}
		if quantization != "" && model.Quantization != quantization {
			continue
		}
		return true
	}
	return false
}

// modelIsLoaded reports whether the node has an ACTIVELY LOADED (not merely
// downloaded) instance for modelID — used to gate real dispatch eligibility
// (see ScoreForFastLane) separately from hasModel's broader "does this node
// have this model at all" check, which the warm-model path still needs (it
// specifically targets nodes that have a model downloaded but not yet
// loaded, so it must NOT be gated by this).
func modelIsLoaded(m protocol.CapabilityManifest, modelID, quantization string) bool {
	for _, model := range m.Models {
		if model.ModelID != modelID {
			continue
		}
		if quantization != "" && model.Quantization != quantization {
			continue
		}
		return model.Loaded
	}
	return false
}
