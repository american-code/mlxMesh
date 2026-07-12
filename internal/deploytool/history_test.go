package deploytool

import (
	"path/filepath"
	"testing"
	"time"
)

func boolPtr(b bool) *bool { return &b }

func TestLoadHistory_MissingFileReturnsEmpty(t *testing.T) {
	h, err := LoadHistory(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("missing history file should not error, got %v", err)
	}
	if len(h.Records) != 0 {
		t.Errorf("expected empty history, got %d records", len(h.Records))
	}
}

func TestHistory_SaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	var h History
	h.Append(Record{
		Timestamp: time.Now().UTC(), Action: "deploy", GitCommit: "abc123",
		ImageTag: "mlxmesh-abc123-20260101-000000", Component: "all",
		DeployedBy: "alice@laptop", HealthyAfter: boolPtr(true),
	})
	if err := h.Save(path); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Records) != 1 {
		t.Fatalf("expected 1 record after reload, got %d", len(reloaded.Records))
	}
	if reloaded.Records[0].ImageTag != "mlxmesh-abc123-20260101-000000" {
		t.Errorf("image tag did not round-trip: %q", reloaded.Records[0].ImageTag)
	}
	if reloaded.Records[0].HealthyAfter == nil || !*reloaded.Records[0].HealthyAfter {
		t.Error("HealthyAfter=true did not round-trip")
	}
}

func TestHistory_Latest(t *testing.T) {
	var h History
	if _, ok := h.Latest(); ok {
		t.Error("empty history should report no latest record")
	}
	h.Append(Record{ImageTag: "tag-1"})
	h.Append(Record{ImageTag: "tag-2"})
	latest, ok := h.Latest()
	if !ok || latest.ImageTag != "tag-2" {
		t.Errorf("Latest() = %+v, %v; want tag-2, true", latest, ok)
	}
}

// SelectRollbackTarget is the regression test for the actual feature
// TODO.md's "no rollback mechanism" gap names: rollback must go to the most
// recent PREVIOUSLY-HEALTHY deploy, not just "whatever's one entry back" —
// skipping over a broken current head and any deploy that never itself
// passed health-check.
func TestSelectRollbackTarget_SkipsCurrentHeadAndUnhealthyEntries(t *testing.T) {
	var h History
	h.Append(Record{Action: "deploy", ImageTag: "v1", HealthyAfter: boolPtr(true)})
	h.Append(Record{Action: "deploy", ImageTag: "v2", HealthyAfter: boolPtr(false)}) // bad deploy
	h.Append(Record{Action: "deploy", ImageTag: "v3", HealthyAfter: boolPtr(true)})  // current head

	target, ok := SelectRollbackTarget(h)
	if !ok {
		t.Fatal("expected a rollback target to be found")
	}
	if target.ImageTag != "v1" {
		t.Errorf("rollback target = %q, want v1 (v2 was unhealthy, v3 is the current head)", target.ImageTag)
	}
}

func TestSelectRollbackTarget_NoHealthyPriorDeployReturnsFalse(t *testing.T) {
	var h History
	h.Append(Record{Action: "deploy", ImageTag: "v1", HealthyAfter: boolPtr(false)})
	h.Append(Record{Action: "deploy", ImageTag: "v2", HealthyAfter: nil}) // never checked
	h.Append(Record{Action: "deploy", ImageTag: "v3", HealthyAfter: boolPtr(true)}) // current head

	if _, ok := SelectRollbackTarget(h); ok {
		t.Error("expected no rollback target: no PRIOR deploy (excluding the head) ever passed health-check")
	}
}

func TestSelectRollbackTarget_EmptyHistoryReturnsFalse(t *testing.T) {
	if _, ok := SelectRollbackTarget(History{}); ok {
		t.Error("empty history must not produce a rollback target")
	}
}

func TestSelectRollbackTarget_SingleRecordReturnsFalse(t *testing.T) {
	var h History
	h.Append(Record{Action: "deploy", ImageTag: "v1", HealthyAfter: boolPtr(true)})
	if _, ok := SelectRollbackTarget(h); ok {
		t.Error("a single record is the current head with nothing before it — no rollback target should exist")
	}
}

// A rollback record itself must be skippable-over too: rolling back twice in
// a row should still land on a genuinely healthy DEPLOY, not accidentally
// treat a rollback record as a valid target (rollback records don't carry
// HealthyAfter/Action=="deploy" semantics the same way).
func TestSelectRollbackTarget_IgnoresRollbackRecordsAsTargets(t *testing.T) {
	var h History
	h.Append(Record{Action: "deploy", ImageTag: "v1", HealthyAfter: boolPtr(true)})
	h.Append(Record{Action: "deploy", ImageTag: "v2", HealthyAfter: boolPtr(true)})
	h.Append(Record{Action: "rollback", ImageTag: "v1"}) // rolled back to v1; no HealthyAfter set on the rollback record itself
	h.Append(Record{Action: "deploy", ImageTag: "v3", HealthyAfter: boolPtr(false)}) // current head, unhealthy

	target, ok := SelectRollbackTarget(h)
	if !ok {
		t.Fatal("expected a rollback target")
	}
	if target.ImageTag != "v2" {
		t.Errorf("rollback target = %q, want v2 (the rollback record and v3 must both be skipped)", target.ImageTag)
	}
}
