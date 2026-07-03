package tests

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/open-inference-mesh/oim/internal/coordinator"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/settlement"
)

// --- Division order tests ---

func TestComputeShrinkage(t *testing.T) {
	shrinkage := settlement.ComputeShrinkage(100.0, 85.0)
	if shrinkage != 15.0 {
		t.Errorf("ComputeShrinkage(100, 85): want 15.0, got %.1f", shrinkage)
	}
}

func TestBuildDivisionOrder(t *testing.T) {
	lines := []settlement.ResourceLine{
		{ResourceType: "memory_hours", MeasuredAmount: 2.0, DeliveredAmount: 1.8, UnitPrice: 0.50},
		{ResourceType: "compute_cycles", MeasuredAmount: 500.0, DeliveredAmount: 480.0, UnitPrice: 0.001},
		{ResourceType: "bandwidth_relayed", MeasuredAmount: 10.0, DeliveredAmount: 9.5, UnitPrice: 0.10},
	}
	order, err := settlement.BuildDivisionOrder("job-1", "node-abc", lines)
	if err != nil {
		t.Fatalf("BuildDivisionOrder: %v", err)
	}

	if order["job_id"] != "job-1" {
		t.Errorf("job_id: want job-1, got %v", order["job_id"])
	}
	// total_delivered = 1.8 + 480 + 9.5 = 491.3
	totalDel, _ := order["total_delivered"].(float64)
	if totalDel != 491.3 {
		t.Errorf("total_delivered: want 491.3, got %.4f", totalDel)
	}
	// total_value = 1.8*0.50 + 480*0.001 + 9.5*0.10 = 0.9 + 0.48 + 0.95 = 2.33
	totalVal, _ := order["total_value"].(float64)
	wantVal := 1.8*0.50 + 480.0*0.001 + 9.5*0.10
	if totalVal < wantVal-0.0001 || totalVal > wantVal+0.0001 {
		t.Errorf("total_value: want %.4f, got %.4f", wantVal, totalVal)
	}
	// total_shrinkage = total_measured - total_delivered
	totalMeas, _ := order["total_measured"].(float64)
	wantShrink := totalMeas - totalDel
	shrink, _ := order["total_shrinkage"].(float64)
	if shrink < wantShrink-0.0001 || shrink > wantShrink+0.0001 {
		t.Errorf("total_shrinkage: want %.4f, got %.4f", wantShrink, shrink)
	}
}

func TestBuildDivisionOrderRejectsEmpty(t *testing.T) {
	_, err := settlement.BuildDivisionOrder("", "node-x", nil)
	if err == nil {
		t.Error("BuildDivisionOrder with empty job_id should return error")
	}
	_, err = settlement.BuildDivisionOrder("job-2", "", nil)
	if err == nil {
		t.Error("BuildDivisionOrder with empty node_id should return error")
	}
}

func TestReconcileClampsFraudulentClaim(t *testing.T) {
	// Node measured at 50 TPS, but claims 1000 — 20× fraud.
	sig := &protocol.MeasuredSignature{TokensPerSecDecode: 50.0}
	reconciled := settlement.ReconcileAgainstMeasuredSignature(1000.0, sig, 0.20)
	want := 50.0 * 1.20 // 60.0
	if reconciled < want-0.001 || reconciled > want+0.001 {
		t.Errorf("want %.2f (capped at measured*(1+tol)), got %.2f", want, reconciled)
	}
}

func TestReconcileAcceptsHonestClaim(t *testing.T) {
	// Node measured at 50 TPS, claims 45 — within 20% tolerance.
	sig := &protocol.MeasuredSignature{TokensPerSecDecode: 50.0}
	reconciled := settlement.ReconcileAgainstMeasuredSignature(45.0, sig, 0.20)
	if reconciled != 45.0 {
		t.Errorf("honest claim should be unchanged; want 45.0, got %.2f", reconciled)
	}
}

func TestReconcileNilSignature(t *testing.T) {
	// No measurement signature — accept as-is (cannot reconcile without reference).
	reconciled := settlement.ReconcileAgainstMeasuredSignature(999.0, nil, 0.20)
	if reconciled != 999.0 {
		t.Errorf("nil sig: want claimed amount unchanged (999.0), got %.2f", reconciled)
	}
}

// --- Settlement record tests ---

func TestCreateSettlementRecord(t *testing.T) {
	priv, _, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	order := map[string]any{"job_id": "job-1", "node_id": "node-x", "total_value": 2.33}
	record, err := settlement.CreateSettlementRecord(order, true, priv)
	if err != nil {
		t.Fatalf("CreateSettlementRecord: %v", err)
	}

	if record["record_id"] == "" {
		t.Error("record_id should be set")
	}
	sig, _ := record["signature"].(string)
	if len(sig) == 0 {
		t.Error("signature should be non-empty")
	}
	if record["verification_result"] != true {
		t.Errorf("verification_result: want true, got %v", record["verification_result"])
	}
	if _, ok := record["signed_at"]; !ok {
		t.Error("signed_at should be present")
	}
}

func TestSettlementRecordVerificationFalseKept(t *testing.T) {
	// A failed-verification record must still be created — it's evidence, not noise (proposal §10).
	priv, _, err := protocol.GenerateNodeIdentity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	order := map[string]any{"job_id": "job-2", "node_id": "node-y"}
	record, err := settlement.CreateSettlementRecord(order, false, priv)
	if err != nil {
		t.Fatalf("CreateSettlementRecord with false verification: %v", err)
	}
	if record["verification_result"] != false {
		t.Errorf("verification_result should be false; got %v", record["verification_result"])
	}
	if record["signature"] == "" {
		t.Error("even a failed-verification record must be signed")
	}
}

// --- Payment pointer tests ---

func TestValidatePaymentPointerStablecoin(t *testing.T) {
	p := settlement.PaymentPointer{RailType: "stablecoin", AddressOrReference: "0xAbCdEf1234567890"}
	ok, err := settlement.ValidatePaymentPointer(p)
	if err != nil || !ok {
		t.Errorf("valid stablecoin pointer: want (true, nil), got (%v, %v)", ok, err)
	}
}

func TestValidatePaymentPointerFiatInvoice(t *testing.T) {
	p := settlement.PaymentPointer{RailType: "fiat_invoice", AddressOrReference: "INV-20260629-001"}
	ok, err := settlement.ValidatePaymentPointer(p)
	if err != nil || !ok {
		t.Errorf("valid fiat_invoice pointer: want (true, nil), got (%v, %v)", ok, err)
	}
}

func TestValidatePaymentPointerOther(t *testing.T) {
	p := settlement.PaymentPointer{RailType: "other", AddressOrReference: "lightning:lnbc1abc"}
	ok, err := settlement.ValidatePaymentPointer(p)
	if err != nil || !ok {
		t.Errorf("valid other pointer: want (true, nil), got (%v, %v)", ok, err)
	}
}

func TestValidatePaymentPointerUnknownRail(t *testing.T) {
	p := settlement.PaymentPointer{RailType: "crypto_token", AddressOrReference: "some-address"}
	ok, err := settlement.ValidatePaymentPointer(p)
	if err == nil {
		t.Error("unknown rail_type should return error")
	}
	if ok {
		t.Error("unknown rail_type should return false")
	}
}

func TestValidatePaymentPointerEmptyAddress(t *testing.T) {
	p := settlement.PaymentPointer{RailType: "stablecoin", AddressOrReference: ""}
	ok, err := settlement.ValidatePaymentPointer(p)
	if err == nil {
		t.Error("empty address_or_reference should return error")
	}
	if ok {
		t.Error("empty address_or_reference should return false")
	}
}

// --- Credit ledger tests ---

func TestCreditAndGetBalance(t *testing.T) {
	l := settlement.NewLedger()
	_ = l.CreditAccount(settlement.CreditEntry{
		UserID: "user-1", Origin: settlement.CreditOriginStartupGrant,
		Amount: 50.0, GrantedOrEarnedAt: time.Now(),
	})
	bal := l.GetBalance("user-1")
	if bal.GrantBalance != 50.0 {
		t.Errorf("grant_balance: want 50.0, got %.2f", bal.GrantBalance)
	}
	if bal.EarnedBalance != 0.0 {
		t.Errorf("earned_balance: want 0.0, got %.2f", bal.EarnedBalance)
	}
	if bal.Total != 50.0 {
		t.Errorf("total: want 50.0, got %.2f", bal.Total)
	}
}

func TestDebitGrantFirst(t *testing.T) {
	// 50 grant + 50 earned; debit 60 → grant fully consumed, earned reduced by 10.
	l := settlement.NewLedger()
	_ = l.CreditAccount(settlement.CreditEntry{UserID: "user-2", Origin: settlement.CreditOriginStartupGrant, Amount: 50.0, GrantedOrEarnedAt: time.Now()})
	_ = l.CreditAccount(settlement.CreditEntry{UserID: "user-2", Origin: settlement.CreditOriginEarnedContrib, Amount: 50.0, GrantedOrEarnedAt: time.Now()})

	ok := l.DebitAccount("user-2", 60.0, "job-debit-1")
	if !ok {
		t.Fatal("DebitAccount should succeed with sufficient total balance")
	}
	bal := l.GetBalance("user-2")
	if bal.GrantBalance != 0.0 {
		t.Errorf("grant_balance: want 0.0 (fully consumed), got %.2f", bal.GrantBalance)
	}
	if bal.EarnedBalance != 40.0 {
		t.Errorf("earned_balance: want 40.0 (50 - 10 overflow), got %.2f", bal.EarnedBalance)
	}
	if bal.Total != 40.0 {
		t.Errorf("total: want 40.0, got %.2f", bal.Total)
	}
}

func TestDebitInsufficientBalance(t *testing.T) {
	l := settlement.NewLedger()
	_ = l.CreditAccount(settlement.CreditEntry{UserID: "user-3", Origin: settlement.CreditOriginStartupGrant, Amount: 30.0, GrantedOrEarnedAt: time.Now()})

	ok := l.DebitAccount("user-3", 50.0, "job-insufficient")
	if ok {
		t.Error("DebitAccount should return false when amount > total balance")
	}
	// Balance must be unchanged after a failed debit.
	bal := l.GetBalance("user-3")
	if bal.Total != 30.0 {
		t.Errorf("balance should be unchanged after failed debit; got %.2f", bal.Total)
	}
}

func TestTotalOutstandingGrantLiability(t *testing.T) {
	l := settlement.NewLedger()
	_ = l.CreditAccount(settlement.CreditEntry{UserID: "user-4", Origin: settlement.CreditOriginStartupGrant, Amount: 50.0, GrantedOrEarnedAt: time.Now()})
	_ = l.CreditAccount(settlement.CreditEntry{UserID: "user-4", Origin: settlement.CreditOriginEarnedContrib, Amount: 20.0, GrantedOrEarnedAt: time.Now()})
	l.DebitAccount("user-4", 20.0, "job-partial") // consumes 20 of the 50 grant

	liability := l.TotalOutstandingGrantLiability()
	if liability != 30.0 {
		t.Errorf("grant liability: want 30.0 (50 grant - 20 debited), got %.2f", liability)
	}
}

// --- Grant decay tests ---

func TestCurrentGrantMultiplierSteps(t *testing.T) {
	steps := settlement.DEFAULT_DECAY_STEPS
	cases := []struct {
		score float64
		want  float64
	}{
		{0.0, 1.0},
		{499.0, 1.0},
		{500.0, 0.5},
		{1999.0, 0.5},
		{2000.0, 0.0},
		{9999.0, 0.0},
	}
	for _, tc := range cases {
		got := settlement.CurrentGrantMultiplier(tc.score, steps)
		if got != tc.want {
			t.Errorf("score=%.0f: want multiplier %.1f, got %.1f", tc.score, tc.want, got)
		}
	}
}

// stubCapacitySource lets tests control the verified capacity score.
type stubCapacitySource struct{ score float64 }

func (s *stubCapacitySource) VerifiedCapacityForPod(_ string) float64 { return s.score }

func TestIssueStartupGrantScoreBasedAmount(t *testing.T) {
	// Verified capacity score of 600 → multiplier 0.5 → grant = 50.
	l := settlement.NewLedger()
	src := &stubCapacitySource{score: 600.0}
	entry, err := settlement.IssueStartupGrant(l, "user-5", "pod-us", src, settlement.DEFAULT_DECAY_STEPS)
	if err != nil {
		t.Fatalf("IssueStartupGrant: %v", err)
	}
	want := settlement.BASE_GRANT_AMOUNT * 0.5 // 50.0
	if entry.Amount != want {
		t.Errorf("grant amount: want %.2f, got %.2f", want, entry.Amount)
	}
	if entry.Origin != settlement.CreditOriginStartupGrant {
		t.Errorf("origin: want startup_grant, got %s", entry.Origin)
	}
	// Ledger should reflect the credit.
	bal := l.GetBalance("user-5")
	if bal.GrantBalance != want {
		t.Errorf("ledger grant_balance: want %.2f, got %.2f", want, bal.GrantBalance)
	}
}

func TestIssueStartupGrantRejectsSecondClaim(t *testing.T) {
	// A second claim for the same user must not credit again — dedup is
	// checked against the ledger itself, not a separate in-memory set that
	// would reset (and become farmable) on every coordinator restart.
	l := settlement.NewLedger()
	src := &stubCapacitySource{score: 0.0} // multiplier 1.0 → full grant
	if _, err := settlement.IssueStartupGrant(l, "user-dup", "pod-us", src, settlement.DEFAULT_DECAY_STEPS); err != nil {
		t.Fatalf("first IssueStartupGrant: %v", err)
	}
	_, err := settlement.IssueStartupGrant(l, "user-dup", "pod-us", src, settlement.DEFAULT_DECAY_STEPS)
	if !errors.Is(err, settlement.ErrStartupGrantAlreadyClaimed) {
		t.Fatalf("second claim: want ErrStartupGrantAlreadyClaimed, got %v", err)
	}
	bal := l.GetBalance("user-dup")
	if bal.GrantBalance != settlement.BASE_GRANT_AMOUNT {
		t.Errorf("grant_balance after duplicate claim attempt: want %.2f (unchanged), got %.2f", settlement.BASE_GRANT_AMOUNT, bal.GrantBalance)
	}
}

func TestIssueStartupGrantDedupSurvivesRestart(t *testing.T) {
	// Same check as above, but through a persistent ledger reopened to simulate
	// a real coordinator restart — this is the exact scenario that used to let
	// a user double their grant by bouncing the process.
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	src := &stubCapacitySource{score: 0.0}

	l1, err := settlement.NewPersistentLedger(dbPath)
	if err != nil {
		t.Fatalf("NewPersistentLedger: %v", err)
	}
	if _, err := settlement.IssueStartupGrant(l1, "user-restart", "pod-us", src, settlement.DEFAULT_DECAY_STEPS); err != nil {
		t.Fatalf("first IssueStartupGrant: %v", err)
	}

	l2, err := settlement.NewPersistentLedger(dbPath) // simulated restart
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	_, err = settlement.IssueStartupGrant(l2, "user-restart", "pod-us", src, settlement.DEFAULT_DECAY_STEPS)
	if !errors.Is(err, settlement.ErrStartupGrantAlreadyClaimed) {
		t.Fatalf("claim after restart: want ErrStartupGrantAlreadyClaimed, got %v", err)
	}
	bal := l2.GetBalance("user-restart")
	if bal.GrantBalance != settlement.BASE_GRANT_AMOUNT {
		t.Errorf("grant_balance after restart+reclaim: want %.2f (unchanged), got %.2f", settlement.BASE_GRANT_AMOUNT, bal.GrantBalance)
	}
}

func TestGrantMultiplierStepsDownWithCapacity(t *testing.T) {
	// Increasing simulated capacity drives the grant multiplier down (proposal §9.4).
	l := settlement.NewLedger()
	steps := settlement.DEFAULT_DECAY_STEPS
	cases := []struct {
		userID    string
		score     float64
		wantGrant float64
	}{
		{"user-a", 0.0, settlement.BASE_GRANT_AMOUNT * 1.0},  // 100.0
		{"user-b", 600.0, settlement.BASE_GRANT_AMOUNT * 0.5}, // 50.0
		{"user-c", 3000.0, settlement.BASE_GRANT_AMOUNT * 0.0}, // 0.0
	}
	for _, tc := range cases {
		src := &stubCapacitySource{score: tc.score}
		entry, err := settlement.IssueStartupGrant(l, tc.userID, "pod-us", src, steps)
		if err != nil {
			t.Fatalf("IssueStartupGrant user=%s: %v", tc.userID, err)
		}
		if entry.Amount != tc.wantGrant {
			t.Errorf("user=%s score=%.0f: want grant %.2f, got %.2f",
				tc.userID, tc.score, tc.wantGrant, entry.Amount)
		}
	}
}

// --- VerifiedCapacityScore (coordinator registry) ---

func TestVerifiedCapacityScoreExcludesUnverified(t *testing.T) {
	// A node with no submitted benchmark contributes zero to the score.
	r := coordinator.NewNodeRegistry()
	measurements := coordinator.NewMeasurementStore()

	reg := makeTestNode(t, "llama-3.2-3b", "4bit", 80.0, false)
	ok, err := r.Register(reg)
	if err != nil || !ok {
		t.Fatalf("register: ok=%v err=%v", ok, err)
	}
	// No measurement submitted → score must be zero.
	score := r.VerifiedCapacityScore(measurements, 0.20)
	if score != 0.0 {
		t.Errorf("node with no submitted benchmark should score 0; got %.2f", score)
	}
}

func TestVerifiedCapacityScoreCountsVerified(t *testing.T) {
	// Two nodes each submit benchmarks that match their claimed signatures → both count.
	r := coordinator.NewNodeRegistry()
	measurements := coordinator.NewMeasurementStore()

	for _, tps := range []float64{80.0, 50.0} {
		reg := makeTestNode(t, "llama-3.2-3b", "4bit", tps, false)
		ok, err := r.Register(reg)
		if err != nil || !ok {
			t.Fatalf("register %.0f TPS node: ok=%v err=%v", tps, ok, err)
		}
		// Submit a measurement that matches the claimed signature (within 20%).
		measurements.Store(reg.Manifest.NodeID, &protocol.MeasuredSignature{
			TokensPerSecDecode:  tps * 0.98, // 2% below claimed — well within 20% tolerance
			TokensPerSecPrefill: tps * 4,
			BenchmarkPromptID:   "medium",
			SampleCount:         3,
			MeasuredAt:          time.Now().UTC().Format(time.RFC3339),
		})
	}

	score := r.VerifiedCapacityScore(measurements, 0.20)
	// score = 80*0.98 + 50*0.98 = 78.4 + 49.0 = 127.4
	want := 80.0*0.98 + 50.0*0.98
	if score < want-0.001 || score > want+0.001 {
		t.Errorf("verified capacity score: want %.4f, got %.4f", want, score)
	}
}

// --- Ledger persistence tests ---

func TestPersistentLedgerSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")

	l1, err := settlement.NewPersistentLedger(dbPath)
	if err != nil {
		t.Fatalf("NewPersistentLedger: %v", err)
	}
	if err := l1.CreditAccount(settlement.CreditEntry{
		UserID: "alice", Origin: settlement.CreditOriginStartupGrant,
		Amount: 100.0, GrantedOrEarnedAt: time.Now(), SourceReference: "test-grant",
	}); err != nil {
		t.Fatalf("CreditAccount: %v", err)
	}
	if ok := l1.DebitAccount("alice", 30.0, "job-1"); !ok {
		t.Fatal("DebitAccount: expected success")
	}
	before := l1.GetBalance("alice")

	// Simulate a coordinator restart: reopen the same DB file as a fresh Ledger.
	l2, err := settlement.NewPersistentLedger(dbPath)
	if err != nil {
		t.Fatalf("reopen NewPersistentLedger: %v", err)
	}
	after := l2.GetBalance("alice")

	if before.Total != 70.0 {
		t.Errorf("pre-restart total: want 70.0, got %.2f", before.Total)
	}
	if after != before {
		t.Errorf("balance did not survive restart: before=%+v after=%+v", before, after)
	}
}

func TestPersistentLedgerRejectsOverspendAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")

	l1, err := settlement.NewPersistentLedger(dbPath)
	if err != nil {
		t.Fatalf("NewPersistentLedger: %v", err)
	}
	if err := l1.CreditAccount(settlement.CreditEntry{
		UserID: "bob", Origin: settlement.CreditOriginEarnedContrib,
		Amount: 10.0, GrantedOrEarnedAt: time.Now(), SourceReference: "test-earn",
	}); err != nil {
		t.Fatalf("CreditAccount: %v", err)
	}

	l2, err := settlement.NewPersistentLedger(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	// Balance persisted, so this must still be rejected as insufficient — not
	// silently allowed because the in-memory view was reset.
	if ok := l2.DebitAccount("bob", 50.0, "job-overspend"); ok {
		t.Error("DebitAccount: expected rejection for amount exceeding restored balance")
	}
}

func TestNewPersistentLedgerCreatesFileAndDir(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ledger.db")
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("precondition: db file should not exist yet")
	}
	l, err := settlement.NewPersistentLedger(dbPath)
	if err != nil {
		t.Fatalf("NewPersistentLedger: %v", err)
	}
	if err := l.CreditAccount(settlement.CreditEntry{
		UserID: "carol", Origin: settlement.CreditOriginEarnedContrib,
		Amount: 5.0, GrantedOrEarnedAt: time.Now(), SourceReference: "x",
	}); err != nil {
		t.Fatalf("CreditAccount: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("expected db file to exist after write: %v", err)
	}
}
