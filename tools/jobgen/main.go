// jobgen submits periodic inference jobs to a pod coordinator.
// Use this to test routing, token accounting, and earnings attribution
// against a live coordinator with a real Exo node registered.
//
// Usage:
//
//	go run ./tools/jobgen \
//	  --coordinator http://localhost:9000 \
//	  --user-id <your-account-uuid> \
//	  --api-key oim_xxx \
//	  --model llama-3.2-3b \
//	  --interval 5s \
//	  --count 10
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

var referencePrompts = []struct {
	label   string
	content string
}{
	{"short", "In one sentence, what is machine learning?"},
	{"medium", "Explain the difference between supervised and unsupervised learning. Give one real-world example of each."},
	{"long", "You are a helpful assistant. Summarize the following in 3 bullet points: Deep learning is a subset of machine learning that uses neural networks with multiple layers. Each layer extracts increasingly abstract features from the input data. This approach has revolutionized computer vision, natural language processing, and speech recognition."},
	{"code", "Write a Python function that takes a list of integers and returns the sum of all even numbers."},
	{"analysis", "What are the three most important considerations when designing a distributed system? Be concise."},
}

type jobResult struct {
	promptLabel      string
	model            string
	completionTokens int
	promptTokens     int
	latencyMs        float64
	err              error
	content          string
}

func rootCmd() *cobra.Command {
	var coordinatorURL, userID, apiKey, model string
	var intervalSec int
	var count int
	var verbose bool
	var useQueue bool
	var pointerHost string

	cmd := &cobra.Command{
		Use:   "jobgen",
		Short: "Submit periodic inference jobs to an OIM coordinator",
		Long: `jobgen submits inference jobs at a configurable rate to test routing,
token accounting, and earnings attribution. Run a local oim node agent
(oim node start --user-id <id>) before using this tool to see earnings flow.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// --user-id is optional: omit it to submit anonymously (dev/simulation
			// mode — the coordinator's credit gate only applies when a user ID is
			// present). Pass one to see real earnings/debits flow through your account.

			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			// Pointer-host simulation: announce this generator as an iOS-style
			// coordination participant and tag its jobs with a pointer, so the
			// coordinator's served-pointer counter (and the dashboards' security /
			// coordination layer) show visible activity without a physical device.
			if pointerHost != "" {
				announcePointerHost(ctx, coordinatorURL, pointerHost)
				refreshLiveHosts(ctx, coordinatorURL, pointerHost)
				go func() {
					t := time.NewTicker(30 * time.Second) // stay under the 90s TTL
					defer t.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-t.C:
							announcePointerHost(ctx, coordinatorURL, pointerHost)
							// Discover other live participants (e.g. a real iPad
							// running the app) so their attributed pointers — and
							// thus their credits — actually flow.
							refreshLiveHosts(ctx, coordinatorURL, pointerHost)
						}
					}
				}()
				fmt.Printf("Pointer host: %s (announcing as coordination participant)\n", pointerHost)
			}

			displayUser := userID
			if displayUser == "" {
				displayUser = "(anonymous — no credit accounting)"
			}
			fmt.Printf("\njobgen → %s\n", coordinatorURL)
			fmt.Printf("User:   %s\n", displayUser)
			fmt.Printf("Model:  %s (empty = coordinator decides)\n", model)
			fmt.Printf("Rate:   1 job every %ds\n", intervalSec)
			if count > 0 {
				fmt.Printf("Count:  %d jobs then exit\n", count)
			} else {
				fmt.Printf("Count:  unlimited (Ctrl+C to stop)\n")
			}
			fmt.Println()

			// Print header
			fmt.Printf("%-6s  %-8s  %-10s  %8s  %7s  %7s\n",
				"JOB#", "PROMPT", "MODEL", "TOKENS", "LATENCY", "EARNED")
			fmt.Println("──────  ────────  ──────────  ────────  ───────  ───────")

			ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
			defer ticker.Stop()

			total := struct {
				jobs    int
				tokens  int
				credits float64
				errors  int
			}{}

			jobNum := 0
			for {
				select {
				case <-ctx.Done():
					printSummary(total.jobs, total.tokens, total.credits, total.errors)
					return nil
				case <-ticker.C:
					jobNum++
					if count > 0 && jobNum > count {
						printSummary(total.jobs, total.tokens, total.credits, total.errors)
						return nil
					}

					prompt := referencePrompts[(jobNum-1)%len(referencePrompts)]
					// Rotate the attributed pointer host across this generator and
					// every live participant, so a real connected+linked device
					// (an iPad) visibly earns from simulated traffic.
					effectiveHost := pointerHost
					if pointerHost != "" {
						effectiveHost = pickPointerHost(pointerHost, jobNum)
					}
					r := submitJob(ctx, coordinatorURL, userID, apiKey, model, prompt.label, prompt.content, useQueue, effectiveHost, verbose)

					total.jobs++
					if r.err != nil {
						total.errors++
						fmt.Printf("%-6d  %-8s  %-10s  %8s  %7s  %7s  ERROR: %v\n",
							jobNum, prompt.label, "–", "–", "–", "–", r.err)
					} else {
						total.tokens += r.completionTokens
						earned := float64(r.completionTokens) / 1000.0 * 1.0
						total.credits += earned

						modelShort := r.model
						if len(modelShort) > 10 {
							modelShort = modelShort[:8] + "…"
						}
						fmt.Printf("%-6d  %-8s  %-10s  %8d  %6.0fms  %7.4f\n",
							jobNum, prompt.label, modelShort,
							r.completionTokens, r.latencyMs, earned)

						if verbose && r.content != "" {
							preview := r.content
							if len(preview) > 120 {
								preview = preview[:120] + "…"
							}
							fmt.Printf("       → %s\n", preview)
						}
					}
				}
			}
		},
	}

	cmd.Flags().StringVar(&coordinatorURL, "coordinator", "http://localhost:9000", "Pod coordinator URL")
	cmd.Flags().StringVar(&userID, "user-id", "", "Your user account ID (from Account tab) — required")
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key (oim_xxx from Account tab); if empty, sends X-OIM-User-ID header only")
	cmd.Flags().StringVar(&model, "model", "", "Model to request (empty = coordinator picks from available)")
	cmd.Flags().IntVar(&intervalSec, "interval", 5, "Seconds between jobs")
	cmd.Flags().IntVar(&count, "count", 0, "Number of jobs to submit (0 = unlimited)")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "Print first 120 chars of each response")
	cmd.Flags().BoolVar(&useQueue, "queue", false, "Set X-OIM-Queue: true — coordinator queues the job instead of returning 503 when nodes are busy")
	cmd.Flags().StringVar(&pointerHost, "pointer-host", "", "Simulate an iOS pointer-host: announce as a coordination participant and route jobs through the encrypted-pointer path attributed to this device ID")
	return cmd
}

var (
	liveHostsMu sync.Mutex
	liveHosts   []string // other live participants (excludes this generator)
)

// refreshLiveHosts pulls the coordinator's live coordination participants and
// caches every device id other than this generator's own. Best-effort.
func refreshLiveHosts(ctx context.Context, coordinator, selfHost string) {
	req, err := http.NewRequestWithContext(ctx, "GET", coordinator+"/nodes", nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	var body struct {
		CoordinationNodes []struct {
			DeviceID string `json:"device_id"`
		} `json:"coordination_nodes"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return
	}
	others := make([]string, 0, len(body.CoordinationNodes))
	for _, p := range body.CoordinationNodes {
		if p.DeviceID != "" && p.DeviceID != selfHost {
			others = append(others, p.DeviceID)
		}
	}
	liveHostsMu.Lock()
	liveHosts = others
	liveHostsMu.Unlock()
}

// pickPointerHost round-robins the attributed pointer host across this generator
// and all other live participants, so real connected devices earn their share.
func pickPointerHost(selfHost string, jobNum int) string {
	liveHostsMu.Lock()
	pool := append([]string{selfHost}, liveHosts...)
	liveHostsMu.Unlock()
	return pool[jobNum%len(pool)]
}

// announcePointerHost registers a synthetic coordination participant so the
// coordinator counts pointers served by --pointer-host jobs. Best-effort.
func announcePointerHost(ctx context.Context, coordinator, deviceID string) {
	body, _ := json.Marshal(map[string]any{
		"device_id":       deviceID,
		"role":            "pointer_host",
		"is_mobile":       true,
		"geographic_hint": "sim",
	})
	req, err := http.NewRequestWithContext(ctx, "POST", coordinator+"/coordination/announce", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
}

// syntheticPointer fabricates a plausible encrypted-pointer triple for
// simulation. The ciphertext is never actually fetched (stub-exo ignores it),
// so a deterministic hash/URL/key is sufficient to exercise the pointer path.
// The fetch URL uses a literal RFC 5737 TEST-NET address: the coordinator's
// SSRF guard resolves hostnames, so a made-up `.local` name would be rejected
// with 400 before the job ever dispatched; a literal public-range IP needs no
// DNS and passes validation while remaining guaranteed non-routable.
func syntheticPointer(deviceID string, jobNum int) (hash, fetchURL, ephemeralPubKey string) {
	hash = fmt.Sprintf("sha256:sim-%s-%d", deviceID, jobNum)
	fetchURL = fmt.Sprintf("http://192.0.2.1/payload/%s/%d", deviceID, jobNum)
	ephemeralPubKey = "sim-ephemeral-pubkey"
	return
}

var pointerJobSeq int

func submitJob(ctx context.Context, coordinator, userID, apiKey, model, promptLabel, promptContent string, useQueue bool, pointerHost string, _ bool) jobResult {
	body := map[string]any{
		"messages":   []map[string]any{{"role": "user", "content": promptContent}},
		"max_tokens": 256,
	}
	if model != "" {
		body["model"] = model
	}

	// Route through the encrypted-pointer path when simulating a pointer host.
	if pointerHost != "" {
		pointerJobSeq++
		hash, fetchURL, ephKey := syntheticPointer(pointerHost, pointerJobSeq)
		body["oim_payload_hash"] = hash
		body["oim_payload_fetch_url"] = fetchURL
		body["oim_ephemeral_public_key"] = ephKey
	}

	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", coordinator+"/v1/chat/completions", bytes.NewReader(b))
	if err != nil {
		return jobResult{promptLabel: promptLabel, err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	} else if userID != "" {
		req.Header.Set("X-OIM-User-ID", userID)
	}
	if useQueue {
		req.Header.Set("X-OIM-Queue", "true")
	}
	if pointerHost != "" {
		req.Header.Set("X-OIM-Pointer-Host", pointerHost)
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	latency := float64(time.Since(start).Milliseconds())
	if err != nil {
		return jobResult{promptLabel: promptLabel, err: err, latencyMs: latency}
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return jobResult{
			promptLabel: promptLabel,
			latencyMs:   latency,
			err:         fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw)),
		}
	}

	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return jobResult{promptLabel: promptLabel, latencyMs: latency, err: err}
	}

	// Extract model, tokens, content from OpenAI-format response
	completionTokens, promptTokens := 0, 0
	if usage, ok := result["usage"].(map[string]any); ok {
		if n, ok := usage["completion_tokens"].(float64); ok {
			completionTokens = int(n)
		}
		if n, ok := usage["prompt_tokens"].(float64); ok {
			promptTokens = int(n)
		}
	}
	// If usage not populated (stub-exo), fall back to estimate
	if completionTokens == 0 {
		completionTokens = 256 // max_tokens default
	}

	modelUsed, _ := result["model"].(string)
	var content string
	if choices, ok := result["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				content, _ = msg["content"].(string)
			}
		}
	}

	return jobResult{
		promptLabel:      promptLabel,
		model:            modelUsed,
		completionTokens: completionTokens,
		promptTokens:     promptTokens,
		latencyMs:        latency,
		content:          content,
	}
}

func printSummary(jobs, tokens int, credits float64, errors int) {
	fmt.Println()
	fmt.Println("─────────────────────────────────────────")
	fmt.Printf("Jobs submitted:   %d\n", jobs)
	fmt.Printf("Errors:           %d\n", errors)
	fmt.Printf("Tokens generated: %d\n", tokens)
	fmt.Printf("Credits earned:   %.4f  (%.2f per 1k tokens, moderate tier)\n",
		credits, 1.0)
	fmt.Printf("Estimated cost:   %.4f credits\n", credits)
	fmt.Println("─────────────────────────────────────────")
	fmt.Println("Check your balance: Account tab → Credit Balance")
}
