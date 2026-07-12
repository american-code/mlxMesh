// Copyright (C) 2024-2026 American Code
// SPDX-License-Identifier: AGPL-3.0-or-later
// For commercial licensing: jmelton@americancode.org

package deploytool

import (
	"fmt"
	"os"
	"time"
)

// RsyncPushArgs builds the rsync argv (everything after the `rsync` binary
// name) for syncing source to the deploy target — byte-for-byte the same
// include/exclude list RUNBOOK.md's "Deploy a new version" step 1 documents,
// so this tool syncs exactly what an operator following the runbook by hand
// would sync, nothing more or less. sshKey may be empty (rsync uses ssh's
// default key resolution).
func RsyncPushArgs(sourceDir, sshHost, remoteDir, sshKey string) []string {
	args := []string{"-az"}
	if sshKey != "" {
		args = append(args, "-e", "ssh -i "+sshKey+" -o StrictHostKeyChecking=no")
	}
	args = append(args,
		"--include=go.mod",
		"--include=go.sum",
		"--include=Dockerfile",
		"--include=cmd/***",
		"--include=internal/***",
		"--include=tools/***",
		"--exclude=*",
		sourceDir+"/",
		sshHost+":"+remoteDir+"/",
	)
	return args
}

// RemoteBuildCommand is the on-box `docker build` step (RUNBOOK step 3),
// tagging with imageTag instead of the runbook's floating `mlxmesh` tag so
// every build this tool performs is individually addressable for a later
// rollback — the floating tag is also updated (a second `docker tag`) so
// existing on-box scripts that reference plain `mlxmesh` keep working
// unmodified.
func RemoteBuildCommand(remoteDir, imageTag string) string {
	return fmt.Sprintf(
		"cd %s && docker build -t mlxmesh:%s . && docker tag mlxmesh:%s mlxmesh:latest-deploy && docker tag mlxmesh:%s mlxmesh:latest",
		remoteDir, imageTag, imageTag, imageTag,
	)
}

// RemoteRetagCommand points the floating `mlxmesh` tag at an already-built
// image (imageTag) without rebuilding — the fast path `oim deploy rollback`
// uses when the target image is still present in the box's local Docker
// image cache (Docker doesn't garbage-collect tags on its own), avoiding a
// full rebuild for a same-day rollback.
func RemoteRetagCommand(imageTag string) string {
	return fmt.Sprintf("docker tag mlxmesh:%s mlxmesh:latest-deploy", imageTag)
}

// RemoteImageExistsCommand checks whether imageTag is present in the box's
// local Docker image cache — the test RemoteRetagCommand's fast path depends
// on. `docker image inspect` exits non-zero if the tag is absent, which the
// caller (cmd/oim/deploy.go) checks via the command's exit code rather than
// parsing output.
func RemoteImageExistsCommand(imageTag string) string {
	return fmt.Sprintf("docker image inspect mlxmesh:%s > /dev/null 2>&1", imageTag)
}

// RemoteRedeployInfraCommand re-runs the on-box redeploy-infra.sh script
// (RUNBOOK step 4) — recreates directory + coordinator containers from
// whatever image the `mlxmesh:latest-deploy`/`mlxmesh` tag currently points
// at. Deliberately does NOT reimplement what that script does (stopping/
// recreating containers, preserving data volumes) — it already exists,
// is proven, and reinventing it here would be a second, divergent copy of
// exactly the kind of undocumented infra this tool exists to stop
// accumulating.
func RemoteRedeployInfraCommand() string {
	return "bash ~/redeploy-infra.sh"
}

// RemoteRefreshNodesCommand re-runs the on-box refresh-nodes.py script
// (RUNBOOK step 5) over the given 1-indexed node range.
func RemoteRefreshNodesCommand(start, end int) string {
	return fmt.Sprintf("python3 ~/refresh-nodes.py %d %d", start, end)
}

// RemoteContainerCountCommand mirrors RUNBOOK's `docker ps -q | wc -l`.
func RemoteContainerCountCommand() string {
	return "docker ps -q | wc -l"
}

// ImageTag generates a unique, sortable, human-readable image tag from the
// short git commit and a timestamp — e.g. "mlxmesh-a1b2c3d-20260315-143022".
// Timestamped (not just the commit) because the SAME commit can legitimately
// be rebuilt more than once (a Dockerfile/base-image change with no source
// diff, or simply re-running a deploy) and each build should still be its
// own addressable rollback target.
func ImageTag(gitCommit string, t time.Time) string {
	return fmt.Sprintf("mlxmesh-%s-%s", gitCommit, t.UTC().Format("20060102-150405"))
}

// DeployedBy returns a best-effort "$USER@hostname" identifying the operator
// machine a deploy/rollback was run from — informational only (see
// Record.DeployedBy's doc comment on why this is never an authorization
// signal). Falls back to "unknown" for either half rather than erroring —
// this is a nice-to-have log field, not something a deploy should ever be
// blocked on.
func DeployedBy() string {
	user := os.Getenv("USER")
	if user == "" {
		user = "unknown"
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return user + "@" + host
}
