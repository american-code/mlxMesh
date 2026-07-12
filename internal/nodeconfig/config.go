// Package nodeconfig handles persisting node contributor settings to
// ~/.config/oim/config.json. CLI flags always override stored values.
// The file is written by the dashboard's /config endpoint and read on
// every oim node start invocation.
package nodeconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Config is the persisted contributor configuration.
// AllowedModels is the only field the dashboard adds beyond what the example
// YAML already covered; empty means "allow all downloaded Exo models."
type Config struct {
	ExoURL               string   `json:"exo_url"`
	MemoryCapPct         float64  `json:"memory_cap_pct"`
	GeographicHint       string   `json:"geographic_hint"`
	ReachabilityEndpoint string   `json:"reachability_endpoint"`
	PodEndpoint          string   `json:"pod_endpoint"`
	AllowedModels        []string `json:"allowed_models"` // empty = all
	SensitivityCap       string   `json:"sensitivity_cap"`
	// Schedule controls when this node contributes to the mesh. Zero value
	// (Mode == "") behaves as ScheduleModeAlways — fully backward compatible
	// with configs saved before this field existed.
	Schedule Schedule `json:"schedule"`
	// DraftModels configures speculative decoding pairings, keyed by served
	// model_id — see DraftModelConfig. Nil/empty (the default for every
	// existing config) means no speculative decoding is configured; this is
	// forward-compatible plumbing only (see that type's doc comment) and
	// never changes behavior on its own. Mirrors protocol.DraftModelConfig's
	// shape (duplicated, not imported, to keep this package dependency-light
	// — same reasoning as validSensitivityCaps below); internal/agent
	// converts between the two at the boundary where it already imports both.
	DraftModels map[string]DraftModelConfig `json:"draft_models,omitempty"`
}

// DraftModelConfig pairs a served model with a smaller "draft" model for
// speculative decoding — forward-compatible plumbing for TODO.md's
// "Speculative decoding on node side." exo-explore/exo's /v1/chat/completions
// and /instance HTTP APIs accept no draft-model parameter as of this writing
// (only the underlying mlx-lm CLI's --draft-model/--num-draft-tokens flags
// do), so configuring this is inert until Exo's API grows a matching
// parameter. Field names match mlx-lm's own flags so this repo is ready to
// pass them through the moment it does.
type DraftModelConfig struct {
	DraftModelID   string `json:"draft_model_id"`
	NumDraftTokens int    `json:"num_draft_tokens,omitempty"` // 0 = let Exo pick its own default
}

func Default() Config {
	return Config{
		ExoURL:         "http://localhost:52415",
		MemoryCapPct:   0.5,
		GeographicHint: "us",
		SensitivityCap: "moderate",
	}
}

func ConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "oim", "config.json")
}

func Load() (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(ConfigPath())
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(data, &cfg)
	return cfg, err
}

func Save(cfg Config) error {
	// Validate before persisting so a bad value is rejected at the write
	// boundary (dashboard POST /config or CLI flags) instead of silently
	// producing surprising runtime behavior later.
	if err := Validate(cfg); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
