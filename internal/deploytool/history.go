// Copyright (C) 2024-2026 American Code
// SPDX-License-Identifier: AGPL-3.0-or-later
// For commercial licensing: jmelton@americancode.org

// Package deploytool implements the `oim deploy` CLI's logic — build/push/
// rollback/status for the deployment RUNBOOK.md documents as an entirely
// manual, unversioned process today ("no rollback mechanism, no
// configuration management, no deployment health checks" — TODO.md's
// Deployment tool item). This package deliberately does NOT reinvent the
// underlying deploy mechanics: it orchestrates the exact same commands
// RUNBOOK.md already documents (rsync source → docker build → the existing
// on-box redeploy-infra.sh/refresh-nodes.py scripts) rather than replacing
// them with a new Ansible/Terraform stack, and adds the three things that
// were actually missing on top: a persisted deployment HISTORY (so "what was
// the last known-good version" is a lookup, not institutional memory),
// automated golden-signal HEALTH verification after every deploy, and a
// ROLLBACK that uses that history instead of an operator's memory.
//
// Command construction (rsync/ssh/docker argv) is kept as pure functions
// here, separate from cmd/oim/deploy.go's actual exec.Command calls, so the
// exact shape of what would run is unit-testable without a real network call,
// SSH binary, or Docker daemon.
package deploytool

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Record is one deployment or rollback event, appended to a history file that
// lives ON THE DEPLOY TARGET (not the operator's own machine) — so `oim
// deploy status`/`history`/`rollback` see accurate, shared state regardless
// of which operator's machine last ran `push`. Mirrors the RUNBOOK's own
// "Images are tagged" rollback note by recording the image tag actually used,
// not just the git commit (a rebuild of the same commit is not guaranteed
// byte-identical, and RUNBOOK.md's own "Alternative" path can deploy a
// pre-built image with no local rebuild at all).
type Record struct {
	Timestamp time.Time `json:"timestamp"`
	// Action is "deploy" or "rollback" — a rollback gets its own record
	// (pointing at the ImageTag/GitCommit it rolled back TO) rather than
	// mutating history in place, so the history is a true append-only log of
	// what actually happened, matching this project's existing ledger
	// philosophy (settlement.Ledger is append-only for the same reason).
	Action string `json:"action"`
	// GitCommit is the short SHA of the source tree that was deployed —
	// empty for a rollback that reused a previously-built image without a
	// local source checkout at that commit (see SelectRollbackTarget's doc
	// comment on why that's sometimes the honest answer).
	GitCommit string `json:"git_commit,omitempty"`
	// ImageTag is the actual docker image tag built/used — the concrete,
	// reproducible target a rollback re-applies. Never empty.
	ImageTag string `json:"image_tag"`
	// Component scopes what was actually redeployed — "all", "coordinator",
	// "node", or "directory" — matching RUNBOOK's "For a single-component
	// change, recreate only that container" guidance.
	Component string `json:"component"`
	// DeployedBy is a best-effort $USER@hostname of the operator's own
	// machine — informational only, never trusted for authorization (the
	// coordinator's admin-panel BDFL auth is the actual access control on
	// who can even VIEW this history via the dashboard; this field just
	// answers "who ran this" for an operator reading the log).
	DeployedBy string `json:"deployed_by"`
	// HealthyAfter is nil until the post-deploy golden-signal check runs,
	// then true/false. A deploy that never got health-checked (e.g. the CLI
	// crashed mid-run) stays nil forever — deliberately distinct from
	// false, since "unknown" and "confirmed unhealthy" call for different
	// operator responses.
	HealthyAfter *bool `json:"healthy_after,omitempty"`
}

// History is the full append-only deployment log for one target.
type History struct {
	Records []Record `json:"records"`
}

// LoadHistory reads path, returning an empty History (not an error) if the
// file doesn't exist yet — a brand-new deploy target has no history, and
// that's a normal starting state, not a failure.
func LoadHistory(path string) (History, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return History{}, nil
	}
	if err != nil {
		return History{}, fmt.Errorf("read history %s: %w", path, err)
	}
	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return History{}, fmt.Errorf("parse history %s: %w", path, err)
	}
	return h, nil
}

// Append adds r to the log. Does not sort — records are expected to be
// appended in chronological order by the caller (real deploys happen in real
// time), and sorting here would silently hide an out-of-order write instead
// of surfacing it as the bug it'd actually be.
func (h *History) Append(r Record) {
	h.Records = append(h.Records, r)
}

// Save writes h to path as indented JSON (human-readable — an operator may
// well `cat` this file directly during an incident, per RUNBOOK.md's own
// terse/direct style).
func (h History) Save(path string) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal history: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write history %s: %w", path, err)
	}
	return nil
}

// Latest returns the most recent record (deploy or rollback), or false if
// history is empty.
func (h History) Latest() (Record, bool) {
	if len(h.Records) == 0 {
		return Record{}, false
	}
	return h.Records[len(h.Records)-1], true
}

// SelectRollbackTarget picks the record `oim deploy rollback` should revert
// to: the most recent record BEFORE the current head whose HealthyAfter was
// true. This is deliberately the "last thing that actually proved itself,"
// not just "the previous entry" — rolling back to a deploy that itself never
// passed its own health check would just trade one broken state for another
// unverified one. Returns false if no such record exists (e.g. this is the
// very first deploy, or nothing in history ever passed health-check) — the
// caller must treat that as "nothing safe to roll back to," not silently
// pick something.
func SelectRollbackTarget(h History) (Record, bool) {
	// Skip the current head (index len-1) — rolling back means going to
	// something OTHER than what's live now, even if the head itself was
	// healthy (an operator invoking rollback wants to undo the head, not
	// re-confirm it).
	for i := len(h.Records) - 2; i >= 0; i-- {
		r := h.Records[i]
		if r.Action == "deploy" && r.HealthyAfter != nil && *r.HealthyAfter {
			return r, true
		}
	}
	return Record{}, false
}
