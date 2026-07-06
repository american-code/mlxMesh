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

package federation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Client pulls a peer coordinator's federation data. All calls are reads
// against another pod's coordinator — no credentials of this pod's own are
// ever sent anywhere but to that one peer.
type Client struct {
	HTTPClient *http.Client
}

func NewClient() *Client {
	return &Client{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

// IdentityResponse mirrors GET /federation/identity's body.
type IdentityResponse struct {
	PodID     string `json:"pod_id"`
	PublicKey string `json:"public_key"`
}

// FetchIdentity retrieves a peer's pod_id and public key. Public endpoint —
// no bearer token required, mirrors GET /health in sensitivity (non-secret).
func (c *Client) FetchIdentity(ctx context.Context, peerCoordinatorURL string) (IdentityResponse, error) {
	var out IdentityResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, peerCoordinatorURL+"/federation/identity", nil)
	if err != nil {
		return out, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("GET %s/federation/identity: %w", peerCoordinatorURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("peer %s returned HTTP %d for identity", peerCoordinatorURL, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("parse identity response: %w", err)
	}
	return out, nil
}

// eventsResponse mirrors GET /federation/ledger-events's body.
type eventsResponse struct {
	PodID  string        `json:"pod_id"`
	Events []LedgerEvent `json:"events"`
}

// FetchEventsSince retrieves a peer's own signed credit-issuance events with
// Sequence > since. federationKey authenticates this pull — the feed exposes
// every user_id + amount this pod has ever credited, which is sensitive
// enough (unlike identity/health) to require the shared federation
// credential rather than being wide open.
func (c *Client) FetchEventsSince(ctx context.Context, peerCoordinatorURL, federationKey string, since uint64) ([]LedgerEvent, error) {
	url := peerCoordinatorURL + "/federation/ledger-events?since=" + strconv.FormatUint(since, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+federationKey)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s/federation/ledger-events: %w", peerCoordinatorURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("peer %s returned HTTP %d for ledger-events", peerCoordinatorURL, resp.StatusCode)
	}
	var out eventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("parse ledger-events response: %w", err)
	}
	return out.Events, nil
}

// PeerBalance mirrors the existing settlement.Balance wire shape returned by
// GET /users/{id}/balance — duplicated here (rather than importing
// settlement) to keep this package's dependency footprint to protocol only.
type PeerBalance struct {
	GrantBalance  float64 `json:"grant_balance"`
	EarnedBalance float64 `json:"earned_balance"`
	Total         float64 `json:"total"`
}

// FetchBalance retrieves a peer's live, self-reported balance for userID —
// the number the audit endpoint checks against that same peer's witnessed
// signed event history.
func (c *Client) FetchBalance(ctx context.Context, peerCoordinatorURL, userID string) (PeerBalance, error) {
	var out PeerBalance
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, peerCoordinatorURL+"/users/"+userID+"/balance", nil)
	if err != nil {
		return out, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("GET %s/users/%s/balance: %w", peerCoordinatorURL, userID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return out, fmt.Errorf("peer %s returned HTTP %d for balance", peerCoordinatorURL, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, fmt.Errorf("parse balance response: %w", err)
	}
	return out, nil
}
