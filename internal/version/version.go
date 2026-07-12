// Copyright (C) 2024-2026 American Code
// SPDX-License-Identifier: AGPL-3.0-or-later
// For commercial licensing: jmelton@americancode.org

// Package version carries build-stamp metadata set at link time. The three
// vars are overridden by the release build via -ldflags -X so every binary can
// report exactly which commit it was built from — essential for support and
// incident triage ("which build is the pod-us coordinator actually running?").
// Defaults keep `go build`/`go run` working without any ldflags.
package version

import (
	"fmt"
	"runtime"
)

var (
	// Version is the release tag (e.g. "v0.16"), or "dev" for an unstamped build.
	Version = "dev"
	// Commit is the short git SHA the build was cut from.
	Commit = "none"
	// Date is the build timestamp (RFC3339, UTC), stamped by the release script.
	Date = "unknown"
)

// String returns a one-line human-readable build stamp.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s)", Version, Commit, Date, runtime.Version())
}
