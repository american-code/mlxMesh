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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"github.com/open-inference-mesh/oim/internal/agent"
	"github.com/open-inference-mesh/oim/internal/bench"
	"github.com/open-inference-mesh/oim/internal/capability"
	"github.com/open-inference-mesh/oim/internal/exoadapter"
	"github.com/open-inference-mesh/oim/internal/governor"
	"github.com/open-inference-mesh/oim/internal/identity"
	"github.com/open-inference-mesh/oim/internal/nodeconfig"
	"github.com/open-inference-mesh/oim/internal/protocol"
	"github.com/open-inference-mesh/oim/internal/version"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "oim",
		Short: "Open Inference Mesh — node agent CLI",
		Long: `oim is the node agent for the Open Inference Mesh (MeshAI).
It wraps a local Exo instance and connects it to the broader community inference mesh.

Quickstart:
  oim node status    — check what this machine can contribute
  oim bench run      — benchmark this node and record its MeasuredSignature
  oim node start     — start the node agent (Milestone 2)`,
	}
	root.AddCommand(nodeCmd())
	root.AddCommand(benchCmd())
	root.AddCommand(versionCmd())
	return root
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print build version, commit, and date",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("oim " + version.String())
		},
	}
}

// --- node subcommands ---

func nodeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node",
		Short: "Node agent commands",
	}
	cmd.AddCommand(nodeStatusCmd())
	cmd.AddCommand(nodeStartCmd())
	return cmd
}

func nodeStatusCmd() *cobra.Command {
	var exoURL string
	var capPct float64

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what this node can currently serve",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()

			_, pub, err := identity.LoadOrCreate()
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			nodeID := protocol.NodeIDFromPubKey(pub)

			fmt.Printf("\nNode ID:         %s\n", nodeID)
			fmt.Printf("Secure Enclave:  %s\n", yesNo(protocol.CheckSecureEnclaveAvailable()))
			fmt.Printf("Foregrounded:    %s\n\n", yesNo(governor.IsForegrounded()))

			// System resources
			sysInfo, err := governor.SystemInfo()
			if err != nil {
				return fmt.Errorf("read system info: %w", err)
			}
			printTable("System Resources", [][]string{
				{"Platform", fmt.Sprintf("%v", sysInfo["platform"])},
				{"Apple Silicon", fmt.Sprintf("%v", sysInfo["is_apple_silicon"])},
				{"Total RAM", fmt.Sprintf("%.2f GB", sysInfo["total_ram_gb"])},
				{"Available RAM", fmt.Sprintf("%.2f GB", sysInfo["available_ram_gb"])},
				{"RAM used", fmt.Sprintf("%.1f%%", sysInfo["used_pct"])},
			})

			committed, err := governor.EnforceContributionCap(capPct)
			if err != nil {
				return fmt.Errorf("compute contribution cap: %w", err)
			}
			total, _ := governor.TotalRAMGB()
			fmt.Printf("Contribution cap: %.2f GB (%d%% of %.2f GB total)\n\n",
				committed, int(capPct*100), total)

			// Exo status
			exo := exoadapter.New(exoURL)
			if !exo.IsHealthy(ctx) {
				fmt.Printf("⚠  Exo not reachable at %s\n", exoURL)
				fmt.Println("   Start Exo first: https://github.com/exo-explore/exo")
				return nil
			}

			opts := capability.DefaultOptions()
			opts.MemoryCapPct = capPct
			manifest, err := capability.AssembleManifest(ctx, exo, pub, opts)
			if err != nil {
				return fmt.Errorf("assemble manifest: %w", err)
			}

			if manifest.IsCluster {
				fmt.Printf("Cluster mode: %d devices (registers as one cluster-node)\n\n",
					*manifest.ClusterDeviceCount)
			}

			if len(manifest.Models) == 0 {
				fmt.Println("No downloaded models found in Exo.")
				fmt.Println("Download a model via Exo first (e.g. exo run llama-3.2-3b)")
				return nil
			}

			rows := make([][]string, len(manifest.Models))
			for i, m := range manifest.Models {
				rows[i] = []string{
					m.ModelID,
					m.Quantization,
					string(m.Runtime),
					strconv.Itoa(m.MaxContextTokens),
					yesNo(m.IsMoE),
				}
			}
			printTableWithHeader("Available Models",
				[]string{"Model ID", "Quantization", "Runtime", "Context", "MoE"},
				rows)

			if manifest.MeasuredSignature != nil {
				sig := manifest.MeasuredSignature
				printTable("Last Benchmark", [][]string{
					{"Prompt ID", sig.BenchmarkPromptID},
					{"Decode (tok/s)", fmt.Sprintf("%.1f", sig.TokensPerSecDecode)},
					{"Prefill (tok/s)", fmt.Sprintf("%.1f", sig.TokensPerSecPrefill)},
					{"Samples", strconv.Itoa(sig.SampleCount)},
					{"Measured at", sig.MeasuredAt},
				})
			} else {
				fmt.Println("No benchmark recorded yet. Run: oim bench run")
			}

			return nil
		},
	}
	cmd.Flags().StringVar(&exoURL, "exo-url", exoadapter.DefaultURL, "Exo HTTP endpoint")
	cmd.Flags().Float64Var(&capPct, "cap", 0.5, "Memory contribution cap (0.0–1.0)")
	return cmd
}

func nodeStartCmd() *cobra.Command {
	var coordinatorURL, listenAddr, geoHint, exoURL, reachabilityEndpoint, userID string
	var capPct, geoLat, geoLng, declaredMemGB float64
	var refreshSec int
	var attemptEnclaveAttestation bool
	var scheduleMode, scheduleStart, scheduleEnd string
	var scheduleDays []string
	var tlsCA string
	var tlsSkipVerify bool
	var tlsCert, tlsKey string
	var disableAutoPortMap bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the node agent and register with a pod coordinator",
		Long: `Registers this node with the assigned pod coordinator and starts
accepting inference jobs. The node serves jobs at --listen and refreshes
its capability manifest every --refresh-interval seconds.

Prerequisites: Exo must be running (oim node status to verify).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Load saved config and apply as defaults; CLI flags take precedence.
			savedCfg, _ := nodeconfig.Load()
			if !cmd.Flags().Changed("cap") && savedCfg.MemoryCapPct > 0 {
				capPct = savedCfg.MemoryCapPct
			}
			if !cmd.Flags().Changed("region") && savedCfg.GeographicHint != "" {
				geoHint = savedCfg.GeographicHint
			}
			if !cmd.Flags().Changed("coordinator") && savedCfg.PodEndpoint != "" {
				coordinatorURL = savedCfg.PodEndpoint
			}
			// A saved reachability_endpoint is only trusted here when it's
			// NOT a loopback/unspecified address. Confirmed live: a stale
			// http://localhost:8765 left in ~/.config/oim/config.json by a
			// pre-pull-mode run got silently resurrected as this fallback on
			// every subsequent start, even though the caller (the menu-bar
			// app) never passed --reachability-endpoint at all — forcing
			// push mode against a target that could never actually be
			// reached and killing the node's earnings entirely. An
			// EXPLICITLY passed --reachability-endpoint (this run's own
			// flag, Changed()==true) is never filtered here, loopback or
			// not — that's a deliberate caller choice, not a stale leftover.
			if !cmd.Flags().Changed("reachability-endpoint") && savedCfg.ReachabilityEndpoint != "" {
				if agent.IsLoopbackReachability(savedCfg.ReachabilityEndpoint) {
					fmt.Printf("Ignoring saved reachability endpoint %q (loopback/unreachable by a remote coordinator) — using pull mode instead\n", savedCfg.ReachabilityEndpoint)
				} else {
					reachabilityEndpoint = savedCfg.ReachabilityEndpoint
				}
			}
			if !cmd.Flags().Changed("exo-url") && savedCfg.ExoURL != "" {
				exoURL = savedCfg.ExoURL
			}

			// Schedule flags write THROUGH to the same config file the dashboard
			// saves to, rather than being threaded into agent.Config directly —
			// the running agent re-reads the schedule from disk on every
			// heartbeat tick (see agent.Run), so both configuration paths
			// (CLI flags here, or the dashboard's Node Setup tab) converge on
			// one live source of truth.
			scheduleChanged := cmd.Flags().Changed("schedule-mode") ||
				cmd.Flags().Changed("schedule-start") ||
				cmd.Flags().Changed("schedule-end") ||
				cmd.Flags().Changed("schedule-days")
			if scheduleChanged {
				sched := savedCfg.Schedule
				if cmd.Flags().Changed("schedule-mode") {
					sched.Mode = scheduleMode
				}
				if cmd.Flags().Changed("schedule-start") {
					sched.DailyStart = scheduleStart
				}
				if cmd.Flags().Changed("schedule-end") {
					sched.DailyEnd = scheduleEnd
				}
				if cmd.Flags().Changed("schedule-days") {
					sched.Days = scheduleDays
				}
				savedCfg.Schedule = sched
				if err := nodeconfig.Save(savedCfg); err != nil {
					return fmt.Errorf("save schedule: %w", err)
				}
				fmt.Printf("Schedule:    mode=%s window=%s-%s days=%v\n", sched.Mode, sched.DailyStart, sched.DailyEnd, sched.Days)
			} else if savedCfg.Schedule.Mode == nodeconfig.ScheduleModeWindow {
				fmt.Printf("Schedule:    mode=%s window=%s-%s days=%v (from saved config)\n",
					savedCfg.Schedule.Mode, savedCfg.Schedule.DailyStart, savedCfg.Schedule.DailyEnd, savedCfg.Schedule.Days)
			}

			priv, pub, err := identity.LoadOrCreate()
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			nodeID := protocol.NodeIDFromPubKey(pub)
			fmt.Printf("Node ID:     %s\n", nodeID)
			fmt.Printf("Coordinator: %s\n", coordinatorURL)
			fmt.Printf("Listening:   %s\n\n", listenAddr)

			// Auto-detect coordinates when neither flag is set.
			// Skipped in Docker (--lat/--lng always provided by gen-compose).
			if geoLat == 0 && geoLng == 0 {
				if lat, lng, err := detectGeoLocation(); err == nil {
					geoLat, geoLng = lat, lng
					fmt.Printf("Geo:         %.4f, %.4f (auto-detected)\n", geoLat, geoLng)
				} else {
					fmt.Printf("Geo:         not detected (%v)\n", err)
				}
			}

			// OIM_CHAOS_DOWNTIME_PCT is a simulation-only backdoor (mirrors OIM_INITIAL_TPS
			// in capability.AssembleManifest) — percent chance per heartbeat this node
			// simulates a downtime window. Never set on a real contributor node.
			var chaosDowntimePct float64
			if v := os.Getenv("OIM_CHAOS_DOWNTIME_PCT"); v != "" {
				fmt.Sscanf(v, "%f", &chaosDowntimePct)
			}

			cfg := agent.Config{
				CoordinatorURL:            coordinatorURL,
				ExoURL:                    exoURL,
				ListenAddr:                listenAddr,
				ReachabilityEndpoint:      reachabilityEndpoint,
				RefreshInterval:           time.Duration(refreshSec) * time.Second,
				CapacityPct:               capPct,
				DeclaredMemoryGB:          declaredMemGB,
				AllowedModels:             savedCfg.AllowedModels,
				UserID:                    userID,
				GeographicHint:            geoHint,
				GeoLat:                    geoLat,
				GeoLng:                    geoLng,
				ChaosDowntimePct:          chaosDowntimePct,
				AttemptEnclaveAttestation: attemptEnclaveAttestation,
				TLSCert:                   tlsCert,
				TLSKey:                    tlsKey,
				DisableAutoPortMap:        disableAutoPortMap,
			}

			// Trust settings for an HTTPS coordinator (private CA or, for throwaway
			// local testing, skip-verify). No-op for plain-HTTP coordinators.
			if err := agent.ConfigureTLS(tlsCA, tlsSkipVerify); err != nil {
				return fmt.Errorf("configure coordinator TLS: %w", err)
			}
			if tlsSkipVerify {
				fmt.Println("WARNING: --tls-skip-verify set — coordinator certificate is NOT verified. Dev only.")
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			fmt.Println("Node agent running. Press Ctrl+C to stop.")
			return agent.Run(ctx, priv, pub, cfg)
		},
	}
	cmd.Flags().StringVar(&coordinatorURL, "coordinator", "http://localhost:9000", "Pod coordinator URL (https:// for TLS)")
	cmd.Flags().StringVar(&tlsCA, "tls-ca", "", "PEM CA file to trust for an HTTPS coordinator with a private/self-signed cert")
	cmd.Flags().BoolVar(&tlsSkipVerify, "tls-skip-verify", false, "Skip coordinator TLS certificate verification (DEV ONLY — insecure)")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "PEM certificate for this node's OWN job endpoint (coordinator dispatch). Self-signed is fine — the coordinator pins the exact fingerprint at registration, no shared CA needed. Omit for plain HTTP (default)")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "PEM private key matching --tls-cert")
	cmd.Flags().StringVar(&listenAddr, "listen", ":8765", "Address for this node to listen for jobs")
	cmd.Flags().StringVar(&exoURL, "exo-url", exoadapter.DefaultURL, "Exo HTTP endpoint")
	cmd.Flags().StringVar(&reachabilityEndpoint, "reachability-endpoint", "", "Endpoint advertised to coordinator (overrides auto-derived; use for NAT/Docker)")
	cmd.Flags().BoolVar(&disableAutoPortMap, "no-auto-port-map", false, "Disable the automatic UPnP/NAT-PMP port-mapping attempt this node makes at startup when --reachability-endpoint is unset. On by default since most contributors are behind a home router's NAT; has no effect at all when --reachability-endpoint is set explicitly. Set this on networks where the attempt is pointless (Docker/cloud/corporate) to skip its ~5s discovery timeout")
	cmd.Flags().Float64Var(&capPct, "cap", 0.5, "Memory contribution cap (0.0–1.0)")
	cmd.Flags().IntVar(&refreshSec, "refresh-interval", 30, "Manifest refresh interval in seconds")
	cmd.Flags().StringVar(&geoHint, "region", "", "Geographic region hint (us/eu/apac); defaults to auto-detect")
	cmd.Flags().Float64Var(&geoLat, "lat", 0, "Approximate latitude of this node (for dashboard mapping; 0 = not declared)")
	cmd.Flags().Float64Var(&geoLng, "lng", 0, "Approximate longitude of this node (for dashboard mapping; 0 = not declared)")
	cmd.Flags().Float64Var(&declaredMemGB, "declared-memory-gb", 0, "Override declared memory GB (0 = read from system; useful for simulation)")
	cmd.Flags().StringVar(&userID, "user-id", "", "User account ID to attribute earned credits to (from your Account tab user ID)")
	cmd.Flags().BoolVar(&attemptEnclaveAttestation, "attempt-enclave-attestation", false,
		"Try to cryptographically prove Secure Enclave possession for high-sensitivity job eligibility. "+
			"Off by default: this requires the binary to be code-signed with Apple Developer Program "+
			"entitlements, which a plain 'go build' from source cannot satisfy. Safe to leave off — "+
			"everything except SensitivityHighRequiresAttestation jobs works without it.")
	cmd.Flags().StringVar(&scheduleMode, "schedule-mode", "", "Contribution schedule: 'always' (default) or 'window' (only share during --schedule-start/--schedule-end)")
	cmd.Flags().StringVar(&scheduleStart, "schedule-start", "", "Window start, 24-hour HH:MM local time (e.g. 22:00). Only used with --schedule-mode window")
	cmd.Flags().StringVar(&scheduleEnd, "schedule-end", "", "Window end, 24-hour HH:MM local time (e.g. 07:00). End < start means the window crosses midnight")
	cmd.Flags().StringSliceVar(&scheduleDays, "schedule-days", nil, "Restrict the window to these weekdays (e.g. mon,tue,wed); empty = every day")
	return cmd
}

// --- bench subcommands ---

func benchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench",
		Short: "Benchmark commands",
	}
	cmd.AddCommand(benchRunCmd())
	cmd.AddCommand(benchCompareCmd())
	return cmd
}

func benchRunCmd() *cobra.Command {
	var exoURL, modelID, promptID string
	var samples int

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Benchmark this node and record its MeasuredSignature",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			exo := exoadapter.New(exoURL)

			if !exo.IsHealthy(ctx) {
				return fmt.Errorf("exo not reachable at %s", exoURL)
			}

			if modelID == "" {
				models, err := exo.GetDownloadedModels(ctx)
				if err != nil || len(models) == 0 {
					return fmt.Errorf("no downloaded models in Exo — download one first")
				}
				for _, key := range []string{"id", "model_id"} {
					if v, ok := models[0][key].(string); ok && v != "" {
						modelID = v
						break
					}
				}
			}
			fmt.Printf("Benchmarking model: %s\n", modelID)
			fmt.Printf("Prompt:             %s (%s)\n", promptID, bench.ReferencePrompts[promptID].Description)
			fmt.Printf("Samples:            %d\n\n", samples)

			sig, err := bench.Run(ctx, exo, modelID, promptID, samples)
			if err != nil {
				return fmt.Errorf("benchmark failed: %w", err)
			}

			if err := capability.SaveBenchmarkResult(sig); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not save benchmark result: %v\n", err)
			}

			printTable("Benchmark Result", [][]string{
				{"Decode (tok/s)", fmt.Sprintf("%.1f", sig.TokensPerSecDecode)},
				{"Prefill (tok/s)", fmt.Sprintf("%.1f", sig.TokensPerSecPrefill)},
				{"Samples averaged", strconv.Itoa(sig.SampleCount)},
				{"Measured at", sig.MeasuredAt},
			})
			fmt.Println("Saved to ~/.config/oim/last_benchmark.json")
			return nil
		},
	}
	cmd.Flags().StringVar(&exoURL, "exo-url", exoadapter.DefaultURL, "Exo HTTP endpoint")
	cmd.Flags().StringVar(&modelID, "model", "", "Model ID to benchmark (default: first downloaded)")
	cmd.Flags().StringVar(&promptID, "prompt", "medium", "Reference prompt: short | medium | long")
	cmd.Flags().IntVar(&samples, "samples", 3, "Number of benchmark runs to average")
	return cmd
}

func benchCompareCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "compare",
		Short: "Compare two MeasuredSignature files for tier-claim verification",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("Usage: oim bench compare — reads ~/.config/oim/last_benchmark.json")
			fmt.Println("Full tier-claim verification is implemented in Milestone 3 (pod coordinator).")
			return nil
		},
	}
	cmd.Flags().Float64("tolerance", 0.20, "Allowed deviation from claimed performance (0.0–1.0)")
	return cmd
}

// --- geo detection ---

// detectGeoLocation calls ipapi.co to infer approximate coordinates from the node's public IP.
// Used only when --lat/--lng are not provided. Times out after 3 s to avoid startup delay.
func detectGeoLocation() (float64, float64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "https://ipapi.co/json/", nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "oim-node/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	var data struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, 0, err
	}
	if data.Latitude == 0 && data.Longitude == 0 {
		return 0, 0, fmt.Errorf("geo detection returned zero coordinates")
	}
	return data.Latitude, data.Longitude, nil
}

// --- display helpers ---

func printTable(title string, rows [][]string) {
	fmt.Printf("%s:\n", title)
	t := tablewriter.NewWriter(os.Stdout)
	t.SetBorder(false)
	t.SetColumnSeparator("  ")
	t.SetHeaderLine(false)
	t.SetNoWhiteSpace(true)
	for _, row := range rows {
		t.Append(row)
	}
	t.Render()
	fmt.Println()
}

func printTableWithHeader(title string, headers []string, rows [][]string) {
	fmt.Printf("%s:\n", title)
	t := tablewriter.NewWriter(os.Stdout)
	t.SetHeader(headers)
	t.SetBorder(true)
	t.SetHeaderColor(
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgCyanColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgCyanColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgCyanColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgCyanColor},
		tablewriter.Colors{tablewriter.Bold, tablewriter.FgCyanColor},
	)
	for _, row := range rows {
		t.Append(row)
	}
	t.Render()
	fmt.Println()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
