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
	promptLabel     string
	model           string
	completionTokens int
	promptTokens    int
	latencyMs       float64
	err             error
	content         string
}

func rootCmd() *cobra.Command {
	var coordinatorURL, userID, apiKey, model string
	var intervalSec int
	var count int
	var verbose bool
	var useQueue bool

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
					r := submitJob(ctx, coordinatorURL, userID, apiKey, model, prompt.label, prompt.content, useQueue, verbose)

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
	return cmd
}

func submitJob(ctx context.Context, coordinator, userID, apiKey, model, promptLabel, promptContent string, useQueue bool, _ bool) jobResult {
	body := map[string]any{
		"messages":   []map[string]any{{"role": "user", "content": promptContent}},
		"max_tokens": 256,
	}
	if model != "" {
		body["model"] = model
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
