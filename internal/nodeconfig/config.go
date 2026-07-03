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
	return os.WriteFile(path, data, 0o644)
}
