// Copyright (C) 2024-2026 American Code
// SPDX-License-Identifier: AGPL-3.0-or-later
// For commercial licensing: jmelton@americancode.org

package deploytool

import "fmt"

// GoldenSignalCheck is one health probe result — one line of RUNBOOK.md's
// "Golden signals — is it healthy right now?" section, made machine-checkable.
type GoldenSignalCheck struct {
	Name   string
	OK     bool
	Detail string
}

// GoldenSignalReport is the full post-deploy (or on-demand `oim deploy
// status`) health verification result.
type GoldenSignalReport struct {
	Checks []GoldenSignalCheck
}

// AllHealthy reports whether every check passed. An empty report (nothing was
// actually checked) is NOT healthy — a report with zero checks is far more
// likely a wiring bug than a target with nothing to verify, and treating it
// as healthy would let a broken health-check silently rubber-stamp every
// deploy.
func (r GoldenSignalReport) AllHealthy() bool {
	if len(r.Checks) == 0 {
		return false
	}
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// EvaluateEndpointChecks turns a map of URL -> observed HTTP status code into
// GoldenSignalChecks — pure and separately testable from the actual HTTP
// round trips that gather `statuses` (see cmd/oim/deploy.go). Mirrors
// RUNBOOK.md's "All five public endpoints should return 200" check exactly.
func EvaluateEndpointChecks(statuses map[string]int) []GoldenSignalCheck {
	checks := make([]GoldenSignalCheck, 0, len(statuses))
	for url, code := range statuses {
		checks = append(checks, GoldenSignalCheck{
			Name:   url,
			OK:     code == 200,
			Detail: fmt.Sprintf("HTTP %d", code),
		})
	}
	return checks
}

// EvaluateContainerCount mirrors RUNBOOK.md's `docker ps -q | wc -l` check
// ("expect 119") — expected is caller-supplied (not hardcoded) since the
// live seed's exact container count is a deployment-specific fact (58
// simulated node/stub pairs today, but that's an operational choice, not a
// constant this package should assume).
func EvaluateContainerCount(actual, expected int) GoldenSignalCheck {
	return GoldenSignalCheck{
		Name:   "container count",
		OK:     actual == expected,
		Detail: fmt.Sprintf("%d running, expected %d", actual, expected),
	}
}

// EvaluateLedgerConsistency mirrors RUNBOOK.md's Prometheus watch:
// `oim_ledger_consistent` must be 1 for every coordinator pod checked.
func EvaluateLedgerConsistency(consistentByPod map[string]bool) []GoldenSignalCheck {
	checks := make([]GoldenSignalCheck, 0, len(consistentByPod))
	for pod, consistent := range consistentByPod {
		checks = append(checks, GoldenSignalCheck{
			Name:   "ledger consistent: " + pod,
			OK:     consistent,
			Detail: fmt.Sprintf("oim_ledger_consistent=%v", consistent),
		})
	}
	return checks
}
