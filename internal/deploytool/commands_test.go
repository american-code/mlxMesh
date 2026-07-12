package deploytool

import (
	"strings"
	"testing"
	"time"
)

func TestRsyncPushArgs_MatchesRunbookIncludeList(t *testing.T) {
	args := RsyncPushArgs("/home/dev/mesh", "ec2-user@1.2.3.4", "/home/ec2-user/mlxmesh-src", "")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--include=go.mod", "--include=go.sum", "--include=Dockerfile",
		"--include=cmd/***", "--include=internal/***", "--include=tools/***",
		"--exclude=*",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("rsync args missing %q: %v", want, args)
		}
	}
	if args[len(args)-2] != "/home/dev/mesh/" {
		t.Errorf("source arg = %q, want trailing slash for rsync directory-contents semantics", args[len(args)-2])
	}
	if args[len(args)-1] != "ec2-user@1.2.3.4:/home/ec2-user/mlxmesh-src/" {
		t.Errorf("destination arg = %q", args[len(args)-1])
	}
}

func TestImageTag_DeterministicAndSortable(t *testing.T) {
	t1 := time.Date(2026, 3, 15, 14, 30, 22, 0, time.UTC)
	tag := ImageTag("a1b2c3d", t1)
	if tag != "mlxmesh-a1b2c3d-20260315-143022" {
		t.Errorf("ImageTag = %q, want mlxmesh-a1b2c3d-20260315-143022", tag)
	}
	// A later timestamp for the SAME commit must sort after — rebuilding an
	// unchanged commit is a real scenario (Dockerfile-only change) and each
	// build must stay its own addressable rollback target.
	t2 := t1.Add(time.Hour)
	tagLater := ImageTag("a1b2c3d", t2)
	if tagLater <= tag {
		t.Errorf("a later build of the same commit must sort after the earlier one: %q vs %q", tag, tagLater)
	}
}

func TestRemoteBuildCommand_TagsBothImageTagAndFloatingLatest(t *testing.T) {
	cmd := RemoteBuildCommand("/home/ec2-user/mlxmesh-src", "mlxmesh-abc123-20260101-000000")
	if !strings.Contains(cmd, "docker build -t mlxmesh:mlxmesh-abc123-20260101-000000") {
		t.Errorf("build command missing the addressable tag: %q", cmd)
	}
	if !strings.Contains(cmd, "docker tag mlxmesh:mlxmesh-abc123-20260101-000000 mlxmesh:latest-deploy") {
		t.Errorf("build command missing the latest-deploy floating tag: %q", cmd)
	}
	if !strings.Contains(cmd, "docker tag mlxmesh:mlxmesh-abc123-20260101-000000 mlxmesh:latest") {
		t.Errorf("build command missing the mlxmesh:latest tag (needed by redeploy-infra.sh): %q", cmd)
	}
}

func TestDeployedBy_NeverEmpty(t *testing.T) {
	// Can't control $USER/hostname in a portable test, but the contract that
	// matters is "never empty, always has the @ separator."
	got := DeployedBy()
	if got == "" || !strings.Contains(got, "@") {
		t.Errorf("DeployedBy() = %q, want a non-empty user@host string", got)
	}
}
