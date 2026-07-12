package deploytool

import "testing"

func TestGoldenSignalReport_AllHealthy(t *testing.T) {
	healthy := GoldenSignalReport{Checks: []GoldenSignalCheck{{Name: "a", OK: true}, {Name: "b", OK: true}}}
	if !healthy.AllHealthy() {
		t.Error("all-OK report should be healthy")
	}
	unhealthy := GoldenSignalReport{Checks: []GoldenSignalCheck{{Name: "a", OK: true}, {Name: "b", OK: false}}}
	if unhealthy.AllHealthy() {
		t.Error("one failing check should make the whole report unhealthy")
	}
}

// An empty report must NOT read as healthy — that would let a wiring bug in
// the checker itself (e.g. no endpoints configured) silently rubber-stamp
// every deploy as green.
func TestGoldenSignalReport_EmptyIsNotHealthy(t *testing.T) {
	if (GoldenSignalReport{}).AllHealthy() {
		t.Error("a report with zero checks must not be considered healthy")
	}
}

func TestEvaluateEndpointChecks(t *testing.T) {
	checks := EvaluateEndpointChecks(map[string]int{
		"https://us.mlxmesh.net/health": 200,
		"https://eu.mlxmesh.net/health": 503,
	})
	byName := map[string]GoldenSignalCheck{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	if !byName["https://us.mlxmesh.net/health"].OK {
		t.Error("200 must be OK")
	}
	if byName["https://eu.mlxmesh.net/health"].OK {
		t.Error("503 must not be OK")
	}
}

func TestEvaluateContainerCount(t *testing.T) {
	if c := EvaluateContainerCount(119, 119); !c.OK {
		t.Errorf("matching count should be OK, got %+v", c)
	}
	if c := EvaluateContainerCount(80, 119); c.OK {
		t.Errorf("mismatched count should not be OK, got %+v", c)
	}
}

func TestEvaluateLedgerConsistency(t *testing.T) {
	checks := EvaluateLedgerConsistency(map[string]bool{"pod-us": true, "pod-eu": false})
	byName := map[string]GoldenSignalCheck{}
	for _, c := range checks {
		byName[c.Name] = c
	}
	if !byName["ledger consistent: pod-us"].OK {
		t.Error("pod-us should be OK (consistent=true)")
	}
	if byName["ledger consistent: pod-eu"].OK {
		t.Error("pod-eu should not be OK (consistent=false)")
	}
}
