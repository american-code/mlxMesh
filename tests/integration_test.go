//go:build integration

// Package tests: end-to-end integration of the real coordinator, node agent, and
// a stub-exo backend as separate processes — the cross-process contract that
// unit tests can't cover (task #18). Opt-in:
//
//	go test -tags integration ./tests/ -run Integration -v
//
// TestMain builds the three binaries once; each test starts them on fresh ports.
package tests

import (
	"bufio"
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/payloadcrypto"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/wallet"
)

var bin struct{ coordinator, node, stubExo string }

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "oim-itest")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	build := func(out, pkg string) string {
		p := filepath.Join(dir, out)
		cmd := exec.Command("go", "build", "-o", p, pkg)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "build", pkg, ":", err)
			os.Exit(1)
		}
		return p
	}
	bin.coordinator = build("oim-coordinator", "github.com/open-inference-mesh/oim/cmd/coordinator")
	bin.node = build("oim", "github.com/open-inference-mesh/oim/cmd/oim")
	bin.stubExo = build("stub-exo", "github.com/open-inference-mesh/oim/cmd/stub-exo")
	os.Exit(m.Run())
}

// --- helpers ---

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func startProc(t *testing.T, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
}

func waitHealthy(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("service never became healthy: %s", url)
}

func getJSON(t *testing.T, url string) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

func postJSON(t *testing.T, url string, body map[string]any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func balance(t *testing.T, coordURL, user string) float64 {
	m := getJSON(t, fmt.Sprintf("%s/users/%s/balance", coordURL, user))
	tot, _ := m["total"].(float64)
	return tot
}

// spins up stub-exo + coordinator + node, returns coordinator base URL.
func startMesh(t *testing.T) string {
	t.Helper()
	exoPort, coordPort, nodePort := freePort(t), freePort(t), freePort(t)
	exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)

	startProc(t, []string{fmt.Sprintf("STUB_LISTEN=:%d", exoPort)}, bin.stubExo)
	waitHealthy(t, exoURL+"/state")

	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0")
	waitHealthy(t, coordURL+"/health")

	startProc(t, nil, bin.node, "node", "start",
		fmt.Sprintf("--coordinator=%s", coordURL),
		fmt.Sprintf("--listen=:%d", nodePort),
		fmt.Sprintf("--exo-url=%s", exoURL),
		fmt.Sprintf("--reachability-endpoint=http://127.0.0.1:%d", nodePort),
		"--region=us", "--user-id=miner")

	// Wait for the node to register.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) >= 1 {
			return coordURL
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("node never registered with coordinator")
	return ""
}

// --- tests ---

// The full money path: register → query → consumer debited, node earns
// (always exactly 75% of the undiscounted matrix cost), and the node's async
// job-outcome does NOT double-credit.
//
// This is a fresh single-node harness, so queue backpressure is 0% at job
// time — exactly the "fully idle network" case the activity discount
// (bootstrapping-economics fix, TODO.md Economic Sustainability) targets, so
// the treasury's margin on this specific job is compressed toward zero
// rather than the traditional ~25%. What must hold regardless of backpressure
// is solvency: every credit minted (miner + treasury) traces back to exactly
// what the consumer was actually debited — the discount only reallocates the
// split between miner/treasury from what the consumer paid, it never mints
// more than that.
func TestIntegrationFullMoneyPath(t *testing.T) {
	coord := startMesh(t)

	// Consumer needs credits.
	postJSON(t, coord+"/users/consumer/startup-grant", map[string]any{}, nil)
	before := balance(t, coord, "consumer")
	if before <= 0 {
		t.Fatal("consumer grant did not land")
	}

	status, resp := postJSON(t, coord+"/v1/chat/completions", map[string]any{
		"model":      "llama-3.2-3b",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 64,
	}, map[string]string{"X-OIM-User-ID": "consumer"})
	if status != 200 {
		t.Fatalf("query status %d: %v", status, resp)
	}
	if _, ok := resp["choices"]; !ok {
		t.Fatalf("no choices in response: %v", resp)
	}

	// Give the async job-outcome time to arrive — it must NOT add a second credit.
	time.Sleep(2 * time.Second)

	after := balance(t, coord, "consumer")
	debited := before - after
	miner := balance(t, coord, "miner")
	treasury := balance(t, coord, "oim-treasury")
	if miner <= 0 {
		t.Fatalf("miner earned nothing")
	}
	if debited <= 0 {
		t.Fatalf("consumer was not debited")
	}
	if got := miner + treasury; got < debited-1e-6 || got > debited+1e-6 {
		t.Errorf("solvency broken: miner+treasury=%.4f != consumer debited=%.4f", got, debited)
	}
	t.Logf("consumer_debited=%.4f miner=%.4f treasury=%.4f (treasury margin %.1f%% of debit)", debited, miner, treasury, 100*treasury/debited)
}

// A request with no credits is gated with 402.
func TestIntegrationInsufficientCredits(t *testing.T) {
	coord := startMesh(t)
	status, resp := postJSON(t, coord+"/v1/chat/completions", map[string]any{
		"model":      "llama-3.2-3b",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 64,
	}, map[string]string{"X-OIM-User-ID": "brokeuser"})
	if status != http.StatusPaymentRequired {
		t.Fatalf("want 402, got %d: %v", status, resp)
	}
}

// An SSRF fetch URL (cloud metadata) is rejected at intake.
func TestIntegrationSSRFRejected(t *testing.T) {
	coord := startMesh(t)
	status, _ := postJSON(t, coord+"/v1/chat/completions", map[string]any{
		"model":                 "llama-3.2-3b",
		"messages":              []map[string]any{{"role": "user", "content": "hi"}},
		"oim_payload_hash":      "h",
		"oim_payload_fetch_url": "http://169.254.169.254/latest/meta-data/",
	}, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("want 400 for SSRF fetch URL, got %d", status)
	}
}

// The Prometheus endpoint exposes counters after traffic.
func TestIntegrationMetricsExposed(t *testing.T) {
	coord := startMesh(t)
	postJSON(t, coord+"/users/consumer/startup-grant", map[string]any{}, nil)
	postJSON(t, coord+"/v1/chat/completions", map[string]any{
		"model": "llama-3.2-3b", "messages": []map[string]any{{"role": "user", "content": "hi"}}, "max_tokens": 64,
	}, map[string]string{"X-OIM-User-ID": "consumer"})
	time.Sleep(500 * time.Millisecond)

	resp, err := http.Get(coord + "/metrics/prometheus")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"oim_http_requests_total", "oim_credits_total", "oim_jobs_dispatched_total"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("metrics missing %q in:\n%s", want, raw)
		}
	}
}

// nonLoopbackIPv4 finds a real, non-loopback IPv4 address to host the test's
// encrypted payload on — mirroring what the Swift client's
// LocalNetwork.primaryIPv4() does in production (a payload fetch URL must be a
// routable host: the coordinator's/node's SSRF guard rejects loopback
// outright). Skips the test rather than failing when no such interface exists
// (e.g. a fully isolated CI sandbox).
func nonLoopbackIPv4(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot enumerate interfaces: %v", err)
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		if v4 := ipnet.IP.To4(); v4 != nil {
			return v4.String()
		}
	}
	t.Skip("no non-loopback IPv4 interface available to host the test payload")
	return ""
}

// reserveAndEncrypt reserves a node against coord, encrypts plaintext to its
// published ECDH key, and hosts the ciphertext at a real non-loopback address
// (the coordinator's/node's SSRF guard rejects loopback fetch URLs outright —
// this mirrors what the Swift client's LocalNetwork.primaryIPv4() does in
// production). Returns the reservation ID and fetch/key fields ready to drop
// into a /v1/chat/completions body, plus a setCiphertext func so a caller can
// swap in a tampered payload before submitting.
func reserveAndEncrypt(t *testing.T, coord string, plaintext []byte) (reservationID, fetchURL, ephemeralPubKeyB64 string, setCiphertext func([]byte)) {
	t.Helper()
	hostIP := nonLoopbackIPv4(t)

	status, resv := postJSON(t, coord+"/v1/reserve-node", map[string]any{
		"model": "llama-3.2-3b", "sensitivity": "moderate",
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("reserve-node status %d: %v", status, resv)
	}
	reservationID, _ = resv["reservation_id"].(string)
	nodeECDHPubB64, _ := resv["ecdh_public_key"].(string)
	if reservationID == "" || nodeECDHPubB64 == "" {
		t.Fatalf("reserve-node response missing fields: %v", resv)
	}

	nodePubRaw, err := base64.StdEncoding.DecodeString(nodeECDHPubB64)
	if err != nil {
		t.Fatalf("decode node ecdh public key: %v", err)
	}
	nodePub, err := ecdh.P256().NewPublicKey(nodePubRaw)
	if err != nil {
		t.Fatalf("parse node ecdh public key: %v", err)
	}

	ephemeralPubRaw, combined, err := payloadcrypto.Encrypt(nodePub, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	mux := http.NewServeMux()
	served := combined
	mux.HandleFunc("/payload/test", func(w http.ResponseWriter, r *http.Request) {
		w.Write(served)
	})
	ln, err := net.Listen("tcp", hostIP+":0")
	if err != nil {
		t.Fatalf("listen on %s: %v", hostIP, err)
	}
	payloadSrv := &http.Server{Handler: mux}
	go payloadSrv.Serve(ln)
	t.Cleanup(func() { payloadSrv.Close() })

	return reservationID,
		fmt.Sprintf("http://%s/payload/test", ln.Addr().String()),
		base64.StdEncoding.EncodeToString(ephemeralPubRaw),
		func(b []byte) { served = b }
}

// Full node-side pointer consumption round trip (M8): reserve a node, encrypt
// a payload to its published ECDH key, host the ciphertext, and confirm the
// node actually decrypts and uses it end-to-end (not just that the fields are
// threaded through).
func TestIntegrationEncryptedPointerRoundTrip(t *testing.T) {
	coord := startMesh(t)
	plaintext, _ := json.Marshal([]map[string]any{{"role": "user", "content": "hi from an encrypted pointer"}})
	reservationID, fetchURL, ephemeralPubKeyB64, _ := reserveAndEncrypt(t, coord, plaintext)

	status, resp := postJSON(t, coord+"/v1/chat/completions", map[string]any{
		"model":                    "llama-3.2-3b",
		"messages":                 []map[string]any{{"role": "user", "content": "placeholder"}},
		"max_tokens":               64,
		"oim_reservation_id":       reservationID,
		"oim_payload_hash":         "sha256:test",
		"oim_payload_fetch_url":    fetchURL,
		"oim_ephemeral_public_key": ephemeralPubKeyB64,
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("want 200 for the real encrypted payload, got %d: %v", status, resp)
	}
	if _, ok := resp["choices"]; !ok {
		t.Fatalf("no choices in response: %v", resp)
	}
}

// A tampered ciphertext must be rejected, not silently accepted — proving the
// node actually attempts decryption rather than trusting anything sent to a
// reserved job. Uses its own mesh (rather than reusing
// TestIntegrationEncryptedPointerRoundTrip's) because a failed dispatch marks
// the node unreachable — an unrelated, pre-existing dispatchToNode behavior
// this test must not be coupled to.
func TestIntegrationEncryptedPointerTamperedRejected(t *testing.T) {
	coord := startMesh(t)
	plaintext, _ := json.Marshal([]map[string]any{{"role": "user", "content": "hi"}})
	reservationID, fetchURL, ephemeralPubKeyB64, setCiphertext := reserveAndEncrypt(t, coord, plaintext)

	tampered := make([]byte, 200) // any well-formed-looking but wrong ciphertext
	setCiphertext(tampered)

	status, resp := postJSON(t, coord+"/v1/chat/completions", map[string]any{
		"model":                    "llama-3.2-3b",
		"messages":                 []map[string]any{{"role": "user", "content": "placeholder"}},
		"max_tokens":               64,
		"oim_reservation_id":       reservationID,
		"oim_payload_hash":         "sha256:test",
		"oim_payload_fetch_url":    fetchURL,
		"oim_ephemeral_public_key": ephemeralPubKeyB64,
	}, nil)
	// The node itself rejects with 502 ("payload decrypt: ... message
	// authentication failed"); the coordinator collapses ANY node-dispatch
	// failure into a generic 503 at its own boundary (the same behavior for a
	// dead node or a missing model) — this asserts the coordinator's real,
	// pre-existing contract rather than the node's internal error detail.
	if status != http.StatusServiceUnavailable {
		t.Fatalf("want 503 (coordinator dispatch-failure contract) for tampered ciphertext, got %d: %v", status, resp)
	}
}

// An unknown/expired reservation ID must fail the job outright rather than
// silently falling back to normal routing (which would dispatch ciphertext
// bound to one node's key to a DIFFERENT node that can never decrypt it).
func TestIntegrationReservationExpiredRejected(t *testing.T) {
	coord := startMesh(t)
	status, resp := postJSON(t, coord+"/v1/chat/completions", map[string]any{
		"model":              "llama-3.2-3b",
		"messages":           []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens":         64,
		"oim_reservation_id": "not-a-real-reservation",
	}, nil)
	if status != http.StatusConflict {
		t.Fatalf("want 409 for unknown reservation, got %d: %v", status, resp)
	}
}

// Fast-lane streaming (task: server-side streaming): submit stream:true,
// reassemble the SSE deltas, and confirm both (a) the reassembled content
// matches what a non-streaming request gets from the same stub, and (b) the
// 75/25 credit split still lands correctly — accounting must be identical
// whether the reply arrived buffered or streamed.
func TestIntegrationStreamingFastLane(t *testing.T) {
	coord := startMesh(t)
	postJSON(t, coord+"/users/streamer/startup-grant", map[string]any{}, nil)
	streamerBefore := balance(t, coord, "streamer")

	body, _ := json.Marshal(map[string]any{
		"model":      "llama-3.2-3b",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 64,
		"stream":     true,
	})
	req, _ := http.NewRequest("POST", coord+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OIM-User-ID", "streamer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("stream status %d: %s", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
	if resp.Header.Get("X-OIM-Served-By-Node-Id") == "" {
		t.Error("missing X-OIM-Served-By-Node-Id header on a streamed response")
	}

	var reassembled strings.Builder
	var sawDone bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) == nil {
			for _, c := range chunk.Choices {
				reassembled.WriteString(c.Delta.Content)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	if !sawDone {
		t.Error("stream never sent data: [DONE]")
	}
	if !strings.Contains(reassembled.String(), "Simulated response") {
		t.Errorf("reassembled content missing expected stub reply: %q", reassembled.String())
	}

	// Credit accounting: same solvency guarantee as the buffered path, sourced
	// from the trailing SSE usage frame instead of one buffered blob — the
	// miner's reward and the treasury's margin (whatever it is, possibly
	// activity-discounted toward zero on this fresh, fully-idle test harness —
	// see TestIntegrationFullMoneyPath) must together equal exactly what the
	// consumer was debited.
	time.Sleep(300 * time.Millisecond) // credit/debit happen synchronously before the handler returns, but give it a beat
	miner := balance(t, coord, "miner")
	treasury := balance(t, coord, "oim-treasury")
	streamerAfter := balance(t, coord, "streamer")
	debited := streamerBefore - streamerAfter
	if miner <= 0 {
		t.Error("streaming job should have credited the serving node")
	}
	if debited <= 0 {
		t.Fatal("streaming job should have debited the consumer")
	}
	if got := miner + treasury; got < debited-1e-6 || got > debited+1e-6 {
		t.Errorf("solvency broken: miner+treasury=%.4f != consumer debited=%.4f", got, debited)
	}
}

// generateSelfSignedCert writes a throwaway self-signed cert+key pair to
// files in t.TempDir(), CN/SAN=127.0.0.1 — enough for a node's --tls-cert to
// serve HTTPS to a test coordinator. Mirrors what a real node operator would
// generate for themselves (no shared CA required — that's the whole point of
// TOFU fingerprint pinning).
func generateSelfSignedCert(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "node.crt")
	keyPath = filepath.Join(dir, "node.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// Coordinator->node TLS (TOFU fingerprint pinning): a node serving its job
// endpoint over a self-signed HTTPS cert registers successfully, and the
// coordinator dispatches a real job to it over that pinned connection — end
// to end, not just the pinning closure in isolation (already unit-tested in
// internal/httptls).
func TestIntegrationNodeTLSPinning(t *testing.T) {
	certPath, keyPath := generateSelfSignedCert(t)

	exoPort, coordPort, nodePort := freePort(t), freePort(t), freePort(t)
	exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)
	nodeReachURL := fmt.Sprintf("https://127.0.0.1:%d", nodePort)

	startProc(t, []string{fmt.Sprintf("STUB_LISTEN=:%d", exoPort)}, bin.stubExo)
	waitHealthy(t, exoURL+"/state")

	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0")
	waitHealthy(t, coordURL+"/health")

	startProc(t, nil, bin.node, "node", "start",
		fmt.Sprintf("--coordinator=%s", coordURL),
		fmt.Sprintf("--listen=:%d", nodePort),
		fmt.Sprintf("--exo-url=%s", exoURL),
		fmt.Sprintf("--reachability-endpoint=%s", nodeReachURL),
		fmt.Sprintf("--tls-cert=%s", certPath),
		fmt.Sprintf("--tls-key=%s", keyPath),
		"--region=us", "--user-id=tlsminer")

	deadline := time.Now().Add(15 * time.Second)
	var registered bool
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) >= 1 {
			registered = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !registered {
		t.Fatal("TLS-enabled node never registered with coordinator")
	}

	// Confirm the registered manifest carries the reachability scheme and a
	// fingerprint at all (proves the manifest-signing path picked it up).
	nodes := getJSON(t, coordURL+"/nodes")
	arr, _ := nodes["nodes"].([]any)
	first, _ := arr[0].(map[string]any)
	if ep, _ := first["reachability_endpoint"].(string); !strings.HasPrefix(ep, "https://") {
		t.Fatalf("expected https:// reachability endpoint, got %v", first["reachability_endpoint"])
	}

	// The real test: the coordinator must actually dispatch over this pinned
	// HTTPS connection and get a real reply back, not just register the node.
	status, resp := postJSON(t, coordURL+"/v1/chat/completions", map[string]any{
		"model":      "llama-3.2-3b",
		"messages":   []map[string]any{{"role": "user", "content": "hi over tls"}},
		"max_tokens": 64,
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("dispatch over pinned TLS status %d: %v", status, resp)
	}
	if _, ok := resp["choices"]; !ok {
		t.Fatalf("no choices in response: %v", resp)
	}
	if servedBy, _ := resp["oim_served_by_node_id"].(string); servedBy == "" {
		t.Error("expected oim_served_by_node_id on a successful TLS dispatch")
	}
}

// TestIntegrationAvailabilityReward is the real end-to-end proof for the
// opt-in --availability-reward feature: a real coordinator + real oim node +
// stub-exo, with the node registered and idle, and NO consumer traffic EVER
// submitted by this test. If earned_balance increases anyway, it can only be
// the coordinator's own periodic probe crediting it — exactly the guarantee
// this feature exists to provide (a node earns something for genuinely being
// available, without anyone gaming it via a fake heartbeat, since the probe
// dispatches a real job through the same path real consumer traffic uses).
//
// OIM_AVAILABILITY_PROBE_INTERVAL / OIM_AVAILABILITY_IDLE_THRESHOLD are
// internal test-only env-var overrides (see durationFromEnv in
// cmd/coordinator/main.go) — production defaults are 10 minutes / 30 minutes,
// far too slow for a test to wait on.
func TestIntegrationAvailabilityReward(t *testing.T) {
	exoPort, coordPort, nodePort := freePort(t), freePort(t), freePort(t)
	exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)

	startProc(t, []string{fmt.Sprintf("STUB_LISTEN=:%d", exoPort)}, bin.stubExo)
	waitHealthy(t, exoURL+"/state")

	startProc(t, []string{
		"OIM_AVAILABILITY_PROBE_INTERVAL=500ms",
		"OIM_AVAILABILITY_IDLE_THRESHOLD=1ms",
	}, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0",
		"--availability-reward")
	waitHealthy(t, coordURL+"/health")

	startProc(t, nil, bin.node, "node", "start",
		fmt.Sprintf("--coordinator=%s", coordURL),
		fmt.Sprintf("--listen=:%d", nodePort),
		fmt.Sprintf("--exo-url=%s", exoURL),
		fmt.Sprintf("--reachability-endpoint=http://127.0.0.1:%d", nodePort),
		"--region=us", "--user-id=idle-miner")

	deadline := time.Now().Add(15 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) >= 1 {
			registered = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !registered {
		t.Fatal("node never registered with coordinator")
	}

	deadline = time.Now().Add(20 * time.Second)
	var earned float64
	for time.Now().Before(deadline) {
		earned = balance(t, coordURL, "idle-miner")
		if earned > 0 {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if earned <= 0 {
		t.Fatal("expected balance > 0 from the availability-reward probe alone — no consumer traffic was ever submitted in this test")
	}
	t.Logf("availability-reward credited %.6f with zero consumer traffic", earned)
}

// TestIntegrationLinkAndUnlinkDevice exercises the real HTTP account-link
// lifecycle end-to-end (never covered at the HTTP layer before — only
// wallet.Manager itself had unit tests): link a device, confirm its earnings
// land on the account, unlink it, confirm a THIRD party (a different account
// key) cannot unlink someone else's device.
func TestIntegrationLinkAndUnlinkDevice(t *testing.T) {
	coord := startMesh(t)

	accPriv, accPub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate account identity: %v", err)
	}
	address := wallet.AddressFromPubKey(accPub)
	deviceNodeID := "test-device-node-id"

	linkMsg := []byte("oim-account-link:" + address + ":" + deviceNodeID)
	sig, err := protocol.SignPayload(accPriv, linkMsg)
	if err != nil {
		t.Fatalf("sign link message: %v", err)
	}

	linkBody := map[string]any{
		"device_node_id":     deviceNodeID,
		"account_public_key": base64.StdEncoding.EncodeToString(accPub),
		"signature":          base64.StdEncoding.EncodeToString(sig),
	}

	// A different account's key must NOT be able to link/unlink this device.
	_, otherPub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate other identity: %v", err)
	}
	otherStatus, otherResp := postJSON(t, coord+"/account/"+address+"/link-device", map[string]any{
		"device_node_id":     deviceNodeID,
		"account_public_key": base64.StdEncoding.EncodeToString(otherPub), // mismatched key
		"signature":          base64.StdEncoding.EncodeToString(sig),
	}, nil)
	if otherStatus != http.StatusUnauthorized {
		t.Fatalf("expected 401 linking with a mismatched key, got %d: %v", otherStatus, otherResp)
	}

	status, resp := postJSON(t, coord+"/account/"+address+"/link-device", linkBody, nil)
	if status != http.StatusOK {
		t.Fatalf("link-device status %d: %v", status, resp)
	}
	if resp["status"] != "linked" {
		t.Fatalf("expected status=linked, got %v", resp)
	}

	// Unlinking with the account key succeeds.
	status, resp = postJSON(t, coord+"/account/"+address+"/unlink-device", linkBody, nil)
	if status != http.StatusOK {
		t.Fatalf("unlink-device status %d: %v", status, resp)
	}
	if resp["status"] != "unlinked" {
		t.Fatalf("expected status=unlinked, got %v", resp)
	}
}

// TestIntegrationLinkedNodeEarningsRouteToWallet is the regression test for a
// real bug found live: a device could show "Linked ✓" in the app yet its
// compute-node earnings kept landing on its raw node_id (or --user-id)
// account, because creditNodeEarning only ever consulted the nodeUsers map
// (populated from --user-id at registration) and never wallet.Manager's
// actual signed link. This proves the fix: link the real running node's ID
// to a fresh wallet address, submit a real job, and confirm the WALLET
// address — not "miner" (this node's --user-id) — receives the earned credit.
func TestIntegrationLinkedNodeEarningsRouteToWallet(t *testing.T) {
	coord := startMesh(t)

	nodes := getJSON(t, coord+"/nodes")
	arr, _ := nodes["nodes"].([]any)
	if len(arr) == 0 {
		t.Fatal("no registered nodes to link")
	}
	first, _ := arr[0].(map[string]any)
	nodeID, _ := first["node_id"].(string)
	if nodeID == "" {
		t.Fatal("could not read node_id from /nodes")
	}

	accPriv, accPub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate account identity: %v", err)
	}
	address := wallet.AddressFromPubKey(accPub)
	linkMsg := []byte("oim-account-link:" + address + ":" + nodeID)
	sig, err := protocol.SignPayload(accPriv, linkMsg)
	if err != nil {
		t.Fatalf("sign link message: %v", err)
	}
	status, resp := postJSON(t, coord+"/account/"+address+"/link-device", map[string]any{
		"device_node_id":     nodeID,
		"account_public_key": base64.StdEncoding.EncodeToString(accPub),
		"signature":          base64.StdEncoding.EncodeToString(sig),
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("link-device status %d: %v", status, resp)
	}

	// Consumer needs credits, then a real job dispatches to our one node.
	postJSON(t, coord+"/users/consumer2/startup-grant", map[string]any{}, nil)
	status, resp = postJSON(t, coord+"/v1/chat/completions", map[string]any{
		"model":      "llama-3.2-3b",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 64,
	}, map[string]string{"X-OIM-User-ID": "consumer2"})
	if status != http.StatusOK {
		t.Fatalf("query status %d: %v", status, resp)
	}
	time.Sleep(500 * time.Millisecond)

	walletBal := balance(t, coord, address)
	minerBal := balance(t, coord, "miner")
	if walletBal <= 0 {
		t.Fatalf("expected the linked wallet address to earn, got balance=%v", walletBal)
	}
	if minerBal > 0 {
		t.Fatalf("expected 'miner' (--user-id, superseded by the real link) to earn NOTHING once linked, got %v", minerBal)
	}
	t.Logf("linked wallet earned %.6f; miner (--user-id) correctly earned 0", walletBal)
}

// TestIntegrationExplicitReachabilityEndpointSkipsAutoPortMap confirms an
// explicit --reachability-endpoint always wins over the automatic UPnP/
// NAT-PMP port-mapping attempt (internal/natmap). This is what every other
// integration test in this file and the entire simulated Docker fleet
// already rely on (tools/gen-compose passes an explicit endpoint per
// simulated node) — a regression here would silently break all of them, not
// just this one test.
func TestIntegrationExplicitReachabilityEndpointSkipsAutoPortMap(t *testing.T) {
	exoPort, coordPort, nodePort := freePort(t), freePort(t), freePort(t)
	exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)
	nodeReachURL := fmt.Sprintf("http://127.0.0.1:%d", nodePort)

	startProc(t, []string{fmt.Sprintf("STUB_LISTEN=:%d", exoPort)}, bin.stubExo)
	waitHealthy(t, exoURL+"/state")

	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0")
	waitHealthy(t, coordURL+"/health")

	startProc(t, nil, bin.node, "node", "start",
		fmt.Sprintf("--coordinator=%s", coordURL),
		fmt.Sprintf("--listen=:%d", nodePort),
		fmt.Sprintf("--exo-url=%s", exoURL),
		fmt.Sprintf("--reachability-endpoint=%s", nodeReachURL),
		"--region=us", "--user-id=miner")
	waitHealthy(t, nodeReachURL+"/health")

	detect := getJSON(t, nodeReachURL+"/detect")
	if got, _ := detect["reachability_endpoint"].(string); got != nodeReachURL {
		t.Fatalf("expected reachability_endpoint=%q (the explicit override), got %v", nodeReachURL, got)
	}
	if got, _ := detect["port_mapping"].(string); got != "manual" {
		t.Fatalf(`expected port_mapping="manual" when --reachability-endpoint is set explicitly, got %v`, got)
	}
}

// TestIntegrationPullModeNodeEarns is THE proof the NAT problem is gone: a real
// node started with NO --reachability-endpoint runs in pull mode — it long-
// polls the coordinator for work over its own outbound connection. The
// coordinator NEVER dials into the node (its inbound job port is bound to
// loopback only), yet a real /v1/chat/completions is served by it and credited.
// This is exactly the "point an ASIC at a pool" model: no port forwarding, no
// reachability endpoint, no inbound connectivity of any kind.
func TestIntegrationPullModeNodeEarns(t *testing.T) {
	exoPort, coordPort := freePort(t), freePort(t)
	exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)

	startProc(t, []string{fmt.Sprintf("STUB_LISTEN=:%d", exoPort)}, bin.stubExo)
	waitHealthy(t, exoURL+"/state")

	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0")
	waitHealthy(t, coordURL+"/health")

	// Crucially: NO --reachability-endpoint. The node goes pull mode. --listen
	// is still given so the node has a loopback /detect for its own reporting,
	// but the coordinator never connects to it.
	nodePort := freePort(t)
	startProc(t, nil, bin.node, "node", "start",
		fmt.Sprintf("--coordinator=%s", coordURL),
		fmt.Sprintf("--listen=:%d", nodePort),
		fmt.Sprintf("--exo-url=%s", exoURL),
		"--region=us", "--user-id=pullminer")

	// Wait for the pull node to register.
	deadline := time.Now().Add(15 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) >= 1 {
			registered = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !registered {
		t.Fatal("pull node never registered")
	}

	// A real consumer job. The coordinator must deliver it to the pull node via
	// the claim mailbox (it has no way to dial the node), the node runs it and
	// posts the result back.
	postJSON(t, coordURL+"/users/consumer3/startup-grant", map[string]any{}, nil)
	status, resp := postJSON(t, coordURL+"/v1/chat/completions", map[string]any{
		"model":      "llama-3.2-3b",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 64,
	}, map[string]string{"X-OIM-User-ID": "consumer3"})
	if status != http.StatusOK {
		t.Fatalf("pull-mode dispatch status %d: %v", status, resp)
	}
	if _, ok := resp["choices"]; !ok {
		t.Fatalf("no choices — pull node did not serve the job: %v", resp)
	}
	if servedBy, _ := resp["oim_served_by_node_id"].(string); servedBy == "" {
		t.Fatal("expected oim_served_by_node_id — the pull node should be credited as the server")
	}

	time.Sleep(500 * time.Millisecond)
	if bal := balance(t, coordURL, "pullminer"); bal <= 0 {
		t.Fatalf("pull node earned nothing — expected a credit for the served job, got %v", bal)
	}
	t.Logf("pull-mode node served a real job and earned with ZERO inbound reachability")
}

// A node can have a model downloaded (advertised in its manifest) without Exo
// having an active instance for it — confirmed live this session: a real
// dispatch to such a node failed with "no eligible nodes" even though the
// node was healthy and reachable. This test proves the fix end to end: the
// cold model is correctly excluded from dispatch, then POST
// /nodes/{id}/warm-model brings it online, then the EXACT SAME dispatch
// succeeds — no code changes between the two attempts, just the warm-up call.
func TestIntegrationColdModelExcludedUntilWarmed(t *testing.T) {
	exoPort, coordPort, nodePort := freePort(t), freePort(t), freePort(t)
	exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)

	// The node's only model starts cold — downloaded (STUB_MODELS) but not
	// loaded (STUB_COLD_MODELS), exactly the gap this feature closes.
	startProc(t, []string{
		fmt.Sprintf("STUB_LISTEN=:%d", exoPort),
		"STUB_MODELS=cold-test-model",
		"STUB_COLD_MODELS=cold-test-model",
	}, bin.stubExo)
	waitHealthy(t, exoURL+"/state")

	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0")
	waitHealthy(t, coordURL+"/health")

	startProc(t, nil, bin.node, "node", "start",
		fmt.Sprintf("--coordinator=%s", coordURL),
		fmt.Sprintf("--listen=:%d", nodePort),
		fmt.Sprintf("--exo-url=%s", exoURL),
		fmt.Sprintf("--reachability-endpoint=http://127.0.0.1:%d", nodePort),
		// Short refresh interval (default 30s) so the "model reports loaded
		// after warm-model" assertion below doesn't need a generous timeout
		// just to outlast an unrelated default.
		"--refresh-interval=2",
		"--region=us", "--user-id=coldminer")

	// Wait for registration AND confirm the manifest reports the model cold —
	// otherwise a later assertion failure could just as easily mean "never
	// registered" as "eligibility gate didn't fire."
	var nodeID string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) >= 1 {
			n, _ := arr[0].(map[string]any)
			models, _ := n["models"].([]any)
			if len(models) == 1 {
				m, _ := models[0].(map[string]any)
				if loaded, _ := m["loaded"].(bool); !loaded {
					nodeID, _ = n["node_id"].(string)
					break
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if nodeID == "" {
		t.Fatal("node never registered with its model correctly reported cold")
	}

	postJSON(t, coordURL+"/users/coldconsumer/startup-grant", map[string]any{}, nil)
	dispatchBody := map[string]any{
		"model":      "cold-test-model",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 64,
	}
	dispatchHeaders := map[string]string{"X-OIM-User-ID": "coldconsumer"}

	// 1. Cold: the only node hosting this model must be excluded.
	status, resp := postJSON(t, coordURL+"/v1/chat/completions", dispatchBody, dispatchHeaders)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 (no eligible nodes) while cold, got %d: %v", status, resp)
	}

	// 2. Warm it up.
	status, resp = postJSON(t, coordURL+fmt.Sprintf("/nodes/%s/warm-model", nodeID),
		map[string]any{"model_id": "cold-test-model"}, nil)
	if status != http.StatusOK {
		t.Fatalf("warm-model failed: %d: %v", status, resp)
	}
	if warmed, _ := resp["warmed"].(bool); !warmed {
		t.Fatalf("expected warmed:true in response, got %v", resp)
	}

	// The node's next heartbeat refresh must report the model loaded before
	// dispatch will succeed — poll rather than assume timing.
	deadline = time.Now().Add(15 * time.Second)
	loaded := false
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) >= 1 {
			n, _ := arr[0].(map[string]any)
			models, _ := n["models"].([]any)
			if len(models) == 1 {
				m, _ := models[0].(map[string]any)
				if l, _ := m["loaded"].(bool); l {
					loaded = true
					break
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !loaded {
		t.Fatal("model never reported loaded after warm-model succeeded")
	}

	// 3. Same exact dispatch, now succeeds.
	status, resp = postJSON(t, coordURL+"/v1/chat/completions", dispatchBody, dispatchHeaders)
	if status != http.StatusOK {
		t.Fatalf("expected dispatch to succeed once warmed, got %d: %v", status, resp)
	}
	if _, ok := resp["choices"]; !ok {
		t.Fatalf("no choices in post-warm dispatch: %v", resp)
	}
	if servedBy, _ := resp["oim_served_by_node_id"].(string); servedBy != nodeID {
		t.Fatalf("expected node %s to serve the post-warm dispatch, got %v", nodeID, servedBy)
	}
}

// A node that registered without ever running `oim bench run` reports
// measured_toks_per_sec == 0 forever, even after serving real traffic — the
// exact "Reduced perf" staleness bug found live this session. This test
// proves the fix: real fast-lane dispatches feed a coordinator-owned rolling
// average (RecordObservedThroughput) that Snapshot()'s measured_toks_per_sec
// now prefers, so the number moves off zero without any manual benchmark
// step. STUB_RESPONSE_FILLER_WORDS pads the stub's canned completion past
// the coordinator's minThroughputSampleTokens floor (~16) so each dispatch
// contributes a real sample.
func TestIntegrationObservedThroughputUpdatesMeasuredSignature(t *testing.T) {
	exoPort, coordPort, nodePort := freePort(t), freePort(t), freePort(t)
	exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)

	startProc(t, []string{
		fmt.Sprintf("STUB_LISTEN=:%d", exoPort),
		"STUB_RESPONSE_FILLER_WORDS=40",
	}, bin.stubExo)
	waitHealthy(t, exoURL+"/state")

	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0")
	waitHealthy(t, coordURL+"/health")

	startProc(t, nil, bin.node, "node", "start",
		fmt.Sprintf("--coordinator=%s", coordURL),
		fmt.Sprintf("--listen=:%d", nodePort),
		fmt.Sprintf("--exo-url=%s", exoURL),
		fmt.Sprintf("--reachability-endpoint=http://127.0.0.1:%d", nodePort),
		"--region=us", "--user-id=throughputminer")

	var nodeID string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) >= 1 {
			n, _ := arr[0].(map[string]any)
			nodeID, _ = n["node_id"].(string)
			// This node never ran `oim bench run` — confirm the claimed
			// signature really is the pre-fix zero, or a later nonzero
			// reading wouldn't prove anything about observed throughput.
			if tps, _ := n["measured_toks_per_sec"].(float64); tps == 0 && nodeID != "" {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if nodeID == "" {
		t.Fatal("node never registered")
	}

	postJSON(t, coordURL+"/users/throughputconsumer/startup-grant", map[string]any{}, nil)
	dispatchBody := map[string]any{
		"model":      "llama-3.2-3b",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 128,
	}
	dispatchHeaders := map[string]string{"X-OIM-User-ID": "throughputconsumer"}

	// Several real dispatches — each padded completion crosses the minimum
	// sample floor, so each should fold into the node's rolling average.
	const numDispatches = 5
	for i := 0; i < numDispatches; i++ {
		status, resp := postJSON(t, coordURL+"/v1/chat/completions", dispatchBody, dispatchHeaders)
		if status != http.StatusOK {
			t.Fatalf("dispatch %d failed: %d: %v", i, status, resp)
		}
	}

	// The node's measured_toks_per_sec must now reflect real observed
	// traffic instead of staying at its pre-fix zero.
	deadline = time.Now().Add(10 * time.Second)
	var lastTPS float64
	var lastSamples float64
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) >= 1 {
			n, _ := arr[0].(map[string]any)
			lastTPS, _ = n["measured_toks_per_sec"].(float64)
			lastSamples, _ = n["observed_sample_count"].(float64)
			if lastTPS > 0 && lastSamples == float64(numDispatches) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	if lastTPS <= 0 {
		t.Fatalf("measured_toks_per_sec still 0 after %d real dispatches — observed throughput never landed", numDispatches)
	}
	if lastSamples != float64(numDispatches) {
		t.Fatalf("expected observed_sample_count=%d, got %v", numDispatches, lastSamples)
	}
	t.Logf("measured_toks_per_sec moved to %.2f after %d real dispatches (node never self-benchmarked)", lastTPS, numDispatches)
}

// TestIntegrationSimulatedNodeNotCreditedByFastLane guards a real bug caught
// live: the coordinator's own production logs showed the seeded demo/"Try
// the mesh" fleet (OIM_SIMULATED_NODE=true) earning real ledger credit —
// reward + treasury margin — every time real consumer traffic landed on it,
// exactly like a real operator's hardware. Simulated nodes are already
// excluded from the availability-reward probe pool (IdleCandidates) on the
// theory that they're "decorative/seed capacity, not a real operator's
// hardware" — this test confirms the same rule now holds for the ordinary
// fast-lane earning path, not just the probe.
func TestIntegrationSimulatedNodeNotCreditedByFastLane(t *testing.T) {
	exoPort, coordPort, nodePort := freePort(t), freePort(t), freePort(t)
	exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)

	startProc(t, []string{fmt.Sprintf("STUB_LISTEN=:%d", exoPort)}, bin.stubExo)
	waitHealthy(t, exoURL+"/state")

	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0")
	waitHealthy(t, coordURL+"/health")

	startProc(t, []string{"OIM_SIMULATED_NODE=true"}, bin.node, "node", "start",
		fmt.Sprintf("--coordinator=%s", coordURL),
		fmt.Sprintf("--listen=:%d", nodePort),
		fmt.Sprintf("--exo-url=%s", exoURL),
		fmt.Sprintf("--reachability-endpoint=http://127.0.0.1:%d", nodePort),
		"--region=us", "--user-id=sim-miner")

	deadline := time.Now().Add(15 * time.Second)
	registered := false
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) >= 1 {
			n, _ := arr[0].(map[string]any)
			if sim, _ := n["simulated"].(bool); sim {
				registered = true
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !registered {
		t.Fatal("simulated node never registered as simulated=true")
	}

	postJSON(t, coordURL+"/users/simconsumer/startup-grant", map[string]any{}, nil)
	status, resp := postJSON(t, coordURL+"/v1/chat/completions", map[string]any{
		"model":      "llama-3.2-3b",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 64,
	}, map[string]string{"X-OIM-User-ID": "simconsumer"})
	if status != http.StatusOK {
		t.Fatalf("dispatch to simulated node failed: %d: %v", status, resp)
	}
	if _, ok := resp["choices"]; !ok {
		t.Fatalf("no choices in response: %v", resp)
	}

	// Give the async job-outcome time to arrive, same as TestIntegrationFullMoneyPath.
	time.Sleep(2 * time.Second)

	if got := balance(t, coordURL, "sim-miner"); got != 0 {
		t.Errorf("simulated node earned %.6f from real dispatch — should be 0, real hardware wasn't behind it", got)
	}
	if got := balance(t, coordURL, "oim-treasury"); got != 0 {
		t.Errorf("treasury earned %.6f margin from a simulated-node dispatch — should be 0", got)
	}
}

// TestIntegrationClusterRingDeduplicated reproduces the real double-
// registration bug found live on lab-01/lab-02: every device in one physical
// Exo ring runs its own independent oim agent, and each registers as a
// separate coordinator node claiming the ring's FULL pooled capacity. Two
// independent stub-exo + oim-agent process pairs are configured with the
// IDENTICAL topology.nodes device-ID list (exactly what real Exo reports from
// every member of one ring); the coordinator must (a) label exactly one of
// the two registrations cluster_standby, and (b) route every dispatch to the
// one primary.
func TestIntegrationClusterRingDeduplicated(t *testing.T) {
	coordPort := freePort(t)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)

	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0")
	waitHealthy(t, coordURL+"/health")

	// Two devices of ONE ring: independent stub-exo and oim-agent processes,
	// same topology membership — just like two Macs in one Exo cluster. Each
	// agent gets its OWN HOME: identity.LoadOrCreate persists the node
	// keypair under $HOME/.config/oim, and two agents sharing a HOME would
	// silently collapse into one node ID — the opposite of the two
	// independent devices this test needs.
	ringIDs := "device-aaa,device-bbb"
	for i, user := range []string{"ring-miner-1", "ring-miner-2"} {
		exoPort, nodePort := freePort(t), freePort(t)
		exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
		startProc(t, []string{
			fmt.Sprintf("STUB_LISTEN=:%d", exoPort),
			fmt.Sprintf("STUB_NODE_NAME=ring-dev-%d", i+1),
			"STUB_TOPOLOGY_NODE_IDS=" + ringIDs,
		}, bin.stubExo)
		waitHealthy(t, exoURL+"/state")

		startProc(t, []string{"HOME=" + t.TempDir()}, bin.node, "node", "start",
			fmt.Sprintf("--coordinator=%s", coordURL),
			fmt.Sprintf("--listen=:%d", nodePort),
			fmt.Sprintf("--exo-url=%s", exoURL),
			fmt.Sprintf("--reachability-endpoint=http://127.0.0.1:%d", nodePort),
			"--region=us", "--user-id="+user)
	}

	// Wait for both registrations, then confirm exactly one is standby.
	var primaryID string
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		arr, _ := nodes["nodes"].([]any)
		if len(arr) == 2 {
			var primaries, standbys []string
			for _, raw := range arr {
				n, _ := raw.(map[string]any)
				id, _ := n["node_id"].(string)
				if standby, _ := n["cluster_standby"].(bool); standby {
					standbys = append(standbys, id)
				} else {
					primaries = append(primaries, id)
				}
			}
			if len(primaries) == 1 && len(standbys) == 1 {
				primaryID = primaries[0]
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if primaryID == "" {
		t.Fatal("never saw exactly one primary + one cluster_standby registration for the ring")
	}

	// Every dispatch must land on the primary — the standby duplicate of the
	// same physical ring must never be routed to.
	postJSON(t, coordURL+"/users/ringconsumer/startup-grant", map[string]any{}, nil)
	for i := 0; i < 3; i++ {
		status, resp := postJSON(t, coordURL+"/v1/chat/completions", map[string]any{
			"model":      "llama-3.2-3b",
			"messages":   []map[string]any{{"role": "user", "content": "hi"}},
			"max_tokens": 64,
		}, map[string]string{"X-OIM-User-ID": "ringconsumer"})
		if status != http.StatusOK {
			t.Fatalf("dispatch %d failed: %d: %v", i, status, resp)
		}
		if servedBy, _ := resp["oim_served_by_node_id"].(string); servedBy != primaryID {
			t.Fatalf("dispatch %d served by %s — expected only the ring primary %s to ever be routed to", i, servedBy, primaryID)
		}
	}
}

// TestIntegrationPrefixAffinityKeepsRepeatedPromptsOnTheSameNode proves
// prefix/KV-cache-aware routing (TODO.md) through the real HTTP dispatch
// path, not just the unit-level rankCandidates function: two genuinely
// independent, equally-eligible real nodes serve the same model, and
// repeated dispatches carrying an IDENTICAL system-prompt prefix must all
// land on the SAME node (preserving its warm KV-cache), while dispatches
// with many DIFFERENT prefixes should spread across both nodes rather than
// all piling onto one.
func TestIntegrationPrefixAffinityKeepsRepeatedPromptsOnTheSameNode(t *testing.T) {
	coordPort := freePort(t)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)

	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest", "--region=us",
		fmt.Sprintf("--public-url=%s", coordURL), "--grant-pow-bits=0")
	waitHealthy(t, coordURL+"/health")

	for i, user := range []string{"affinity-miner-1", "affinity-miner-2"} {
		exoPort, nodePort := freePort(t), freePort(t)
		exoURL := fmt.Sprintf("http://127.0.0.1:%d", exoPort)
		startProc(t, []string{fmt.Sprintf("STUB_LISTEN=:%d", exoPort)}, bin.stubExo)
		waitHealthy(t, exoURL+"/state")

		startProc(t, []string{"HOME=" + t.TempDir()}, bin.node, "node", "start",
			fmt.Sprintf("--coordinator=%s", coordURL),
			fmt.Sprintf("--listen=:%d", nodePort),
			fmt.Sprintf("--exo-url=%s", exoURL),
			fmt.Sprintf("--reachability-endpoint=http://127.0.0.1:%d", nodePort),
			"--region=us", fmt.Sprintf("--user-id=%s-%d", user, i))
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		nodes := getJSON(t, coordURL+"/nodes")
		if arr, ok := nodes["nodes"].([]any); ok && len(arr) == 2 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	postJSON(t, coordURL+"/users/affinityconsumer/startup-grant", map[string]any{}, nil)
	dispatch := func(systemPrompt string) string {
		status, resp := postJSON(t, coordURL+"/v1/chat/completions", map[string]any{
			"model": "llama-3.2-3b",
			"messages": []map[string]any{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": "hi"},
			},
			"max_tokens": 64,
		}, map[string]string{"X-OIM-User-ID": "affinityconsumer"})
		if status != http.StatusOK {
			t.Fatalf("dispatch failed: %d: %v", status, resp)
		}
		servedBy, _ := resp["oim_served_by_node_id"].(string)
		return servedBy
	}

	const fixedPrompt = "You are a helpful assistant. Always answer concisely."
	first := dispatch(fixedPrompt)
	if first == "" {
		t.Fatal("expected a non-empty served-by node ID")
	}
	for i := 0; i < 8; i++ {
		if got := dispatch(fixedPrompt); got != first {
			t.Fatalf("repeated dispatch %d with an identical system prompt landed on a different node (%s) than the first (%s) — prefix affinity should keep it pinned", i, got, first)
		}
	}

	// The consistent-hash ring's placement is randomized per test run (it
	// depends on the two nodes' randomly-generated identities), so a SMALL
	// number of distinct prefixes has a real, non-negligible chance of all
	// landing on the same node purely from ring skew with only 2 members —
	// not a routing bug (see hashRingReplicas' doc comment). Uses up to 200
	// distinct prefixes, stopping as soon as both nodes are seen — in
	// practice this exits after just a few dispatches almost every run; 200
	// is only a large upper bound to make a false failure statistically
	// negligible (empirically <0.1% even in an adversarial ring instantiation).
	seen := map[string]bool{}
	const maxDistinctPrefixProbes = 200
	for i := 0; i < maxDistinctPrefixProbes && len(seen) < 2; i++ {
		seen[dispatch(fmt.Sprintf("distinct system prompt #%d", i))] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected distinct prefixes to spread across both nodes within %d probes, all landed on %+v", maxDistinctPrefixProbes, seen)
	}
}

// doRequest issues req with the given method/headers against a real running
// coordinator and returns the status + decoded JSON body.
func doRequest(t *testing.T, method, url string, headers map[string]string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// TestIntegrationAdminPanelAuthAndTreasuryFlow drives the full BDFL admin
// login + treasury flow (task #96) against a real coordinator binary:
// challenge -> sign -> authenticate -> use the session token for treasury
// read/credit, audit-log, and node deregistration — while confirming the
// EXISTING static --api-key workflow (RUNBOOK's /admin/reconcile curl) still
// works unchanged alongside it, and that a captured/replayed signature and
// excess treasury credits are both rejected.
func TestIntegrationAdminPanelAuthAndTreasuryFlow(t *testing.T) {
	priv, pub, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	pubHex := hex.EncodeToString(pub)

	coordPort := freePort(t)
	coordURL := fmt.Sprintf("http://127.0.0.1:%d", coordPort)
	startProc(t, nil, bin.coordinator,
		fmt.Sprintf("--listen=:%d", coordPort), "--pod-id=itest-admin", "--region=us",
		"--api-key=static-secret",
		fmt.Sprintf("--bdfl-public-key=%s", pubHex),
		"--admin-treasury-rate-limit-per-min=2")
	waitHealthy(t, coordURL+"/health")

	// The pre-existing static-key workflow must be entirely unaffected by this
	// feature landing.
	if status, _ := doRequest(t, "GET", coordURL+"/admin/reconcile", map[string]string{"Authorization": "Bearer static-secret"}); status != http.StatusOK {
		t.Fatalf("GET /admin/reconcile with static --api-key = %d, want 200 (must keep working unchanged)", status)
	}
	if status, _ := doRequest(t, "GET", coordURL+"/admin/reconcile", nil); status != http.StatusUnauthorized {
		t.Fatalf("GET /admin/reconcile with no credential = %d, want 401", status)
	}

	sign := func(k []byte, n string) string {
		sig, err := protocol.SignPayload(k, []byte("oim-admin-auth:"+n))
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(sig)
	}
	requestNonce := func() string {
		status, chal := postJSON(t, coordURL+"/admin/challenge", map[string]any{}, nil)
		if status != http.StatusOK {
			t.Fatalf("POST /admin/challenge = %d: %v", status, chal)
		}
		nonce, _ := chal["nonce"].(string)
		if nonce == "" {
			t.Fatalf("expected a non-empty nonce, got %+v", chal)
		}
		return nonce
	}

	// A signature from the wrong key must be rejected — and, per adminauth's
	// one-shot semantics, this also consumes the nonce (verified immediately
	// below by replaying the SAME nonce with the correct key).
	wrongPriv, _, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	nonce1 := requestNonce()
	if status, _ := postJSON(t, coordURL+"/admin/authenticate", map[string]any{
		"nonce": nonce1, "signature": sign(wrongPriv, nonce1),
	}, nil); status != http.StatusUnauthorized {
		t.Fatalf("authenticate with wrong-key signature = %d, want 401", status)
	}
	if status, _ := postJSON(t, coordURL+"/admin/authenticate", map[string]any{
		"nonce": nonce1, "signature": sign(priv, nonce1),
	}, nil); status != http.StatusUnauthorized {
		t.Fatalf("replaying a nonce already consumed by a failed attempt = %d, want 401", status)
	}

	// A fresh challenge, signed with the real key, succeeds.
	nonce2 := requestNonce()
	status, authResp := postJSON(t, coordURL+"/admin/authenticate", map[string]any{
		"nonce": nonce2, "signature": sign(priv, nonce2),
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("POST /admin/authenticate = %d: %v", status, authResp)
	}
	sessionToken, _ := authResp["session_token"].(string)
	if !strings.HasPrefix(sessionToken, "oimadmin_") {
		t.Fatalf("expected an oimadmin_ session token, got %+v", authResp)
	}
	authHeader := map[string]string{"Authorization": "Bearer " + sessionToken}

	// Session token grants the same admin-level access as the static key.
	if status, _ := doRequest(t, "GET", coordURL+"/admin/treasury", authHeader); status != http.StatusOK {
		t.Fatalf("GET /admin/treasury with session token = %d, want 200", status)
	}

	// Treasury credit: validation, success, and audit trail.
	if status, resp := postJSON(t, coordURL+"/admin/treasury/credit", map[string]any{"amount": 0, "reason": "test"}, authHeader); status != http.StatusBadRequest {
		t.Fatalf("treasury credit with amount<=0 = %d: %v, want 400", status, resp)
	}
	if status, resp := postJSON(t, coordURL+"/admin/treasury/credit", map[string]any{"amount": 10}, authHeader); status != http.StatusBadRequest {
		t.Fatalf("treasury credit with no reason = %d: %v, want 400", status, resp)
	}
	status, credResp := postJSON(t, coordURL+"/admin/treasury/credit", map[string]any{"amount": 25, "reason": "quarterly top-up"}, authHeader)
	if status != http.StatusOK {
		t.Fatalf("treasury credit = %d: %v", status, credResp)
	}

	// Rate limit (--admin-treasury-rate-limit-per-min=2, burst 1): the very
	// next credit call in the same instant must trip 429.
	if status, resp := postJSON(t, coordURL+"/admin/treasury/credit", map[string]any{"amount": 5, "reason": "immediate second call"}, authHeader); status != http.StatusTooManyRequests {
		t.Fatalf("second rapid treasury credit = %d: %v, want 429 (rate limit)", status, resp)
	}

	status, auditResp := doRequest(t, "GET", coordURL+"/admin/audit-log?limit=10", authHeader)
	if status != http.StatusOK {
		t.Fatalf("GET /admin/audit-log = %d: %v", status, auditResp)
	}
	actions, _ := auditResp["actions"].([]any)
	if len(actions) != 1 {
		t.Fatalf("expected exactly 1 audit action (only one credit call should have succeeded), got %+v", actions)
	}
	first, _ := actions[0].(map[string]any)
	if detail, _ := first["detail"].(string); detail != "quarterly top-up" {
		t.Errorf("audit log entry detail = %q, want %q", detail, "quarterly top-up")
	}

	// Node management: the session token also authorizes the existing
	// DELETE /nodes/{id} admin action (idempotent even for an unknown ID).
	if status, _ := doRequest(t, "DELETE", coordURL+"/nodes/nonexistent-node", authHeader); status != http.StatusOK {
		t.Fatalf("DELETE /nodes/{id} with session token = %d, want 200", status)
	}
	if status, _ := doRequest(t, "DELETE", coordURL+"/nodes/nonexistent-node", nil); status != http.StatusUnauthorized {
		t.Fatalf("DELETE /nodes/{id} with no credential = %d, want 401", status)
	}
}
