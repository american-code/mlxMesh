// Copyright (C) 2024 Open Inference Mesh
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

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
