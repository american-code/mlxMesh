package main

import (
	"context"
	"fmt"
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
	"github.com/open-inference-mesh/oim/internal/protocol"
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
	return root
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
	var coordinatorURL, listenAddr, geoHint, exoURL, reachabilityEndpoint string
	var capPct float64
	var refreshSec int

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the node agent and register with a pod coordinator",
		Long: `Registers this node with the assigned pod coordinator and starts
accepting inference jobs. The node serves jobs at --listen and refreshes
its capability manifest every --refresh-interval seconds.

Prerequisites: Exo must be running (oim node status to verify).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			priv, pub, err := identity.LoadOrCreate()
			if err != nil {
				return fmt.Errorf("load identity: %w", err)
			}
			nodeID := protocol.NodeIDFromPubKey(pub)
			fmt.Printf("Node ID:     %s\n", nodeID)
			fmt.Printf("Coordinator: %s\n", coordinatorURL)
			fmt.Printf("Listening:   %s\n\n", listenAddr)

			cfg := agent.Config{
				CoordinatorURL:       coordinatorURL,
				ExoURL:               exoURL,
				ListenAddr:           listenAddr,
				ReachabilityEndpoint: reachabilityEndpoint,
				RefreshInterval:      time.Duration(refreshSec) * time.Second,
				CapacityPct:          capPct,
				GeographicHint:       geoHint,
			}

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			fmt.Println("Node agent running. Press Ctrl+C to stop.")
			return agent.Run(ctx, priv, pub, cfg)
		},
	}
	cmd.Flags().StringVar(&coordinatorURL, "coordinator", "http://localhost:9000", "Pod coordinator URL")
	cmd.Flags().StringVar(&listenAddr, "listen", ":8765", "Address for this node to listen for jobs")
	cmd.Flags().StringVar(&exoURL, "exo-url", exoadapter.DefaultURL, "Exo HTTP endpoint")
	cmd.Flags().StringVar(&reachabilityEndpoint, "reachability-endpoint", "", "Endpoint advertised to coordinator (overrides auto-derived; use for NAT/Docker)")
	cmd.Flags().Float64Var(&capPct, "cap", 0.5, "Memory contribution cap (0.0–1.0)")
	cmd.Flags().IntVar(&refreshSec, "refresh-interval", 30, "Manifest refresh interval in seconds")
	cmd.Flags().StringVar(&geoHint, "region", "", "Geographic region hint (us/eu/apac); defaults to auto-detect")
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
