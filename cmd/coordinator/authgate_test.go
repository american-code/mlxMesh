package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-inference-mesh/oim/internal/httpmw"
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
	handler := authMiddleware("admin-secret", newAPIKeyStore(), nil, next)

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

// adminAuthorized gates /admin/reconcile: admin key required, fail closed when
// none is configured.
func TestAdminAuthorized(t *testing.T) {
	req := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/admin/reconcile", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return r
	}
	if adminAuthorized(req("secret"), "") {
		t.Error("no admin key configured must fail closed even with a token present")
	}
	if adminAuthorized(req(""), "secret") {
		t.Error("missing token must be rejected")
	}
	if adminAuthorized(req("wrong"), "secret") {
		t.Error("wrong token must be rejected")
	}
	if !adminAuthorized(req("secret"), "secret") {
		t.Error("matching admin key must be accepted")
	}
}

// authorizeUserRead is the gate for per-user reads under --protect-user-reads.
func TestAuthorizeUserRead(t *testing.T) {
	keys := newAPIKeyStore()
	aliceKey, err := keys.generate("alice")
	if err != nil {
		t.Fatal(err)
	}
	bobKey, err := keys.generate("bob")
	if err != nil {
		t.Fatal(err)
	}

	reqWith := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/users/alice/balance", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return r
	}

	// Protection OFF: everything allowed (backward-compatible open reads).
	if !authorizeUserRead(reqWith(""), "alice", false, "admin-secret", keys) {
		t.Error("protection off: unauthenticated read should be allowed")
	}

	// Protection ON.
	if authorizeUserRead(reqWith(""), "alice", true, "admin-secret", keys) {
		t.Error("protection on: no token must be rejected")
	}
	if !authorizeUserRead(reqWith("admin-secret"), "alice", true, "admin-secret", keys) {
		t.Error("protection on: admin key must be allowed")
	}
	if !authorizeUserRead(reqWith(aliceKey), "alice", true, "admin-secret", keys) {
		t.Error("protection on: alice's own key reading alice's balance must be allowed")
	}
	if authorizeUserRead(reqWith(bobKey), "alice", true, "admin-secret", keys) {
		t.Error("protection on: bob's key reading alice's balance must be rejected (no cross-account reads)")
	}
	if authorizeUserRead(reqWith("oim_totally_made_up"), "alice", true, "admin-secret", keys) {
		t.Error("protection on: a forged oim_ token must be rejected")
	}
}

// The per-account quota inside authMiddleware must throttle a single
// authenticated user_id independent of source IP, and must not throttle the
// admin key.
func TestAuthMiddleware_PerAccountQuota(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	keys := newAPIKeyStore()
	userKey, err := keys.generate("alice")
	if err != nil {
		t.Fatal(err)
	}
	// Rate 0/sec sustained with burst 2 → third authenticated call is throttled.
	limiter := httpmw.NewRateLimiter(0.0001, 2)
	defer limiter.Stop()
	handler := authMiddleware("admin-secret", keys, limiter, next)

	call := func(token string) int {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		return rec.Code
	}

	if code := call(userKey); code != http.StatusOK {
		t.Fatalf("first authenticated call = %d, want 200 (within burst)", code)
	}
	if code := call(userKey); code != http.StatusOK {
		t.Fatalf("second authenticated call = %d, want 200 (within burst)", code)
	}
	if code := call(userKey); code != http.StatusTooManyRequests {
		t.Errorf("third call for the same account = %d, want %d (quota exhausted)", code, http.StatusTooManyRequests)
	}
	// Admin key is exempt from the per-account quota.
	if code := call("admin-secret"); code != http.StatusOK {
		t.Errorf("admin key call = %d, want %d (admin exempt from per-account quota)", code, http.StatusOK)
	}
}
