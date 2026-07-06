package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// isSelfAuthenticatingWrite gates whether the outer admin Bearer-token check
// applies. Getting this wrong in either direction is a real incident: too
// narrow and node registration / wallet sign-in / new-user onboarding lock up
// the moment an operator sets --api-key (nothing can ever obtain that token
// for them); too broad and it defeats the point of the gate.
func TestIsSelfAuthenticatingWrite(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		// Self-signed node lifecycle — no Bearer token mechanism exists for a
		// node agent, and each of these verifies its own Ed25519 signature.
		{"POST", "/nodes/register", true},
		{"POST", "/nodes/abc123/refresh", true},
		{"POST", "/nodes/abc123/attest-enclave", true},
		{"POST", "/nodes/abc123/benchmark-result", true},
		{"POST", "/nodes/abc123/job-outcome", true},
		// Unsigned/forged is inert (only credits on internal signature success).
		{"POST", "/settlement/records", true},
		// Wallet bootstrap — the whole point is minting a credential; gating
		// it behind one is a lockout, not security.
		{"POST", "/account/challenge", true},
		{"POST", "/account/auth", true},
		{"POST", "/account/0xabc/link-device", true},
		// First api-key mint / startup-grant claim must be reachable before
		// the caller has any credential; startup-grant additionally carries
		// its own PoW + dedicated rate limit.
		{"POST", "/users/alice/api-key", true},
		{"POST", "/users/alice/startup-grant", true},
		// Coordination announce/withdraw — no signature scheme, additive/
		// informational only.
		{"POST", "/coordination/announce", true},
		{"POST", "/coordination/withdraw", true},

		// Billing/admin surface — this is what --api-key is actually meant to protect.
		{"POST", "/v1/chat/completions", false},
		{"POST", "/v1/reserve-node", false},
		{"POST", "/jobs/background/assign", false},
		{"POST", "/jobs/background/cycle", false},
		{"POST", "/jobs/background/execute", false},
		{"DELETE", "/nodes/abc123", false},
		{"DELETE", "/users/alice/api-key", false},
		// GET is handled by a separate check in authMiddleware, not this function.
		{"GET", "/nodes/register", false},
	}
	for _, c := range cases {
		if got := isSelfAuthenticatingWrite(c.method, c.path); got != c.want {
			t.Errorf("isSelfAuthenticatingWrite(%q, %q) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

// TestAuthMiddleware_NodeRegistrationNotLockedOut is the regression test for
// the actual incident: with --api-key set, a node agent (which has no way to
// send a Bearer token) must still be able to reach /nodes/register, while a
// billing endpoint without a token must still be rejected.
func TestAuthMiddleware_NodeRegistrationNotLockedOut(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := authMiddleware("admin-secret", newAPIKeyStore(), next)

	req := httptest.NewRequest(http.MethodPost, "/nodes/register", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("POST /nodes/register with no Authorization header = %d, want %d (node has no way to obtain a Bearer token)", rec.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/chat/completions with no Authorization header = %d, want %d", rec2.Code, http.StatusUnauthorized)
	}

	req3 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req3.Header.Set("Authorization", "Bearer admin-secret")
	rec3 := httptest.NewRecorder()
	handler.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("POST /v1/chat/completions with valid admin key = %d, want %d", rec3.Code, http.StatusOK)
	}
}
