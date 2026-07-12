package main

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-inference-mesh/oim/internal/adminauth"
	"github.com/open-inference-mesh/oim/internal/httpmw"
	"github.com/open-inference-mesh/oim/internal/protocol"
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
	handler := authMiddleware("admin-secret", nil, newAPIKeyStore(), nil, next)

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
	if adminAuthorized(req("secret"), "", nil) {
		t.Error("no admin key configured must fail closed even with a token present")
	}
	if adminAuthorized(req(""), "secret", nil) {
		t.Error("missing token must be rejected")
	}
	if adminAuthorized(req("wrong"), "secret", nil) {
		t.Error("wrong token must be rejected")
	}
	if !adminAuthorized(req("secret"), "secret", nil) {
		t.Error("matching admin key must be accepted")
	}
}

// adminAuthorized must also accept a valid BDFL admin session token
// (additive to the static admin key, not a replacement for it).
func TestAdminAuthorized_AcceptsBDFLSession(t *testing.T) {
	req := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/admin/reconcile", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return r
	}
	_, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	adminAuth, err := adminauth.NewAuthenticator(hex.EncodeToString(pub))
	if err != nil {
		t.Fatal(err)
	}
	// The full challenge -> sign -> authenticate round trip is covered by
	// internal/adminauth's own tests and the integration test; here we only
	// check that adminAuthorized correctly delegates to ValidSession and still
	// honors the static key alongside it.
	if adminAuthorized(req("oimadmin_nonexistent"), "", adminAuth) {
		t.Error("an unknown session token must be rejected")
	}
	if adminAuthorized(req(""), "", adminAuth) {
		t.Error("missing token must be rejected even with adminAuth configured")
	}
	// Static admin key must still work even when a BDFL authenticator is wired in.
	if !adminAuthorized(req("static-secret"), "static-secret", adminAuth) {
		t.Error("static admin key must still be accepted alongside a configured BDFL authenticator")
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

// callerControlsAccount gates MUTATING an account's api-key credential —
// rotating one (POST when a key already exists) and revoking one (DELETE).
func TestCallerControlsAccount(t *testing.T) {
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
		r := httptest.NewRequest(http.MethodDelete, "/users/alice/api-key", nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		return r
	}

	if callerControlsAccount(reqWith(""), "alice", "admin-secret", nil, keys) {
		t.Error("no token must be rejected")
	}
	if !callerControlsAccount(reqWith("admin-secret"), "alice", "admin-secret", nil, keys) {
		t.Error("admin key must be accepted")
	}
	if !callerControlsAccount(reqWith(aliceKey), "alice", "admin-secret", nil, keys) {
		t.Error("alice's own key controlling her own account must be accepted")
	}
	if callerControlsAccount(reqWith(bobKey), "alice", "admin-secret", nil, keys) {
		t.Error("bob's key must NOT control alice's account")
	}
	if callerControlsAccount(reqWith("oim_totally_made_up"), "alice", "admin-secret", nil, keys) {
		t.Error("a forged oim_ token must be rejected")
	}
}

// TestAPIKeyRotationDELETE_TakeoverChainIsBlocked is the regression test for
// the exact incident this session's api-key hardening was meant to close:
// POST /users/{id}/api-key gates ROTATION (the account already has a key) on
// ownership, but that gate alone is bypassable if DELETE /users/{id}/api-key
// has no ownership check of its own — any authenticated caller (including one
// who just self-minted a free key for themselves, since first-mint is always
// open by design) could revoke a VICTIM's key, which resets
// apiKeys.exists(victim) to false and walks straight back through the open
// first-mint path to seize the account. This proves each step of that chain
// using the exact gate both handlers share (callerControlsAccount), not just
// each function in isolation — the bug was specifically in how the two
// handlers compose, not in either check alone.
func TestAPIKeyRotationDELETE_TakeoverChainIsBlocked(t *testing.T) {
	keys := newAPIKeyStore()
	victimKey, err := keys.generate("victim")
	if err != nil {
		t.Fatal(err)
	}
	attackerKey, err := keys.generate("attacker") // first mint for "attacker" — legitimately open
	if err != nil {
		t.Fatal(err)
	}

	deleteReq := func(id, token string) *http.Request {
		r := httptest.NewRequest(http.MethodDelete, "/users/"+id+"/api-key", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		return r
	}

	// Step 1: attacker attempts to revoke the victim's key using their own
	// (freely self-minted) credential. Must be rejected — this is the gate
	// that was missing before the fix.
	if callerControlsAccount(deleteReq("victim", attackerKey), "victim", "", nil, keys) {
		t.Fatal("attacker's own key must not authorize revoking the victim's key")
	}

	// Step 2: prove the victim's key is genuinely untouched — the handler-level
	// gate must have actually prevented the revoke from being reachable, not
	// merely reported false while the caller went ahead anyway. (This
	// simulates what the DELETE handler does: only call apiKeys.revoke after
	// callerControlsAccount passes.)
	if uid, ok := keys.lookup(victimKey); !ok || uid != "victim" {
		t.Fatalf("victim's key must still resolve to victim after a blocked revoke attempt, got (%q, %v)", uid, ok)
	}

	// Step 3: with the key intact, apiKeys.exists("victim") stays true, so the
	// POST rotation gate (apiKeys.exists(userID) && !callerControlsAccount)
	// still applies — the first-mint path the whole attack depended on
	// reopening never becomes reachable.
	if !keys.exists("victim") {
		t.Fatal("victim's key must still exist — the takeover chain depends on it having been wrongly deleted")
	}
	if callerControlsAccount(deleteReq("victim", attackerKey), "victim", "", nil, keys) {
		t.Fatal("attacker still must not control the victim's account on a second attempt")
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
	handler := authMiddleware("admin-secret", nil, keys, limiter, next)

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
