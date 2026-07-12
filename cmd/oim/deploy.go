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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/open-inference-mesh/oim/internal/deploytool"
)

// remoteHistoryPath is where the deployment history JSON lives ON THE DEPLOY
// TARGET (not the operator's own machine) — shared state every operator's
// `oim deploy` invocation reads/writes, matching how the target's Docker
// state and running services are themselves shared, not per-operator.
const remoteHistoryPath = "~/.oim-deployments.json"

func deployCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Build, push, verify, and roll back a coordinator/directory/node deployment",
		Long: `oim deploy orchestrates the exact deploy steps RUNBOOK.md documents
(rsync source -> docker build -> the on-box redeploy-infra.sh/refresh-nodes.py
scripts) rather than replacing them, and adds the three things that were
missing from the purely manual process: a persisted deployment HISTORY, an
automated post-deploy HEALTH check (RUNBOOK's "Golden signals"), and a
ROLLBACK that uses that history instead of an operator's memory.

Every subcommand supports --dry-run, which prints the exact commands that
would run (rsync/ssh/docker) without executing them — use it to review a
deploy plan before committing to a live host.`,
	}
	cmd.AddCommand(deployPushCmd())
	cmd.AddCommand(deployRollbackCmd())
	cmd.AddCommand(deployStatusCmd())
	cmd.AddCommand(deployHistoryCmd())
	return cmd
}

// --- shared flags/helpers ---

// remoteRunner executes (or, in dry-run mode, prints) a command on the deploy
// target over SSH, or locally when host is empty (a same-box `oim deploy`
// invocation needs no SSH hop at all).
type remoteRunner struct {
	sshHost string // "" = run locally, no ssh
	sshKey  string // "" = use ssh's own default key resolution
	dryRun  bool
}

// run executes a MUTATING remote command (build, redeploy-infra.sh,
// refresh-nodes.py, a retag) — respects dryRun by printing instead of
// actually running, since these are exactly the effects --dry-run promises
// not to have.
func (r remoteRunner) run(remoteCmd string) (string, error) {
	if r.dryRun {
		if r.sshHost == "" {
			fmt.Printf("[dry-run] %s\n", remoteCmd)
		} else {
			fmt.Printf("[dry-run] ssh %s -- %s\n", r.sshHost, remoteCmd)
		}
		return "", nil
	}
	return r.exec(remoteCmd)
}

// query executes a READ-ONLY remote command and ALWAYS actually runs it, even
// under --dry-run — reading current state (history, image presence, running
// containers) changes nothing, and skipping it would make a "dry run" unable
// to show a realistic plan at all (e.g. `deploy rollback --dry-run` could
// never report a real rollback target). Only `run` (mutating commands) and
// `writeRemoteFile` (history writes) respect dryRun.
func (r remoteRunner) query(remoteCmd string) (string, error) {
	return r.exec(remoteCmd)
}

func (r remoteRunner) exec(remoteCmd string) (string, error) {
	var cmd *exec.Cmd
	if r.sshHost == "" {
		cmd = exec.Command("sh", "-c", remoteCmd)
	} else {
		args := []string{}
		if r.sshKey != "" {
			args = append(args, "-i", r.sshKey)
		}
		args = append(args, r.sshHost, remoteCmd)
		cmd = exec.Command("ssh", args...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stdout.String(), fmt.Errorf("%s: %w (stderr: %s)", remoteCmd, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// writeRemoteFile overwrites path on the target with contents — used to
// persist the updated history JSON back after append. Piped over stdin
// rather than embedded in the command string so history content (JSON with
// quotes/newlines) never has to survive shell-escaping.
func (r remoteRunner) writeRemoteFile(path, contents string) error {
	remoteCmd := "cat > " + path
	if r.dryRun {
		fmt.Printf("[dry-run] write %d bytes to %s (host=%q)\n", len(contents), path, r.sshHost)
		return nil
	}
	var cmd *exec.Cmd
	if r.sshHost == "" {
		cmd = exec.Command("sh", "-c", remoteCmd)
	} else {
		args := []string{}
		if r.sshKey != "" {
			args = append(args, "-i", r.sshKey)
		}
		args = append(args, r.sshHost, remoteCmd)
		cmd = exec.Command("ssh", args...)
	}
	cmd.Stdin = strings.NewReader(contents)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("write %s: %w (stderr: %s)", path, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// fetchRemoteHistory reads and parses the history file from the target. A
// missing file (brand-new target, nothing deployed via this tool yet) is not
// an error — mirrors deploytool.LoadHistory's own "missing = empty" contract,
// just over SSH instead of a local path. Always a real read (see
// remoteRunner.query) even under --dry-run, so a dry-run rollback can show an
// actual target instead of always reporting "no history."
func fetchRemoteHistory(r remoteRunner) (deploytool.History, error) {
	out, err := r.query("cat " + remoteHistoryPath + " 2>/dev/null || true")
	if err != nil {
		return deploytool.History{}, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return deploytool.History{}, nil
	}
	var h deploytool.History
	if err := json.Unmarshal([]byte(out), &h); err != nil {
		return deploytool.History{}, fmt.Errorf("parse remote history: %w", err)
	}
	return h, nil
}

func saveRemoteHistory(r remoteRunner, h deploytool.History) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal history: %w", err)
	}
	return r.writeRemoteFile(remoteHistoryPath, string(data))
}

// runGoldenSignals is the impure "gather" half of deploytool's golden-signal
// check — real HTTP calls and one SSH round trip — kept separate from
// deploytool.Evaluate* (the pure half) so the evaluation logic stays unit
// testable without a network.
func runGoldenSignals(r remoteRunner, endpoints []string, expectedContainers int) deploytool.GoldenSignalReport {
	if r.dryRun {
		fmt.Println("[dry-run] skipping golden-signal health check")
		return deploytool.GoldenSignalReport{}
	}
	statuses := make(map[string]int, len(endpoints))
	client := &http.Client{Timeout: 8 * time.Second}
	for _, u := range endpoints {
		resp, err := client.Get(u)
		if err != nil {
			statuses[u] = 0
			continue
		}
		statuses[u] = resp.StatusCode
		resp.Body.Close()
	}
	checks := deploytool.EvaluateEndpointChecks(statuses)

	if expectedContainers > 0 {
		out, err := r.run(deploytool.RemoteContainerCountCommand())
		count := -1
		if err == nil {
			count, _ = strconv.Atoi(strings.TrimSpace(out))
		}
		checks = append(checks, deploytool.EvaluateContainerCount(count, expectedContainers))
	}
	return deploytool.GoldenSignalReport{Checks: checks}
}

func printGoldenSignalReport(report deploytool.GoldenSignalReport) {
	// An empty report means health-checking was SKIPPED (dry-run mode — see
	// runGoldenSignals), not that anything failed. GoldenSignalReport.AllHealthy
	// deliberately treats "empty" as unhealthy (a real wiring bug shouldn't
	// silently read as healthy), but that same conservatism would misreport a
	// dry-run's intentional skip as a failure here — so it gets its own,
	// distinct message instead of reusing the pass/fail wording.
	if len(report.Checks) == 0 {
		fmt.Println("(health check skipped)")
		return
	}
	rows := make([][]string, len(report.Checks))
	for i, c := range report.Checks {
		status := "FAIL"
		if c.OK {
			status = "OK"
		}
		rows[i] = []string{c.Name, status, c.Detail}
	}
	printTableWithHeader("Golden Signals", []string{"Check", "Status", "Detail"}, rows)
	if report.AllHealthy() {
		fmt.Println("✓ all checks healthy")
	} else {
		fmt.Println("✗ one or more checks failed — see table above")
	}
}

func gitShortSHA() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

// --- subcommands ---

func deployPushCmd() *cobra.Command {
	var host, sshKey, sourceDir, remoteDir, component string
	var nodeStart, nodeEnd, expectedContainers int
	var endpoints []string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Sync source, build, redeploy, and health-check a target (RUNBOOK.md's 'Deploy a new version', automated)",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := remoteRunner{sshHost: host, sshKey: sshKey, dryRun: dryRun}
			commit := gitShortSHA()
			tag := deploytool.ImageTag(commit, time.Now())

			fmt.Printf("Deploying commit %s as image tag %s (component=%s) to %s\n", commit, tag, component, hostLabel(host))

			// 1. sync source
			rsyncArgs := deploytool.RsyncPushArgs(sourceDir, host, remoteDir, sshKey)
			if dryRun || host == "" {
				fmt.Printf("[dry-run] rsync %s\n", strings.Join(rsyncArgs, " "))
			} else {
				rsync := exec.Command("rsync", rsyncArgs...)
				rsync.Stdout, rsync.Stderr = os.Stdout, os.Stderr
				if err := rsync.Run(); err != nil {
					return fmt.Errorf("rsync source: %w", err)
				}
			}

			// 2. build
			if _, err := r.run(deploytool.RemoteBuildCommand(remoteDir, tag)); err != nil {
				return fmt.Errorf("remote build: %w", err)
			}

			// 3. redeploy the requested component(s)
			if component == "all" || component == "coordinator" || component == "directory" {
				if _, err := r.run(deploytool.RemoteRedeployInfraCommand()); err != nil {
					return fmt.Errorf("redeploy infra: %w", err)
				}
			}
			if component == "all" || component == "node" {
				if _, err := r.run(deploytool.RemoteRefreshNodesCommand(nodeStart, nodeEnd)); err != nil {
					return fmt.Errorf("refresh nodes: %w", err)
				}
			}

			// 4. record the deploy BEFORE health-check, so even a health-check
			// that never completes still leaves a HealthyAfter=nil ("unknown")
			// record rather than no record at all.
			healthy := (*bool)(nil)
			record := deploytool.Record{
				Timestamp: time.Now().UTC(), Action: "deploy", GitCommit: commit,
				ImageTag: tag, Component: component, DeployedBy: deploytool.DeployedBy(),
				HealthyAfter: healthy,
			}
			hist, err := fetchRemoteHistory(r)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not read existing history (%v) — starting fresh\n", err)
			}
			hist.Append(record)
			if err := saveRemoteHistory(r, hist); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not persist deployment record: %v\n", err)
			}

			// 5. health check, then update the just-appended record in place.
			report := runGoldenSignals(r, endpoints, expectedContainers)
			printGoldenSignalReport(report)
			if !dryRun {
				ok := report.AllHealthy()
				hist.Records[len(hist.Records)-1].HealthyAfter = &ok
				if err := saveRemoteHistory(r, hist); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not persist health-check result: %v\n", err)
				}
				if !ok {
					return fmt.Errorf("deploy completed but failed its post-deploy health check — see table above; consider `oim deploy rollback`")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "SSH destination (user@host); empty = run locally, no SSH hop")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path (passed to ssh -i)")
	cmd.Flags().StringVar(&sourceDir, "source", ".", "Local source directory to sync")
	cmd.Flags().StringVar(&remoteDir, "remote-dir", "~/mlxmesh-src", "Source directory on the target")
	cmd.Flags().StringVar(&component, "component", "all", "What to redeploy: all | coordinator | directory | node")
	cmd.Flags().IntVar(&nodeStart, "node-start", 1, "First simulated node index to refresh (component=all|node)")
	cmd.Flags().IntVar(&nodeEnd, "node-end", 58, "Last simulated node index to refresh (component=all|node)")
	cmd.Flags().IntVar(&expectedContainers, "expected-containers", 0, "Expected `docker ps` count for the container-count golden signal (0 = skip this check)")
	cmd.Flags().StringSliceVar(&endpoints, "health-endpoint", nil, "Public URL(s) to health-check after deploy (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the commands that would run without executing them")
	return cmd
}

func deployRollbackCmd() *cobra.Command {
	var host, sshKey string
	var expectedContainers int
	var endpoints []string
	var dryRun bool
	var nodeStart, nodeEnd int
	var component string

	cmd := &cobra.Command{
		Use:   "rollback",
		Short: "Revert to the most recent previously-healthy deploy recorded in history",
		Long: `Finds the most recent deploy in the target's history that itself passed
its post-deploy health check (skipping the current head and anything that
never passed), re-tags that image as the active one (rebuilding only if it's
no longer in the target's local Docker image cache — Docker does not garbage
collect tags on its own, so a same-day rollback is usually just a re-tag),
redeploys, and health-checks again.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			r := remoteRunner{sshHost: host, sshKey: sshKey, dryRun: dryRun}
			hist, err := fetchRemoteHistory(r)
			if err != nil {
				return fmt.Errorf("read history: %w", err)
			}
			target, ok := deploytool.SelectRollbackTarget(hist)
			if !ok {
				return fmt.Errorf("no previously-healthy deploy found in history — nothing safe to roll back to")
			}
			fmt.Printf("Rolling back to image tag %s (commit %s, deployed %s)\n", target.ImageTag, target.GitCommit, target.Timestamp.Format(time.RFC3339))

			// Read-only check — always run for real (even under --dry-run, see
			// remoteRunner.query) so a dry-run plan can accurately report
			// whether this rollback would need a rebuild, instead of always
			// silently assuming the fast retag-only path.
			existsCmd := deploytool.RemoteImageExistsCommand(target.ImageTag)
			if _, err := r.query(existsCmd); err != nil {
				return fmt.Errorf("image %s is no longer present on the target's local Docker cache — a rollback this old needs a fresh `oim deploy push` from that commit's source instead: %w", target.ImageTag, err)
			}
			if _, err := r.run(deploytool.RemoteRetagCommand(target.ImageTag)); err != nil {
				return fmt.Errorf("retag: %w", err)
			}

			if component == "all" || component == "coordinator" || component == "directory" {
				if _, err := r.run(deploytool.RemoteRedeployInfraCommand()); err != nil {
					return fmt.Errorf("redeploy infra: %w", err)
				}
			}
			if component == "all" || component == "node" {
				if _, err := r.run(deploytool.RemoteRefreshNodesCommand(nodeStart, nodeEnd)); err != nil {
					return fmt.Errorf("refresh nodes: %w", err)
				}
			}

			report := runGoldenSignals(r, endpoints, expectedContainers)
			printGoldenSignalReport(report)

			healthy := report.AllHealthy()
			hist.Append(deploytool.Record{
				Timestamp: time.Now().UTC(), Action: "rollback", GitCommit: target.GitCommit,
				ImageTag: target.ImageTag, Component: component, DeployedBy: deploytool.DeployedBy(),
				HealthyAfter: &healthy,
			})
			if !dryRun {
				if err := saveRemoteHistory(r, hist); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not persist rollback record: %v\n", err)
				}
				if !healthy {
					return fmt.Errorf("rollback completed but failed its post-rollback health check — see table above")
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "SSH destination (user@host); empty = run locally, no SSH hop")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path (passed to ssh -i)")
	cmd.Flags().StringVar(&component, "component", "all", "What to redeploy: all | coordinator | directory | node")
	cmd.Flags().IntVar(&nodeStart, "node-start", 1, "First simulated node index to refresh (component=all|node)")
	cmd.Flags().IntVar(&nodeEnd, "node-end", 58, "Last simulated node index to refresh (component=all|node)")
	cmd.Flags().IntVar(&expectedContainers, "expected-containers", 0, "Expected `docker ps` count for the container-count golden signal (0 = skip this check)")
	cmd.Flags().StringSliceVar(&endpoints, "health-endpoint", nil, "Public URL(s) to health-check after rollback (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the commands that would run without executing them")
	return cmd
}

func deployStatusCmd() *cobra.Command {
	var host, sshKey string
	var expectedContainers int
	var endpoints []string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Run RUNBOOK.md's golden-signal health check against a target",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := remoteRunner{sshHost: host, sshKey: sshKey}
			report := runGoldenSignals(r, endpoints, expectedContainers)
			printGoldenSignalReport(report)
			hist, err := fetchRemoteHistory(r)
			if err == nil {
				if latest, ok := hist.Latest(); ok {
					fmt.Printf("\nLast recorded action: %s of %s (commit %s) at %s by %s\n",
						latest.Action, latest.ImageTag, latest.GitCommit, latest.Timestamp.Format(time.RFC3339), latest.DeployedBy)
				}
			}
			if !report.AllHealthy() {
				return fmt.Errorf("one or more golden signals are unhealthy")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "SSH destination (user@host); empty = run locally, no SSH hop")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path (passed to ssh -i)")
	cmd.Flags().IntVar(&expectedContainers, "expected-containers", 0, "Expected `docker ps` count for the container-count golden signal (0 = skip this check)")
	cmd.Flags().StringSliceVar(&endpoints, "health-endpoint", nil, "Public URL(s) to health-check (repeatable)")
	return cmd
}

func deployHistoryCmd() *cobra.Command {
	var host, sshKey string
	var limit int

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show the deployment/rollback history recorded on a target",
		RunE: func(cmd *cobra.Command, args []string) error {
			r := remoteRunner{sshHost: host, sshKey: sshKey}
			hist, err := fetchRemoteHistory(r)
			if err != nil {
				return err
			}
			if len(hist.Records) == 0 {
				fmt.Println("No deployment history recorded on this target yet.")
				return nil
			}
			records := hist.Records
			if limit > 0 && len(records) > limit {
				records = records[len(records)-limit:]
			}
			rows := make([][]string, len(records))
			for i, rec := range records {
				healthy := "unknown"
				if rec.HealthyAfter != nil {
					healthy = "unhealthy"
					if *rec.HealthyAfter {
						healthy = "healthy"
					}
				}
				rows[i] = []string{
					rec.Timestamp.Format(time.RFC3339), rec.Action, rec.Component,
					rec.ImageTag, rec.GitCommit, rec.DeployedBy, healthy,
				}
			}
			printTableWithHeader("Deployment History",
				[]string{"Time", "Action", "Component", "Image Tag", "Commit", "Deployed By", "Health"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "SSH destination (user@host); empty = run locally, no SSH hop")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path (passed to ssh -i)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Show at most this many most-recent records (0 = all)")
	return cmd
}

func hostLabel(host string) string {
	if host == "" {
		return "localhost"
	}
	return host
}
