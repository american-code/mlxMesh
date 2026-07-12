package main

import (
	"bufio"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

//go:embed deployui.html
var deployUIHTML []byte

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

func deployUICmd() *cobra.Command {
	var port int
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Start a local deploy dashboard at localhost:<port> and open it in the browser",
		RunE: func(cmd *cobra.Command, args []string) error {
			self, err := os.Executable()
			if err != nil {
				self = os.Args[0]
			}

			mux := http.NewServeMux()

			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Write(deployUIHTML)
			})

			// current git SHA + working directory for the header
			mux.HandleFunc("/meta", func(w http.ResponseWriter, r *http.Request) {
				sha, _ := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
				cwd, _ := os.Getwd()
				fmt.Fprintf(w, `{"sha":%q,"cwd":%q}`, strings.TrimSpace(string(sha)), cwd)
			})

			// SSE stream for any deploy subcommand
			mux.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				action := q.Get("action")
				host := q.Get("host")
				sshKey := q.Get("ssh_key")
				source := q.Get("source")
				if source == "" {
					source = "."
				}
				healthEndpoints := q["health_endpoint"]
				expectedContainers := q.Get("expected_containers")
				dryRun := q.Get("dry_run") == "true"

				var cmdArgs []string
				switch action {
				case "push":
					cmdArgs = []string{"deploy", "push", "--host", host, "--ssh-key", sshKey, "--source", source}
					for _, ep := range healthEndpoints {
						if ep != "" {
							cmdArgs = append(cmdArgs, "--health-endpoint", ep)
						}
					}
					if expectedContainers != "" && expectedContainers != "0" {
						cmdArgs = append(cmdArgs, "--expected-containers", expectedContainers)
					}
					if dryRun {
						cmdArgs = append(cmdArgs, "--dry-run")
					}
				case "status":
					cmdArgs = []string{"deploy", "status", "--host", host, "--ssh-key", sshKey}
				case "history":
					cmdArgs = []string{"deploy", "history", "--host", host, "--ssh-key", sshKey}
				case "rollback":
					cmdArgs = []string{"deploy", "rollback", "--host", host, "--ssh-key", sshKey}
					if dryRun {
						cmdArgs = append(cmdArgs, "--dry-run")
					}
				default:
					http.Error(w, "unknown action: "+action, http.StatusBadRequest)
					return
				}

				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				flusher, ok := w.(http.Flusher)
				if !ok {
					http.Error(w, "streaming unsupported", http.StatusInternalServerError)
					return
				}

				pr, pw, err := os.Pipe()
				if err != nil {
					fmt.Fprintf(w, "data: ERROR: %v\n\n", err)
					flusher.Flush()
					return
				}
				proc := exec.CommandContext(r.Context(), self, cmdArgs...)
				proc.Env = os.Environ()
				proc.Stdout = pw
				proc.Stderr = pw
				if err := proc.Start(); err != nil {
					pw.Close()
					pr.Close()
					fmt.Fprintf(w, "data: ERROR: %v\n\n", err)
					flusher.Flush()
					return
				}
				pw.Close()

				scanner := bufio.NewScanner(pr)
				for scanner.Scan() {
					line := stripANSI(scanner.Text())
					fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(line, "\n", " "))
					flusher.Flush()
				}
				pr.Close()

				if proc.Wait() == nil {
					fmt.Fprintf(w, "event: done\ndata: success\n\n")
				} else {
					fmt.Fprintf(w, "event: done\ndata: failed\n\n")
				}
				flusher.Flush()
			})

			addr := fmt.Sprintf("localhost:%d", port)
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listen %s: %w", addr, err)
			}
			url := "http://" + addr
			fmt.Printf("Deploy UI → %s\n", url)
			exec.Command("open", url).Start() //nolint:errcheck
			return http.Serve(ln, mux)
		},
	}
	cmd.Flags().IntVar(&port, "port", 7070, "Local port for the deploy UI")
	return cmd
}
