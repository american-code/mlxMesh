package directory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/open-inference-mesh/oim/internal/protocol"
)

// Gossip handles pod digest propagation between directory instances.
// Bootstrap stage: single-pair sync (one configured peer max).
// Full multi-peer gossip is deferred to M7 — keep the function signatures
// general now so the upgrade doesn't break callers.
type Gossip struct {
	store   *PodStore
	peerURL string // empty = standalone mode, no peer
	client  *http.Client
}

func NewGossip(store *PodStore, peerURL string) *Gossip {
	return &Gossip{
		store:   store,
		peerURL: peerURL,
		client:  &http.Client{Timeout: 10 * time.Second},
	}
}

// ReceivePodDigest stores the digest locally and propagates once to the peer (if configured).
// Propagation is one-hop only — the peer endpoint stores directly and does NOT re-propagate,
// preventing loop amplification between the two bootstrap instances.
func (g *Gossip) ReceivePodDigest(digest protocol.PodHealthDigest) {
	g.store.Upsert(digest)
	if g.peerURL != "" {
		go func() {
			if err := g.propagateToPeer(context.Background(), digest); err != nil {
				log.Printf("[gossip] propagate to peer %s: %v", g.peerURL, err)
			}
		}()
	}
}

// propagateToPeer forwards the digest to the peer's /gossip/digest endpoint.
func (g *Gossip) propagateToPeer(ctx context.Context, digest protocol.PodHealthDigest) error {
	b, err := json.Marshal(digest)
	if err != nil {
		return fmt.Errorf("marshal digest: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.peerURL+"/gossip/digest", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s/gossip/digest: %w", g.peerURL, err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("peer returned HTTP %d", resp.StatusCode)
	}
	return nil
}
